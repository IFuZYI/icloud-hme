package mail

import (
	"errors"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// TestConnectSendsIMAPIDForNetEaseStyleServer 验证网易 163 风格服务器门禁:
// LOGIN 后必须先发 ID 声明, 否则 SELECT 返回 "Unsafe Login"。
//
// 修复前本测试失败(RED): Connect 成功但 InboxCount 报
// "Unsafe Login. Please contact kefu@188.com for help", 即线上 502 根因。
func TestConnectSendsIMAPIDForNetEaseStyleServer(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{RequireID: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("user@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect 应成功: %v", err)
	}
	defer c.Disconnect()

	if !srv.ReceivedID() {
		t.Fatal("客户端未发送非空 ID 声明")
	}
	if _, err := c.InboxCount(); err != nil {
		t.Fatalf("ID 声明后 SELECT 应成功, got: %v", err)
	}
}

// TestConnectIDRejectedStillUsable 验证服务器对 ID 回 NO 时连接仍可用
// (best-effort: ID 失败只记日志, 不影响后续命令)。
func TestConnectIDRejectedStillUsable(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{RejectID: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("user@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("ID 被拒不应让 Connect 失败: %v", err)
	}
	defer c.Disconnect()

	if !srv.ReceivedID() {
		t.Fatal("客户端未发送非空 ID 声明")
	}
	if _, err := c.InboxCount(); err != nil {
		t.Fatalf("ID 被拒后连接应仍可用: %v", err)
	}
}

// TestConnectSkipsIDWhenNotAdvertised 验证服务器未声明 ID 能力时
// (iCloud imap.mail.me.com 行为)客户端不得发送 ID 命令。
func TestConnectSkipsIDWhenNotAdvertised(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{HideID: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("user@icloud.com", "app-password", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect 应成功: %v", err)
	}
	defer c.Disconnect()

	if srv.ReceivedID() {
		t.Fatal("服务器未声明 ID 能力, 客户端不应发送 ID")
	}
	if _, err := c.InboxCount(); err != nil {
		t.Fatalf("InboxCount 应成功: %v", err)
	}
}

// TestListInboxNetEaseStyleFolders 验证网易风格文件夹行为:
//   - 缺失文件夹返回 "Folder not exist"(163 实测措辞)必须被静默跳过;
//   - 垃圾邮件夹名为「垃圾邮件」而非 Junk, 必须一并扫描。
//
// 修复前本测试失败(RED): isNoSuchFolder 不识别 "Folder not exist",
// 整个列表读取直接报错。
func TestListInboxNetEaseStyleFolders(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{
		NetEaseFolderErr: true,
		JunkFolder:       "垃圾邮件",
	})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("user@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Disconnect()

	now := time.Now()
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "收件箱邮件", now.Add(-2*time.Minute))
	srv.AddMessage(t, "垃圾邮件", "b@example.com", "u@163.com", "垃圾邮件里的验证码", now.Add(-1*time.Minute))

	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("网易风格文件夹扫描失败(应跳过缺失的 Junk 并读「垃圾邮件」): %v", err)
	}
	if total != 2 || len(msgs) != 2 {
		t.Fatalf("应合并 INBOX+垃圾邮件 共 2 封, got %d/total %d", len(msgs), total)
	}
	if !strings.Contains(msgs[0].Subject, "验证码") {
		t.Fatalf("新→旧排序: 第一封应为垃圾邮件夹中的验证码, got %q", msgs[0].Subject)
	}
}

// TestIsNoSuchFolderRejectsOtherErrors 钉住「文件夹不存在」判定的拒绝面:
// 该谓词的调用点都是「SELECT 失败 → 跳过该夹继续扫描」——若谓词被写宽
// (例如恒 true 或误收其他错误), 所有 SELECT 错误(含 ID 门禁的
// "Unsafe Login"、网络错误)都会被静默吞成空列表, 比直接报错更难排查。
// 审查发现: 恒 true 变异可存活(其他测试只覆盖接受面), 本测试即补此护栏。
func TestIsNoSuchFolderRejectsOtherErrors(t *testing.T) {
	accept := []string{
		"No such mailbox",          // iCloud/标准实现
		"does not exist",           // 旧措辞(保留兼容)
		"Folder not exist",         // 网易 163/126 实测措辞
		"EXAMINE Folder not exist", // 带命令名前缀的真实响应
	}
	for _, msg := range accept {
		if !isNoSuchFolder(errors.New(msg)) {
			t.Fatalf("isNoSuchFolder(%q) = false, want true", msg)
		}
	}
	reject := []string{
		"Unsafe Login. Please contact kefu@188.com for help", // ID 门禁错误: 必须抛出而非静默吞掉
		"IMAP 连接失败: i/o timeout",
		"NO [LIMIT] Too many connections",
		"",
	}
	for _, msg := range reject {
		if msg == "" {
			if isNoSuchFolder(nil) {
				t.Fatal("isNoSuchFolder(nil) = true, want false")
			}
			continue
		}
		if isNoSuchFolder(errors.New(msg)) {
			t.Fatalf("isNoSuchFolder(%q) = true, want false(错误会被静默吞掉)", msg)
		}
	}
}
