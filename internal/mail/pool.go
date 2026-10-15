// IMAP 连接池: 按账号复用长连接, 避免每次读信都 TLS+Login。
package mail

import (
	"fmt"
	"sync"
	"time"
)

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
}

type pooledConn struct {
	mu       sync.Mutex
	key      string
	password string
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
// key 为 iCloud 邮箱(默认服务器 imap.mail.me.com)。
func (p *Pool) Do(appleID, appPassword string, fn func(*Client) error) error {
	if appleID == "" || appPassword == "" {
		return fmt.Errorf("IMAP 凭据为空")
	}
	return p.do(appleID, appPassword, func() *Client { return NewClient(appleID, appPassword) }, fn)
}

// DoWithServer 与 Do 相同, 但连接指向自定义 IMAP 服务器(外部收件邮箱, 如 163)。
// key 应包含服务器/端口/邮箱, 保证不同邮箱不共享连接(避免串用凭据)。
func (p *Pool) DoWithServer(key, username, password, server string, port int, fn func(*Client) error) error {
	if key == "" || username == "" || password == "" {
		return fmt.Errorf("IMAP 凭据为空")
	}
	return p.do(key, password, func() *Client { return NewClientWithServer(username, password, server, port) }, fn)
}

func (p *Pool) do(key, password string, newClient func() *Client, fn func(*Client) error) error {
	pc := p.getOrCreate(key, password, newClient)
	pc.mu.Lock()
	defer pc.mu.Unlock()

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

// Close 关闭池内全部连接。
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, pc := range p.items {
		pc.mu.Lock()
		pc.stopKeepalive()
		if pc.client != nil {
			pc.client.Disconnect()
			pc.client = nil
		}
		pc.mu.Unlock()
		delete(p.items, k)
	}
}

func (p *Pool) getOrCreate(key, password string, newClient func() *Client) *pooledConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pc, ok := p.items[key]; ok {
		pc.mu.Lock()
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
		return pc
	}
	pc := &pooledConn{key: key, password: password, newClient: newClient}
	p.items[key] = pc
	return pc
}

func (pc *pooledConn) ensure(idleClose, keepalive time.Duration) error {
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
			pc.startKeepalive(keepalive)
			return nil
		}
		pc.stopKeepalive()
		pc.client.forceClose()
		pc.client = nil
	}
	c := pc.newClient()
	if err := c.Connect(); err != nil {
		return err
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
