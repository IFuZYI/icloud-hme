package account

import (
	"testing"
	"time"

	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// TestSetMailboxNetEaseStyleServer 验证「接入收件邮箱」在网易 163 风格服务器上
// 可以成功: LOGIN 后必须补发 RFC 2971 ID 声明, 否则 SELECT 被拒,
// 表现为 502(修复前本测试失败于 "Unsafe Login")。
func TestSetMailboxNetEaseStyleServer(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{
		RequireID:        true,
		NetEaseFolderErr: true,
		JunkFolder:       "垃圾邮件",
	})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}

	err = m.SetMailbox(sum.ID, MailboxConfig{
		Provider: "163",
		Email:    "u@163.com",
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	})
	if err != nil {
		t.Fatalf("SetMailbox(163 风格服务器)应成功, got: %v", err)
	}
	if !srv.ReceivedID() {
		t.Fatal("客户端未发送非空 ID 声明")
	}

	acc, ok := m.GetAccount(sum.ID)
	if !ok || acc.Mailbox == nil {
		t.Fatal("SetMailbox 后账号应保存收件邮箱配置")
	}
	if acc.Mailbox.Email != "u@163.com" || acc.Mailbox.IMAPHost != srv.Host || acc.Mailbox.IMAPPort != srv.Port {
		t.Fatalf("收件邮箱配置保存不完整: %+v", acc.Mailbox)
	}

	// 重启(重新加载)后配置仍在。
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	acc2, ok := m2.GetAccount(sum.ID)
	if !ok || acc2.Mailbox == nil {
		t.Fatal("重新加载后收件邮箱配置应仍在")
	}
	if acc2.Mailbox.Email != "u@163.com" {
		t.Fatalf("重新加载后邮箱 = %q, 期望 u@163.com", acc2.Mailbox.Email)
	}
}

// TestMailboxReadPathNetEaseStyle 验证接入后的读信路径:
// 缺失文件夹("Folder not exist")被静默跳过, 「垃圾邮件」文件夹一并扫描。
func TestMailboxReadPathNetEaseStyle(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{
		RequireID:        true,
		NetEaseFolderErr: true,
		JunkFolder:       "垃圾邮件",
	})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMailbox(sum.ID, MailboxConfig{
		Provider: "163",
		Email:    "u@163.com",
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	}); err != nil {
		t.Fatalf("SetMailbox: %v", err)
	}

	now := time.Now()
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "收件箱邮件", now.Add(-2*time.Minute))
	srv.AddMessage(t, "垃圾邮件", "b@example.com", "u@163.com", "垃圾邮件里的验证码", now.Add(-1*time.Minute))

	var msgs []mail.Message
	var total int
	err = m.WithMailClient(sum.ID, func(mc *mail.Client) error {
		var e error
		msgs, total, e = mc.ListInboxPageRange(10, 0, mail.DateRange{})
		return e
	})
	if err != nil {
		t.Fatalf("读信路径失败(应跳过缺失的 Junk 并读「垃圾邮件」): %v", err)
	}
	if total != 2 || len(msgs) != 2 {
		t.Fatalf("应读到 INBOX+垃圾邮件 共 2 封, got %d/total %d", len(msgs), total)
	}
}
