package mail

import (
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// TestFindByRecipientFallsBackWhenServerSearchReturnsEmpty 验证网易 Coremail 的
// SEARCH 缺陷兜底: 163/126 对 header 类条件(TO/FROM/SUBJECT/HEADER/TEXT)静默
// 返回空且不报错(原始协议实测: UID SEARCH TO "..." 返回 OK 但零结果, 邮件
// 明明在箱里)。只依赖服务端 SEARCH 会让「按别名筛选」在这类服务商上永远
// 查不到邮件; 空结果必须退回本地信封过滤。
func TestFindByRecipientFallsBackWhenServerSearchReturnsEmpty(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{EmptyHeaderSearch: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	srv.AddMessage(t, "INBOX", "noreply@example.com", "alias@icloud.com", "验证码 123456", time.Now().Add(-time.Minute))

	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	msgs, total, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(msgs) != 1 {
		t.Fatalf("空 SEARCH 兜底后应找到 1 封, got total=%d len=%d", total, len(msgs))
	}
	if msgs[0].Subject != "验证码 123456" {
		t.Fatalf("subject = %q, 期望 验证码 123456", msgs[0].Subject)
	}
}

// TestFindByRecipientBrokenSearchNoMatchStaysEmpty 验证兜底不会产生幻影匹配:
// 服务端 SEARCH 静默返回空、本地过滤也无命中时, 结果保持为空且无错误。
func TestFindByRecipientBrokenSearchNoMatchStaysEmpty(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{EmptyHeaderSearch: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	srv.AddMessage(t, "INBOX", "noreply@example.com", "other@icloud.com", "别人的邮件", time.Now().Add(-time.Minute))

	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	msgs, total, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 0 || len(msgs) != 0 {
		t.Fatalf("无命中时应为空, got total=%d len=%d", total, len(msgs))
	}
}

// TestFindByRecipientWorkingSearchStillUsesServerResults 验证正常服务端
// (SEARCH 可用)不受兜底影响: 命中走服务端结果, 无命中返回空。
func TestFindByRecipientWorkingSearchStillUsesServerResults(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	srv.AddMessage(t, "INBOX", "noreply@example.com", "alias@icloud.com", "命中邮件", time.Now().Add(-time.Minute))
	srv.AddMessage(t, "INBOX", "noreply@example.com", "other@icloud.com", "无关邮件", time.Now().Add(-2*time.Minute))

	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	msgs, total, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(msgs) != 1 || msgs[0].Subject != "命中邮件" {
		t.Fatalf("服务端 SEARCH 命中应只返回 1 封, got total=%d msgs=%+v", total, msgs)
	}

	msgs2, total2, err := c.FindByRecipientRange("nobody@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total2 != 0 || len(msgs2) != 0 {
		t.Fatalf("无命中时应为空, got total=%d len=%d", total2, len(msgs2))
	}
}
