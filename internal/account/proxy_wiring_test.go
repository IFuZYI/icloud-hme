package account

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// TestMailPathsUseAccountProxy 验证账号代理被传给所有 IMAP 建连路径:
// iCloud 读信(连接池 Do)、外部收件邮箱读信(DoWithServer)、
// 绑定收件邮箱(测连接)、设置 App 专用密码(测连接)。
//
// 同一账号的 IMAP 出口必须与 HME API 一致; 漏传代理会让 IMAP 从服务器
// 真实 IP 直连, 账号在服务商侧出现「多出口」画像, 是风控触发信号。
func TestMailPathsUseAccountProxy(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	target := net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))

	var (
		mu   sync.Mutex
		seen []string
	)
	restore := mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		mu.Lock()
		seen = append(seen, proxy)
		mu.Unlock()
		return mailtest.DialInsecure(target)
	})
	t.Cleanup(restore)

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const proxy = "socks5://user:pass@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{
		Name:        "主号",
		ICloudEmail: "a@icloud.com",
		Proxy:       proxy,
	})
	if err != nil {
		t.Fatalf("AddAccountWithInput: %v", err)
	}
	// 直接注入 App Password, 避免触发真实网络校验。
	m.mu.Lock()
	m.accounts[sum.ID].AppPassword = "app-password"
	m.mu.Unlock()

	// 路径 1: 未绑定外部邮箱时, 读信走 iCloud IMAP(Pool.Do)。
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient(iCloud): %v", err)
	}

	// 路径 2: 绑定外部收件邮箱时的连接测试。
	if err := m.SetMailbox(sum.ID, MailboxConfig{
		Provider: "163",
		Email:    "u@163.com",
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	}); err != nil {
		t.Fatalf("SetMailbox: %v", err)
	}

	// 路径 3: 绑定后读信走外部邮箱(Pool.DoWithServer)。
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient(外部邮箱): %v", err)
	}

	// 路径 4: 设置 App 专用密码时的连接测试。
	if err := m.SetAppPassword(sum.ID, "a@icloud.com", "app-password"); err != nil {
		t.Fatalf("SetAppPassword: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 4 {
		t.Fatalf("应有 4 条 IMAP 建连路径, 实际拨号 %d 次: %v", len(got), got)
	}
	for i, p := range got {
		if p != proxy {
			t.Fatalf("第 %d 条路径未传账号代理: got %q, want %q", i+1, p, proxy)
		}
	}
}

// TestWebMailClientUsesAccountProxy 验证 Web 邮件回退客户端也带账号代理。
// 回退路径若直连, 同样暴露真实出口 IP, 破坏单一出口前提。
func TestWebMailClientUsesAccountProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const proxy = "http://user:pass@127.0.0.1:8080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	// 直接注入 Cookie, 避免 AddAccountWithInput 触发真实网络校验。
	m.mu.Lock()
	m.accounts[sum.ID].Cookies = map[string]string{"X-APPLE-WEBAUTH-USER": "v=1:s=1:d=12345"}
	m.mu.Unlock()

	wmc, err := m.WebMailClient(sum.ID)
	if err != nil {
		t.Fatalf("WebMailClient: %v", err)
	}
	if got := wmc.Proxy(); got != proxy {
		t.Fatalf("Web 邮件客户端未使用账号代理: got %q, want %q", got, proxy)
	}
}

// TestUpdateProxyClosesPooledConnections 验证改代理后旧 IMAP 连接被关闭:
// 旧连接会继续按 NOOP 保活, 从旧出口发流量并保留旧 IP 上的已认证会话,
// 与「同一账号单一出口」的风控要求冲突。
func TestUpdateProxyClosesPooledConnections(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	id := newMailboxAccount(t, m, "主号", "u@163.com", srv)
	if err := m.WithMailClient(id, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient: %v", err)
	}
	poolLen := func() int {
		m.mu.Lock()
		pool := m.imapPools[id]
		m.mu.Unlock()
		if pool == nil {
			return 0
		}
		return pool.Len()
	}
	if got := poolLen(); got != 1 {
		t.Fatalf("读信后池中应有 1 条连接, 实际 %d", got)
	}
	m.mu.Lock()
	oldPool := m.imapPools[id]
	m.mu.Unlock()

	if _, err := m.UpdateProxy(id, "socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("UpdateProxy: %v", err)
	}
	// 必须观察旧池本身, 不能只看新池: 新池天然为空, 断言 0 是恒真的。
	deadline := time.Now().Add(2 * time.Second)
	for oldPool.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := oldPool.Len(); got != 0 {
		t.Fatalf("改代理后旧池连接应全部关闭, 旧池剩余 %d", got)
	}
	// 池对象关闭还不够: 必须证明旧 TCP 连接真的被掐断, 否则旧会话仍会
	// 从旧出口发 NOOP(风控要防的正是这个)。
	deadline = time.Now().Add(2 * time.Second)
	for srv.ActiveConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.ActiveConns(); got != 0 {
		t.Fatalf("改代理后旧连接应已断开, 服务端仍有 %d 条活跃连接", got)
	}
	if err := oldPool.Do("u@163.com", "auth-code", "", func(*mail.Client) error { return nil }); !errors.Is(err, mail.ErrPoolClosed) {
		t.Fatalf("旧池应已关闭, Do 应返回 ErrPoolClosed, 实际 %v", err)
	}
	if got := poolLen(); got != 0 {
		t.Fatalf("新池应为空, 实际 %d", got)
	}
}

// TestManagerClosePreventsNewPools 验证 Manager.Close 之后不会再创建新池
// 或发起连接(否则关闭后仍有 keepalive 从旧出口发 NOOP)。
func TestManagerClosePreventsNewPools(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := newMailboxAccount(t, m, "主号", "u@163.com", srv)
	m.Close()

	if err := m.WithMailClient(id, func(*mail.Client) error { return nil }); !errors.Is(err, mail.ErrPoolClosed) {
		t.Fatalf("Close 后 WithMailClient 应返回 ErrPoolClosed, 实际 %v", err)
	}
	m.mu.Lock()
	poolCount := len(m.imapPools)
	m.mu.Unlock()
	if poolCount != 0 {
		t.Fatalf("Close 后不应再创建池, 实际 %d", poolCount)
	}
}

// TestManagerReloadAfterCloseRejected 验证 Close 之后 Reload 不会复活 Manager:
// 关闭闩锁必须一直有效, 否则 reload 会重新建池并从旧出口拨号。
func TestManagerReloadAfterCloseRejected(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := newMailboxAccount(t, m, "主号", "u@163.com", srv)
	m.Close()

	if err := m.Reload(); err == nil {
		t.Fatal("Close 后 Reload 应返回错误")
	}
	if err := m.WithMailClient(id, func(*mail.Client) error { return nil }); !errors.Is(err, mail.ErrPoolClosed) {
		t.Fatalf("Reload 被拒后 WithMailClient 仍应返回 ErrPoolClosed, 实际 %v", err)
	}
	m.mu.Lock()
	poolCount := len(m.imapPools)
	m.mu.Unlock()
	if poolCount != 0 {
		t.Fatalf("Reload 被拒后不应创建池, 实际 %d", poolCount)
	}
}

// TestRemoveAndReAddAccountProxyAllowed 验证删除账号再以同一邮箱/代理重建后,
// 旧池与旧配置不会残留导致读信失败。
func TestRemoveAndReAddAccountProxyAllowed(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(func(addr string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const proxy = "socks5://127.0.0.1:1080"
	add := func() string {
		sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: proxy})
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		m.accounts[sum.ID].AppPassword = "app-password"
		m.mu.Unlock()
		return sum.ID
	}

	id := add()
	if err := m.WithMailClient(id, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("首次读信: %v", err)
	}
	if !m.RemoveAccount(id) {
		t.Fatal("RemoveAccount 应成功")
	}

	id2 := add()
	if err := m.WithMailClient(id2, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("重建账号后读信不应被旧池/旧配置拦截: %v", err)
	}
}

// TestUpdateProxyClosesOnlyTargetAccount 验证改 A 的代理不会断开 B 的 IMAP
// 会话: 全池关闭会让其它账号被迫重连(TLS+LOGIN), 本身就是风控信号。
func TestUpdateProxyClosesOnlyTargetAccount(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})

	var (
		mu     sync.Mutex
		dialed []string
	)
	t.Cleanup(mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		mu.Lock()
		dialed = append(dialed, proxy)
		mu.Unlock()
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	idA := newMailboxAccount(t, m, "号A", "u1@163.com", srv)
	idB := newMailboxAccount(t, m, "号B", "u2@163.com", srv)
	poolLen := func(id string) int {
		m.mu.Lock()
		pool := m.imapPools[id]
		m.mu.Unlock()
		if pool == nil {
			return 0
		}
		return pool.Len()
	}
	for _, id := range []string{idA, idB} {
		if err := m.WithMailClient(id, func(*mail.Client) error { return nil }); err != nil {
			t.Fatalf("WithMailClient(%s): %v", id, err)
		}
	}
	if poolLen(idA) != 1 || poolLen(idB) != 1 {
		t.Fatalf("两个账号应各有 1 条连接, 实际 A=%d B=%d", poolLen(idA), poolLen(idB))
	}
	m.mu.Lock()
	oldA := m.imapPools[idA]
	m.mu.Unlock()

	if _, err := m.UpdateProxy(idA, "socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("UpdateProxy: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for oldA.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if oldA.Len() != 0 {
		t.Fatalf("账号 A 的旧池应已关闭, 剩余 %d", oldA.Len())
	}
	if poolLen(idA) != 0 || poolLen(idB) != 1 {
		t.Fatalf("只应替换账号 A 的池, 实际 A=%d B=%d", poolLen(idA), poolLen(idB))
	}
	// A 用新代理重新建连, B 的连接保持不变。
	if err := m.WithMailClient(idA, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient(A 新代理): %v", err)
	}
	if poolLen(idA) != 1 || poolLen(idB) != 1 {
		t.Fatalf("A 应新建 1 条连接, 实际 A=%d B=%d", poolLen(idA), poolLen(idB))
	}
	// B 的连接仍可复用(再次读信不新增连接)。
	if err := m.WithMailClient(idB, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient(B): %v", err)
	}
	if poolLen(idA) != 1 || poolLen(idB) != 1 {
		t.Fatalf("账号 B 应复用连接, 实际 A=%d B=%d", poolLen(idA), poolLen(idB))
	}

	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()
	// 2 次 SetMailbox 连接测试 + 2 次初始读信 + 1 次 A 用新代理重连;
	// B 的第二次读信必须复用, 不能再产生拨号。
	if len(got) != 5 {
		t.Fatalf("应有 5 次拨号, 实际 %d: %v", len(got), got)
	}
	if got[4] != "socks5://127.0.0.1:1080" {
		t.Fatalf("A 重连应使用新代理, 实际 %q", got[4])
	}
}
