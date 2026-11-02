// IMAP 连接池: 按账号复用长连接, 避免每次读信都 TLS+Login。
package mail

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrPoolClosed 表示连接池已关闭, 不再接受新的借出。
var ErrPoolClosed = errors.New("IMAP 连接池已关闭")

// PoolIdleClose 是池内连接的空闲重建阈值: 空闲超过该时长的连接在下次借用前
// 直接重建(不再 Ping 一个大概率已被服务端静默断开的连接)。配合保活使用——
// 保活持续续命的活跃会话不会被重建, 减少 TLS+LOGIN 次数(外部邮箱风控的第一
// 触发信号)。用 var 而非 const: 测试覆盖空闲行为。
var PoolIdleClose = 30 * time.Minute

// PoolKeepaliveInterval 是池内空闲连接的 NOOP 保活周期; 0 表示关闭保活。
// 主流 IMAP 客户端都会把会话期间的长连接维持住(我们的对应手段是 NOOP):
// 空闲连接每周期发一次 NOOP, 避免服务端/NAT 静默断连后用户下次操作时的重连。
// 用 var 而非 const: 测试覆盖保活行为。
var PoolKeepaliveInterval = 5 * time.Minute

// Pool 管理按 key 复用的 IMAP 长连接。同一 key 串行使用(go-imap 非并发安全)。
type Pool struct {
	mu    sync.Mutex
	items map[string]*pooledConn
	// idleClose 空闲超过该时间则下次使用前重建; 0 表示不主动关。
	idleClose time.Duration
	// keepalive 是空闲连接的 NOOP 保活周期; 0 表示关闭保活。
	keepalive time.Duration
	// closed 表示池已关闭; Close 之后不允许再建连。
	closed bool
}

type pooledConn struct {
	mu       sync.Mutex
	key      string
	password string
	// closed 表示该连接已被池关闭; 关闭后即使有 Do 已拿到指针也不得再建连,
	// 否则会留下一个不在池中、却持续保活旧出口的孤儿连接。
	closed bool
	// retired 在池项被摘除时原子置位, 供已排队等 pc.mu 的 Do 在拿到锁后
	// 立即放弃, 不再通过旧代理发起 TLS+LOGIN。
	retired atomic.Bool
	// newClient 惰性建连时使用的工厂(区分 iCloud 默认服务器与外部收件邮箱)。
	newClient func() *Client
	client    *Client
	lastUsed  time.Time
	// keepaliveStop 是当前连接代际的保活停止通道; nil 表示无保活在跑。
	// 保活 goroutine 每周期发一次 NOOP, 失败即丢弃连接并退出。
	keepaliveStop chan struct{}
	// lastKeepalive 是最近一次成功保活(NOOP)的时间; 零值表示从未保活。
	lastKeepalive time.Time
}

// NewPool 创建连接池。空闲重建阈值与保活周期在创建时从包级配置读取
// (PoolIdleClose / PoolKeepaliveInterval), 测试可先改配置再建池。
func NewPool() *Pool {
	return &Pool{
		items:     make(map[string]*pooledConn),
		idleClose: PoolIdleClose,
		keepalive: PoolKeepaliveInterval,
	}
}

// Do 借出已连接的 Client 执行 fn; 用完不 Logout, 连接留在池中。
// key 为 iCloud 邮箱(默认服务器 imap.mail.me.com); proxy 为空表示直连。
// 池键包含代理: 换代理后必须新建连接, 否则流量仍从旧出口出去。
func (p *Pool) Do(appleID, appPassword, proxy string, fn func(*Client) error) error {
	if appleID == "" || appPassword == "" {
		return fmt.Errorf("IMAP 凭据为空")
	}
	return p.do(appleID, appPassword, proxy, func() *Client {
		return NewClient(appleID, appPassword).WithProxy(proxy)
	}, fn)
}

// DoWithServer 与 Do 相同, 但连接指向自定义 IMAP 服务器(外部收件邮箱, 如 163)。
// key 应包含服务器/端口/邮箱, 保证不同邮箱不共享连接(避免串用凭据)。
func (p *Pool) DoWithServer(key, username, password, server string, port int, proxy string, fn func(*Client) error) error {
	if key == "" || username == "" || password == "" {
		return fmt.Errorf("IMAP 凭据为空")
	}
	return p.do(key, password, proxy, func() *Client {
		return NewClientWithServer(username, password, server, port).WithProxy(proxy)
	}, fn)
}

func (p *Pool) do(key, password, proxy string, newClient func() *Client, fn func(*Client) error) error {
	// 代理是连接身份的一部分: 同一邮箱换代理不得复用旧连接。
	if proxy != "" {
		key += "\x00" + proxy
	}
	// 坏连接(代理/NAT 静默断开、服务端断开)最多在新连接上重试一次:
	// 单次失败直接上抛会表现为「读信失败 → 回退 Web API」, 用户看到收不到邮件。
	// 读信(list/preview/detail)幂等; 删除路径重试同样安全(缺失 UID 的
	// STORE 返回 OK, EXPUNGE 无操作, UIDVALIDITY 内 UID 不复用)。
	const maxConnRetries = 1
	connRetries := 0
	for {
		if p.isClosed() {
			return ErrPoolClosed
		}
		pc, err := p.getOrCreate(key, password, newClient)
		if err != nil {
			return err
		}
		retry := false
		// 用闭包 + defer 解锁: 调用方 fn 可能 panic(MIME 解析等),
		// 不能把 pc.mu 永久留在锁定状态, 否则该账号后续读信全部卡死。
		func() {
			pc.mu.Lock()
			defer pc.mu.Unlock()
			defer func() {
				if r := recover(); r != nil {
					// fn panic 时协议状态未知, 不能把连接放回池里复用。
					pc.stopKeepalive()
					if pc.client != nil {
						pc.client.forceClose()
						pc.client = nil
					}
					panic(r)
				}
			}()
			if pc.closed || pc.retired.Load() {
				// Close 在我们拿到指针后关闭了该连接; 换新池项重试,
				// 绝不能在这个已脱离池的 pc 上重新建连。
				retry = true
				return
			}
			err = p.useConn(pc, fn)
		}()
		if retry {
			continue
		}
		if err != nil && isLikelyConnErr(err) && connRetries < maxConnRetries {
			// 连接已坏(useConn 已丢弃该连接); 在新连接上重试一次。
			connRetries++
			continue
		}
		return err
	}
}

// useConn 在已持有 pc.mu 的前提下执行一次借出; 调用方负责解锁。
func (p *Pool) useConn(pc *pooledConn, fn func(*Client) error) error {
	if err := pc.ensure(p.idleClose, p.keepalive); err != nil {
		return err
	}
	err := fn(pc.client)
	pc.lastUsed = time.Now()
	if err != nil && isLikelyConnErr(err) {
		// 连接坏了, 丢掉, 下次重建
		pc.stopKeepalive()
		pc.client.forceClose()
		pc.client = nil
	}
	return err
}

// Len 返回池中连接数(诊断/测试用)。
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

func (p *Pool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Close 关闭池内全部连接。先摘除池项并原子标记 retired, 再异步关闭,
// 避免持池锁做网络收尾; 已排队的 Do 会在拿到 pc.mu 后立即放弃。
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	items := p.items
	p.items = make(map[string]*pooledConn)
	for _, pc := range items {
		pc.retired.Store(true)
	}
	p.mu.Unlock()
	for _, pc := range items {
		go pc.close()
	}
}

// close 标记连接已关闭并立即断开, 防止已借出指针的 Do 重新建连。
func (pc *pooledConn) close() {
	pc.retired.Store(true)
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.closed = true
	pc.stopKeepalive()
	if pc.client != nil {
		pc.client.forceClose()
		pc.client = nil
	}
}

func (p *Pool) getOrCreate(key, password string, newClient func() *Client) (*pooledConn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}
	if pc, ok := p.items[key]; ok {
		p.mu.Unlock()
		// 不在 p.mu 下等待 pc.mu: 在途 IMAP 读可能持有它长达
		// imapSocketTimeout, 否则 Close/Len 都会被拖住。
		pc.mu.Lock()
		if pc.closed {
			pc.mu.Unlock()
			return pc, nil
		}
		// 密码变更则丢弃旧连接(凭据不再匹配); 工厂始终刷新为最新配置。
		if pc.password != password {
			pc.stopKeepalive()
			if pc.client != nil {
				pc.client.forceClose()
				pc.client = nil
			}
			pc.password = password
		}
		pc.newClient = newClient
		pc.mu.Unlock()
		return pc, nil
	}
	pc := &pooledConn{key: key, password: password, newClient: newClient}
	p.items[key] = pc
	p.mu.Unlock()
	return pc, nil
}

func (pc *pooledConn) ensure(idleClose, keepalive time.Duration) error {
	if pc.closed || pc.retired.Load() {
		return ErrPoolClosed
	}
	if pc.client != nil {
		// 空闲太久主动重建, 避免服务端静默断连。
		// 「空闲」取「最近一次使用」与「最近一次保活成功」中较晚者——
		// 保活持续续命的连接不算空闲, 不会被误重建(重建=TLS+LOGIN,
		// 是外部邮箱风控的第一触发信号)。
		idleSince := pc.lastUsed
		if pc.lastKeepalive.After(idleSince) {
			idleSince = pc.lastKeepalive
		}
		if idleClose > 0 && !idleSince.IsZero() && time.Since(idleSince) > idleClose {
			pc.stopKeepalive()
			pc.client.forceClose()
			pc.client = nil
		}
	}
	if pc.client != nil {
		if err := pc.client.Ping(); err == nil {
			if pc.retired.Load() {
				pc.stopKeepalive()
				pc.client.forceClose()
				pc.client = nil
				return ErrPoolClosed
			}
			pc.startKeepalive(keepalive)
			return nil
		}
		pc.stopKeepalive()
		pc.client.forceClose()
		pc.client = nil
	}
	c := pc.newClient()
	if pc.retired.Load() {
		return ErrPoolClosed
	}
	if err := c.Connect(); err != nil {
		return err
	}
	if pc.retired.Load() {
		// 建连期间代理被切换: 立即断开, 不让本次连接承载任何邮件操作。
		c.forceClose()
		return ErrPoolClosed
	}
	pc.client = c
	pc.lastUsed = time.Now()
	// 新连接代际: 保活时间清零, 避免旧代际的保活时间把新连接误判为「活跃」。
	pc.lastKeepalive = time.Time{}
	pc.startKeepalive(keepalive)
	return nil
}

// startKeepalive 为当前连接代际启动 NOOP 保活(已启动或 interval<=0 时为空操作)。
// 调用方必须持有 pc.mu。
func (pc *pooledConn) startKeepalive(interval time.Duration) {
	if interval <= 0 || pc.keepaliveStop != nil || pc.client == nil {
		return
	}
	stop := make(chan struct{})
	pc.keepaliveStop = stop
	go pc.keepaliveLoop(stop, interval)
}

// stopKeepalive 请求当前保活 goroutine 退出。不等待其结束(它可能正阻塞在
// pc.mu 上): goroutine 醒来后按代际(stop 通道)判定自己是否过期并退出。
// 调用方必须持有 pc.mu。
func (pc *pooledConn) stopKeepalive() {
	if pc.keepaliveStop == nil {
		return
	}
	close(pc.keepaliveStop)
	pc.keepaliveStop = nil
}

// keepaliveLoop 每 interval 向服务端发一次 NOOP, 续命空闲连接并证明其存活:
//   - NOOP 成功: 更新 lastKeepalive(空闲判定据此排除活跃会话);
//   - NOOP 失败: 连接已死, 丢弃之, 下次借用重建;
//   - 代际过期(被 stopKeepalive 替换): 立即退出, 不碰新代际的连接。
func (pc *pooledConn) keepaliveLoop(stop chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		pc.mu.Lock()
		if pc.keepaliveStop != stop {
			// 代际已变更(被停止或替换): 本 goroutine 作废。
			pc.mu.Unlock()
			return
		}
		if pc.client == nil {
			pc.mu.Unlock()
			return
		}
		err := pc.client.Ping()
		if err == nil {
			pc.lastKeepalive = time.Now()
			pc.mu.Unlock()
			continue
		}
		// 连接已死: 丢弃并结束本代际保活。
		pc.client.forceClose()
		pc.client = nil
		pc.keepaliveStop = nil
		pc.mu.Unlock()
		return
	}
}

func isLikelyConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// 常见断连/IO 错误关键字
	for _, k := range []string{
		"connection reset", "broken pipe", "EOF", "i/o timeout",
		"use of closed", "not connected", "connection refused",
		// go-imap 在等待命令响应期间连接被关闭(代理/NAT 静默断开)时返回
		// "imap: connection closed"。漏掉它, 坏连接会留在池里反复失败。
		"connection closed",
		"IMAP 连接", "wsarecv", "wsasend",
	} {
		if containsFold(s, k) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		len(sub) == 0 ||
		indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	// 小写 ASCII 子串查找, 够用
	sl := toLowerASCII(s)
	subl := toLowerASCII(sub)
	for i := 0; i+len(subl) <= len(sl); i++ {
		if sl[i:i+len(subl)] == subl {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
