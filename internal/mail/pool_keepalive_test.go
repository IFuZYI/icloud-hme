package mail

import (
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// ====================================================================
// Dovecot 文件夹措辞: 第三种主流「文件夹不存在」形态
// ====================================================================
//
// 回归背景: isNoSuchFolder 只识别 iCloud("No such mailbox")与网易
// ("Folder not exist")两种措辞; Dovecot("Mailbox doesn't exist: <名>")
// 匹配不上——一个缺失的候选夹(如无 Junk 的自建服务器)会让整个列表读取
// 报错而非静默跳过。Dovecot 是自建/企业邮箱最常见的实现, 必须覆盖。
//
// 契约: 缺失文件夹(任意上述措辞)静默跳过, 其余 SELECT 错误照常抛出。
func TestListInboxDovecotStyleMissingFolderSkipped(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{DovecotFolderErr: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("user@example.com", "pass", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Disconnect()

	now := time.Now()
	srv.AddMessage(t, "INBOX", "a@example.com", "u@example.com", "收件箱邮件", now.Add(-time.Minute))

	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("Dovecot 风格缺失文件夹应被静默跳过: %v", err)
	}
	if total != 1 || len(msgs) != 1 {
		t.Fatalf("应读到 INBOX 的 1 封, got %d/total %d", len(msgs), total)
	}
}

// 谓词单元面: Dovecot 措辞必须接受; 其余错误仍必须拒绝(防止谓词被写宽)。
func TestIsNoSuchFolderAcceptsDovecotWording(t *testing.T) {
	accept := []string{
		"Mailbox doesn't exist: Junk",
		"Mailbox doesn't exist: 垃圾邮件",
		"NO Mailbox doesn't exist: Junk",
	}
	for _, msg := range accept {
		if !isNoSuchFolder(errString(msg)) {
			t.Fatalf("isNoSuchFolder(%q) = false, want true", msg)
		}
	}
	reject := []string{
		"Unsafe Login. Please contact kefu@188.com for help",
		"IMAP 连接失败: i/o timeout",
		"NO [LIMIT] Too many connections",
	}
	for _, msg := range reject {
		if isNoSuchFolder(errString(msg)) {
			t.Fatalf("isNoSuchFolder(%q) = true, want false(错误会被静默吞掉)", msg)
		}
	}
}

// ====================================================================
// 连接池保活与空闲重建: 会话次数是外部邮箱风控的第一触发信号
// ====================================================================
//
// 背景: 池内连接空闲超过 idleClose 后下次借用直接重建(TLS+LOGIN);
// 保活(NOOP)持续续命的连接不会被重建。对 163 等外部收件邮箱,
// 减少 TLS+LOGIN 次数是降风控的核心手段。

// errString 把字符串包成 error(表驱动测试用)。
type errString string

func (e errString) Error() string { return string(e) }

// TestPoolKeepaliveReusesConnection 验证: 空闲超过 idleClose 的连接,
// 有保活时仍被复用(不重建); 无保活时被重建。
//
// 修复前本测试失败(RED): 池无保活, 空闲超过阈值一律重建。
func TestPoolKeepaliveReusesConnection(t *testing.T) {
	prevIdle, prevKeepalive := PoolIdleClose, PoolKeepaliveInterval
	PoolIdleClose = 50 * time.Millisecond
	PoolKeepaliveInterval = 20 * time.Millisecond
	t.Cleanup(func() { PoolIdleClose, PoolKeepaliveInterval = prevIdle, prevKeepalive })

	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	pool := NewPool()
	defer pool.Close()

	call := func() {
		t.Helper()
		err := pool.DoWithServer("ext:"+srv.Host+":993:u@163.com", "u@163.com", "auth-code", srv.Host, srv.Port, func(c *Client) error {
			_, e := c.InboxCount()
			return e
		})
		if err != nil {
			t.Fatalf("池调用失败: %v", err)
		}
	}

	call()
	conns1 := srv.ConnCount()
	noops1 := srv.NoopCount()
	// 空闲超过 idleClose: 保活应持续续命(等待期内有周期 NOOP)。
	time.Sleep(120 * time.Millisecond)
	if got := srv.NoopCount(); got == noops1 {
		t.Fatalf("空闲期内应有保活 NOOP, 基线 %d → %d", noops1, got)
	}
	call()

	if got := srv.ConnCount(); got != conns1 {
		t.Fatalf("有保活时应复用连接(新建 0), 实际新建 %d 个", got-conns1)
	}
}

// TestPoolWithoutKeepaliveRebuildsAfterIdle 验证保活关闭时保持现状语义:
// 空闲超过 idleClose 的连接下次借用时重建(避免用一个大概率已被服务端
// 静默断开的连接)。
func TestPoolWithoutKeepaliveRebuildsAfterIdle(t *testing.T) {
	prevIdle, prevKeepalive := PoolIdleClose, PoolKeepaliveInterval
	PoolIdleClose = 30 * time.Millisecond
	PoolKeepaliveInterval = 0 // 关闭保活
	t.Cleanup(func() { PoolIdleClose, PoolKeepaliveInterval = prevIdle, prevKeepalive })

	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	pool := NewPool()
	defer pool.Close()

	call := func() {
		t.Helper()
		err := pool.DoWithServer("ext:"+srv.Host+":993:u@163.com", "u@163.com", "auth-code", srv.Host, srv.Port, func(c *Client) error {
			_, e := c.InboxCount()
			return e
		})
		if err != nil {
			t.Fatalf("池调用失败: %v", err)
		}
	}

	call()
	conns1 := srv.ConnCount()
	time.Sleep(80 * time.Millisecond)
	call()

	if got := srv.ConnCount(); got == conns1 {
		t.Fatal("保活关闭且空闲超阈值时应重建连接")
	}
}

// TestPoolKeepaliveDisabledStopsNoop 验证 PoolKeepaliveInterval=0 时
// 不发保活 NOOP(测试环境与不需要保活的场景)。
func TestPoolKeepaliveDisabledStopsNoop(t *testing.T) {
	prevKeepalive := PoolKeepaliveInterval
	PoolKeepaliveInterval = 0
	t.Cleanup(func() { PoolKeepaliveInterval = prevKeepalive })

	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	pool := NewPool()
	defer pool.Close()

	if err := pool.DoWithServer("ext:"+srv.Host+":993:u@163.com", "u@163.com", "auth-code", srv.Host, srv.Port, func(c *Client) error {
		_, e := c.InboxCount()
		return e
	}); err != nil {
		t.Fatalf("池调用失败: %v", err)
	}

	// 连接阶段本身会发 NOOP(Connect 探活 + ensure 探活), 记录基线后等待,
	// 等待期内不应有新 NOOP。
	base := srv.NoopCount()
	time.Sleep(150 * time.Millisecond)
	if got := srv.NoopCount(); got != base {
		t.Fatalf("保活关闭时不应有周期 NOOP, 基线 %d → %d", base, got)
	}
}
