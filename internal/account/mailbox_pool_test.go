package account

import (
	"testing"
	"time"

	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// newMailboxAccount 建一个带外部收件邮箱(163 风格)的账号, 返回 manager 与账号 ID。
func newMailboxAccount(t *testing.T, m *Manager, name, mailboxEmail string, srv *mailtest.Server) string {
	t.Helper()
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: name, ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMailbox(sum.ID, MailboxConfig{
		Provider: "163",
		Email:    mailboxEmail,
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	}); err != nil {
		t.Fatalf("SetMailbox(%s): %v", mailboxEmail, err)
	}
	return sum.ID
}

// TestExternalMailboxConnectionReusedAcrossReads 验证外部收件邮箱(如 163)的
// 读信路径复用长连接: 每次读信都重新 TLS+LOGIN 会被服务商视为高频异常访问,
// 是触发风控/封禁的典型信号。修复前每次 WithMailClient 都新建连接(RED)。
func TestExternalMailboxConnectionReusedAcrossReads(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	id := newMailboxAccount(t, m, "主号", "u@163.com", srv)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "第一封", time.Now())

	before := srv.ConnCount()
	for i := 0; i < 2; i++ {
		var n int
		err := m.WithMailClient(id, func(mc *mail.Client) error {
			msgs, _, e := mc.ListInboxPageRange(10, 0, mail.DateRange{})
			n = len(msgs)
			return e
		})
		if err != nil {
			t.Fatalf("第 %d 次读信失败: %v", i+1, err)
		}
		if n != 1 {
			t.Fatalf("第 %d 次读信应看到 1 封邮件, got %d", i+1, n)
		}
	}
	if delta := srv.ConnCount() - before; delta != 1 {
		t.Fatalf("两次读信应复用连接(仅新建 1 个), 实际新建 %d 个", delta)
	}
}

// TestExternalMailboxPoolKeyedByMailbox 验证连接池的键落在「邮箱」而非「账号」上。
//
// 背景变更(审计 B1): 同一个外部收件邮箱不再允许绑定到两个账号——共享物理邮箱
// 会让一个账号的受限 API 秘钥读到/删除另一个账号别名的邮件。绑定阶段已拒绝
// 重复(TestSetMailboxRejectsDuplicateEmail), 但池本身仍必须按邮箱键复用连接,
// 否则同一邮箱在重连/多客户端场景下会被反复建连(服务商风控信号)。
func TestExternalMailboxPoolKeyedByMailbox(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	srv.AddMessage(t, "INBOX", "a@example.com", "same@163.com", "邮件", time.Now())
	before := srv.ConnCount()
	for i := 0; i < 2; i++ {
		err := m.imapPool.DoWithServer("same@163.com", "same@163.com", "auth-code", srv.Host, srv.Port, func(mc *mail.Client) error {
			_, _, e := mc.ListInboxPageRange(10, 0, mail.DateRange{})
			return e
		})
		if err != nil {
			t.Fatalf("第 %d 次读信失败: %v", i+1, err)
		}
	}
	if delta := srv.ConnCount() - before; delta != 1 {
		t.Fatalf("同一收件邮箱应共享 1 个连接, 实际新建 %d 个", delta)
	}
}

// TestExternalMailboxConnectionNotSharedAcrossMailboxes 验证不同收件邮箱不共享
// 连接——共享会串用凭据/读错邮箱内容; 每个邮箱各自一条连接。
func TestExternalMailboxConnectionNotSharedAcrossMailboxes(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	idA := newMailboxAccount(t, m, "号A", "u1@163.com", srv)
	idB := newMailboxAccount(t, m, "号B", "u2@163.com", srv)
	srv.AddMessage(t, "INBOX", "a@example.com", "u1@163.com", "邮件", time.Now())

	before := srv.ConnCount()
	for _, id := range []string{idA, idB} {
		if err := m.WithMailClient(id, func(mc *mail.Client) error {
			_, _, e := mc.ListInboxPageRange(10, 0, mail.DateRange{})
			return e
		}); err != nil {
			t.Fatalf("读信失败: %v", err)
		}
	}
	if delta := srv.ConnCount() - before; delta != 2 {
		t.Fatalf("不同收件邮箱应各建 1 个连接(共 2 个), 实际新建 %d 个", delta)
	}
}
