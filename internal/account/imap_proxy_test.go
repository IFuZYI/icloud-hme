package account

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

func strPtr(s string) *string { return &s }

func accountByID(t *testing.T, m *Manager, id string) *Account {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc := m.accounts[id]
	if acc == nil {
		t.Fatalf("账号 %s 不存在", id)
	}
	return acc
}

// TestSummaryReportsIMAPProxy 验证对外摘要只暴露 has_imap_proxy 布尔值,
// 绝不回显 IMAP 代理地址本身(含 user:pass)。
func TestSummaryReportsIMAPProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.HasIMAPProxy {
		t.Fatal("未设置时 has_imap_proxy 应为 false")
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("socks5://u:p@127.0.0.1:1080")); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	sum = accountByID(t, m, sum.ID).Summary()
	if !sum.HasIMAPProxy {
		t.Fatal("设置后 has_imap_proxy 应为 true")
	}
}

// TestUpdateProxiesSetsIMAPProxyIndependently 验证 IMAP 代理与账号代理相互独立:
// 只改 IMAP 代理不能动账号代理; 清空 IMAP 代理后 IMAP 直连(不回退账号代理)。
func TestUpdateProxiesSetsIMAPProxyIndependently(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const accountProxy = "http://u:p@127.0.0.1:8080"
	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: accountProxy})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.UpdateProxies(sum.ID, nil, strPtr(imapProxy)); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	acc := accountByID(t, m, sum.ID)
	if acc.Proxy != accountProxy {
		t.Fatalf("账号代理不应被改动, 实际 %q", acc.Proxy)
	}
	if acc.IMAPProxy != imapProxy {
		t.Fatalf("IMAP 代理应为 %q, 实际 %q", imapProxy, acc.IMAPProxy)
	}
	if !acc.Summary().HasIMAPProxy || !acc.Summary().HasProxy {
		t.Fatal("两个代理标记都应为 true")
	}

	// 清空 IMAP 代理: 账号代理保持不变。
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("")); err != nil {
		t.Fatalf("清除 IMAP 代理: %v", err)
	}
	acc = accountByID(t, m, sum.ID)
	if acc.IMAPProxy != "" {
		t.Fatalf("IMAP 代理应被清空, 实际 %q", acc.IMAPProxy)
	}
	if acc.Proxy != accountProxy {
		t.Fatalf("账号代理不应被改动, 实际 %q", acc.Proxy)
	}
	if acc.Summary().HasIMAPProxy {
		t.Fatal("清空后 has_imap_proxy 应为 false")
	}
}

// TestUpdateProxiesPartialUpdateLeavesOtherFieldUntouched 验证指针为 nil 的字段
// 表示「不改动」: 老客户端只发 proxy 时不得把 IMAP 代理清掉。
func TestUpdateProxiesPartialUpdateLeavesOtherFieldUntouched(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const accountProxy = "http://u:p@127.0.0.1:8080"
	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: accountProxy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr(imapProxy)); err != nil {
		t.Fatal(err)
	}

	// 只改账号代理(模拟旧客户端请求体只有 proxy)。
	const newAccountProxy = "http://u:p@127.0.0.1:8081"
	if _, err := m.UpdateProxies(sum.ID, strPtr(newAccountProxy), nil); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	acc := accountByID(t, m, sum.ID)
	if acc.Proxy != newAccountProxy {
		t.Fatalf("账号代理应为 %q, 实际 %q", newAccountProxy, acc.Proxy)
	}
	if acc.IMAPProxy != imapProxy {
		t.Fatalf("IMAP 代理不应被改动, 实际 %q", acc.IMAPProxy)
	}

	// 只改 IMAP 代理。
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("socks5://u:p@127.0.0.1:1081")); err != nil {
		t.Fatal(err)
	}
	acc = accountByID(t, m, sum.ID)
	if acc.Proxy != newAccountProxy {
		t.Fatalf("账号代理不应被改动, 实际 %q", acc.Proxy)
	}
}

// TestUpdateProxiesRejectsInvalidIMAPProxy 验证非法 IMAP 代理被拒绝, 且不落盘、
// 不改内存。
func TestUpdateProxiesRejectsInvalidIMAPProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "http://u:p@127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("ftp://x")); err == nil {
		t.Fatal("非法 IMAP 代理应返回错误")
	}
	acc := accountByID(t, m, sum.ID)
	if acc.IMAPProxy != "" {
		t.Fatalf("非法输入不应写入, 实际 %q", acc.IMAPProxy)
	}
	if acc.Proxy != "http://u:p@127.0.0.1:8080" {
		t.Fatalf("账号代理不应被改动, 实际 %q", acc.Proxy)
	}
}

// TestUpdateProxiesRejectsInvalidAccountProxy 验证非法账号代理同样被拒绝,
// 且 IMAP 代理不受影响。
func TestUpdateProxiesRejectsInvalidAccountProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr(imapProxy)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, strPtr("ftp://x"), nil); err == nil {
		t.Fatal("非法账号代理应返回错误")
	}
	acc := accountByID(t, m, sum.ID)
	if acc.Proxy != "" {
		t.Fatalf("非法输入不应写入, 实际 %q", acc.Proxy)
	}
	if acc.IMAPProxy != imapProxy {
		t.Fatalf("IMAP 代理不应被改动, 实际 %q", acc.IMAPProxy)
	}
}

// TestIMAPPathsPreferIMAPProxy 验证设置 IMAP 专用代理后, 所有 IMAP 建连路径
// 都用它而不是账号代理: iCloud 读信、外部收件邮箱读信、绑定邮箱测连接、
// 设置 App 专用密码测连接。
func TestIMAPPathsPreferIMAPProxy(t *testing.T) {
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
	dialed := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}

	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const accountProxy = "http://u:p@127.0.0.1:8080"
	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: accountProxy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr(imapProxy)); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].AppPassword = "app-password"
	m.mu.Unlock()

	// 路径 1: iCloud 读信(连接池 Do)。
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient: %v", err)
	}
	// 路径 2: 绑定外部收件邮箱(测连接 + 后续读信)。
	if err := m.SetMailbox(sum.ID, MailboxConfig{
		Provider: "163",
		Email:    "u@163.com",
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	}); err != nil {
		t.Fatalf("SetMailbox: %v", err)
	}
	// 路径 3: 设置 App 专用密码(测连接)。
	if err := m.SetAppPassword(sum.ID, "a@icloud.com", "new-app-password"); err != nil {
		t.Fatalf("SetAppPassword: %v", err)
	}

	got := dialed()
	if len(got) < 3 {
		t.Fatalf("应至少拨号 3 次, 实际 %d: %v", len(got), got)
	}
	for i, p := range got {
		if p != imapProxy {
			t.Fatalf("第 %d 次拨号应使用 IMAP 代理 %q, 实际 %q (全部: %v)", i+1, imapProxy, p, got)
		}
	}
}

// TestUpdateIMAPProxyReplacesIMAPPool 验证改 IMAP 代理后旧池被整体替换:
// 旧连接关闭, 新读信按新代理重建(否则流量仍从旧出口出去)。
func TestUpdateIMAPProxyReplacesIMAPPool(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "http://u:p@127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].AppPassword = "app-password"
	m.mu.Unlock()

	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("首次读信: %v", err)
	}
	m.mu.Lock()
	oldPool := m.imapPools[sum.ID]
	m.mu.Unlock()
	if oldPool == nil || oldPool.Len() != 1 {
		t.Fatalf("读信后应有 1 条连接, 实际 %v", oldPool)
	}

	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("socks5://u:p@127.0.0.1:1080")); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for oldPool.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := oldPool.Len(); got != 0 {
		t.Fatalf("改 IMAP 代理后旧池应关闭, 实际剩余 %d", got)
	}
	m.mu.Lock()
	newPool := m.imapPools[sum.ID]
	m.mu.Unlock()
	if newPool == oldPool {
		t.Fatal("应安装新池")
	}
	if newPool.Len() != 0 {
		t.Fatalf("新池应为空, 实际 %d", newPool.Len())
	}
}

// TestAccountProxyChangeKeepsDedicatedIMAPPool 验证设置了 IMAP 专用代理时,
// 改账号代理不需要重建 IMAP 连接(IMAP 出口没变, 重建 TLS+LOGIN 反而是风控信号)。
func TestAccountProxyChangeKeepsDedicatedIMAPPool(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "http://u:p@127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateProxies(sum.ID, nil, strPtr("socks5://u:p@127.0.0.1:1080")); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].AppPassword = "app-password"
	m.mu.Unlock()
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("首次读信: %v", err)
	}
	m.mu.Lock()
	pool := m.imapPools[sum.ID]
	m.mu.Unlock()

	if _, err := m.UpdateProxies(sum.ID, strPtr("http://u:p@127.0.0.1:8081"), nil); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	m.mu.Lock()
	same := m.imapPools[sum.ID]
	m.mu.Unlock()
	if same != pool {
		t.Fatal("IMAP 出口未变, 不应替换 IMAP 连接池")
	}
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("再读信: %v", err)
	}
	if pool.Len() != 1 {
		t.Fatalf("应复用旧连接, 实际 %d", pool.Len())
	}
}

// TestAccountProxyChangeKeepsDirectIMAPPool 验证 IMAP 未配代理(直连)时,
// 改账号代理不会替换 IMAP 池: 两个代理互不关联。
func TestAccountProxyChangeKeepsDirectIMAPPool(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "http://u:p@127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].AppPassword = "app-password"
	m.mu.Unlock()
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("首次读信: %v", err)
	}
	m.mu.Lock()
	pool := m.imapPools[sum.ID]
	m.mu.Unlock()

	if _, err := m.UpdateProxies(sum.ID, strPtr("http://u:p@127.0.0.1:8081"), nil); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	m.mu.Lock()
	same := m.imapPools[sum.ID]
	m.mu.Unlock()
	if same != pool {
		t.Fatal("IMAP 直连且未改 IMAP 代理, 不应替换 IMAP 连接池")
	}
	if err := m.WithMailClient(sum.ID, func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("再读信: %v", err)
	}
	if pool.Len() != 1 {
		t.Fatalf("应复用旧连接, 实际 %d", pool.Len())
	}
}

// TestAddAccountWithIMAPProxy 验证创建账号时可以直接带上 IMAP 专用代理。
func TestAddAccountWithIMAPProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{
		Name:        "主号",
		ICloudEmail: "a@icloud.com",
		Proxy:       "http://u:p@127.0.0.1:8080",
		IMAPProxy:   imapProxy,
	})
	if err != nil {
		t.Fatalf("AddAccountWithInput: %v", err)
	}
	acc := accountByID(t, m, sum.ID)
	if acc.IMAPProxy != imapProxy {
		t.Fatalf("IMAP 代理应为 %q, 实际 %q", imapProxy, acc.IMAPProxy)
	}
	if !acc.Summary().HasIMAPProxy {
		t.Fatal("has_imap_proxy 应为 true")
	}
}

// TestAddAccountRejectsInvalidIMAPProxy 验证创建时非法 IMAP 代理被拒绝。
func TestAddAccountRejectsInvalidIMAPProxy(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if _, err := m.AddAccountWithInput(AddAccountInput{
		Name:        "主号",
		ICloudEmail: "a@icloud.com",
		IMAPProxy:   "ftp://x",
	}); err == nil {
		t.Fatal("非法 IMAP 代理应返回错误")
	}
	if got := len(m.accounts); got != 0 {
		t.Fatalf("失败时不应创建账号, 实际 %d 个", got)
	}
}

// TestValidateProxyAcceptsSocks5h 验证 socks5h 与 mail 层拨号能力一致
// (mail.normalizeProxyURL 支持 socks5h, 账号校验不能把它拒掉)。
func TestValidateProxyAcceptsSocks5h(t *testing.T) {
	got, err := validateProxy("socks5h://u:p@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("socks5h 应被接受: %v", err)
	}
	if got != "socks5h://u:p@127.0.0.1:1080" {
		t.Fatalf("应原样返回, 实际 %q", got)
	}
}

// TestEffectiveIMAPProxyDedicatedOnly 钉住语义: 只返回 IMAP 专用代理,
// 账号代理不会作为回退值出现(留空即 IMAP 直连)。
func TestEffectiveIMAPProxyDedicatedOnly(t *testing.T) {
	acc := &Account{Proxy: "http://u:p@127.0.0.1:8080"}
	if got := acc.EffectiveIMAPProxy(); got != "" {
		t.Fatalf("未设置 IMAP 代理时应返回空串(直连), 实际 %q", got)
	}
	acc.IMAPProxy = "socks5://u:p@127.0.0.1:1080"
	if got := acc.EffectiveIMAPProxy(); got != acc.IMAPProxy {
		t.Fatalf("应返回 IMAP 专用代理 %q, 实际 %q", acc.IMAPProxy, got)
	}
	var nilAcc *Account
	if got := nilAcc.EffectiveIMAPProxy(); got != "" {
		t.Fatalf("nil 账号应返回空串, 实际 %q", got)
	}
}

// TestLegacyAccountWithoutIMAPProxyGoesDirect 模拟升级路径: accounts.json 里
// 只写了 proxy 的存量账号, 加载后 IMAP 必须直连(新语义: 两个代理互不关联),
// 而不是偷偷回退到账号代理。
func TestLegacyAccountWithoutIMAPProxyGoesDirect(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	dir := t.TempDir()
	legacy := `{"accounts":{"acc_legacy":{` +
		`"id":"acc_legacy","name":"存量号","icloud_email":"a@icloud.com","host":"icloud.com",` +
		`"proxy":"http://u:p@127.0.0.1:8080","app_password":"app-password","status":"active"}}}`
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		mu   sync.Mutex
		seen []string
	)
	t.Cleanup(mail.OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		mu.Lock()
		seen = append(seen, proxy)
		mu.Unlock()
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))

	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.WithMailClient("acc_legacy", func(*mail.Client) error { return nil }); err != nil {
		t.Fatalf("WithMailClient: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) == 0 || got[0] != "" {
		t.Fatalf("存量账号未配 IMAP 代理时应直连, 实际 %v", got)
	}
}

// TestUpdateProxiesSameValueKeepsTimezone 验证原样再保存不会清掉时区缓存
// (代理值未变 = 无副作用, 不写盘、不换池)。
func TestUpdateProxiesSameValueKeepsTimezone(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const proxy = "http://u:p@127.0.0.1:8080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].Timezone = "Europe/Berlin"
	m.mu.Unlock()

	same := proxy
	if _, err := m.UpdateProxies(sum.ID, &same, nil); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	if got := accountByID(t, m, sum.ID).Timezone; got != "Europe/Berlin" {
		t.Fatalf("代理未变时不应清时区缓存, 实际 %q", got)
	}
}

// TestUpdateProxiesIMAPOnlyChangeKeepsTimezone 验证只改 IMAP 代理(账号代理传原值)
// 不应清掉账号代理的时区缓存。
func TestUpdateProxiesIMAPOnlyChangeKeepsTimezone(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const proxy = "http://u:p@127.0.0.1:8080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].Timezone = "Europe/Berlin"
	m.mu.Unlock()

	sameProxy := proxy
	newIMAP := "socks5://u:p@127.0.0.1:1080"
	if _, err := m.UpdateProxies(sum.ID, &sameProxy, &newIMAP); err != nil {
		t.Fatalf("UpdateProxies: %v", err)
	}
	if got := accountByID(t, m, sum.ID).Timezone; got != "Europe/Berlin" {
		t.Fatalf("账号代理未变时不应清时区缓存, 实际 %q", got)
	}
}

// TestIMAPProxyPersistsAcrossReload 验证 imap_proxy 能落盘并在重启后加载
// (JSON tag 写错会在这里暴露)。
func TestIMAPProxyPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	const imapProxy = "socks5://u:p@127.0.0.1:1080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", IMAPProxy: imapProxy})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()

	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if got := accountByID(t, reloaded, sum.ID).IMAPProxy; got != imapProxy {
		t.Fatalf("重启后 IMAP 代理应为 %q, 实际 %q", imapProxy, got)
	}
}

// TestUpdateProxiesSaveFailureRollsBack 验证落盘失败时 Proxy/IMAPProxy/Timezone
// 全部回滚, 不留「内存已换、磁盘未换」的不一致。
func TestUpdateProxiesSaveFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	const oldProxy = "http://u:p@127.0.0.1:8080"
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: oldProxy})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].Timezone = "Europe/Berlin"
	m.mu.Unlock()

	// 让 save() 必然失败: accounts.json 变成目录。
	dataFile := filepath.Join(dir, "accounts.json")
	if err := os.Remove(dataFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataFile, 0o755); err != nil {
		t.Fatal(err)
	}

	newProxy := "http://u:p@127.0.0.1:8081"
	newIMAP := "socks5://u:p@127.0.0.1:1080"
	if _, err := m.UpdateProxies(sum.ID, &newProxy, &newIMAP); err == nil {
		t.Fatal("落盘失败应返回错误")
	}
	acc := accountByID(t, m, sum.ID)
	if acc.Proxy != oldProxy || acc.IMAPProxy != "" || acc.Timezone != "Europe/Berlin" {
		t.Fatalf("落盘失败应全字段回滚, 实际 proxy=%q imap=%q tz=%q", acc.Proxy, acc.IMAPProxy, acc.Timezone)
	}
}

// TestUpdateProxiesUnknownAccount 验证账号不存在时返回错误。
func TestUpdateProxiesUnknownAccount(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_, err = m.UpdateProxies("missing", nil, strPtr("socks5://u:p@127.0.0.1:1080"))
	if err == nil {
		t.Fatal("未知账号应返回错误")
	}
	if !strings.Contains(err.Error(), "账号不存在") {
		t.Fatalf("错误应指明账号不存在, 实际 %v", err)
	}
}
