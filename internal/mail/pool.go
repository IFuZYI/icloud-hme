// IMAP 连接池: 按账号复用长连接, 避免每次读信都 TLS+Login。
package mail

import (
	"fmt"
	"sync"
	"time"
)

// Pool 管理按 key 复用的 IMAP 长连接。同一 key 串行使用(go-imap 非并发安全)。
type Pool struct {
	mu    sync.Mutex
	items map[string]*pooledConn
	// idleClose 空闲超过该时间则下次使用前重建; 0 表示不主动关。
	idleClose time.Duration
}

type pooledConn struct {
	mu       sync.Mutex
	key      string
	password string
	// newClient 惰性建连时使用的工厂(区分 iCloud 默认服务器与外部收件邮箱)。
	newClient func() *Client
	client    *Client
	lastUsed  time.Time
}

// NewPool 创建连接池。
func NewPool() *Pool {
	return &Pool{
		items:     make(map[string]*pooledConn),
		idleClose: 10 * time.Minute,
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

	if err := pc.ensure(p.idleClose); err != nil {
		return err
	}
	err := fn(pc.client)
	pc.lastUsed = time.Now()
	if err != nil && isLikelyConnErr(err) {
		// 连接坏了, 丢掉, 下次重建
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

func (pc *pooledConn) ensure(idleClose time.Duration) error {
	if pc.client != nil {
		// 空闲太久主动重建, 避免服务端静默断连
		if idleClose > 0 && !pc.lastUsed.IsZero() && time.Since(pc.lastUsed) > idleClose {
			pc.client.forceClose()
			pc.client = nil
		}
	}
	if pc.client != nil {
		if err := pc.client.Ping(); err == nil {
			return nil
		}
		pc.client.forceClose()
		pc.client = nil
	}
	c := pc.newClient()
	if err := c.Connect(); err != nil {
		return err
	}
	pc.client = c
	pc.lastUsed = time.Now()
	return nil
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
