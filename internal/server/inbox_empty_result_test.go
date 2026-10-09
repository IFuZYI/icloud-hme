package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// TestListInboxEmptyAliasResultSerializesMessagesAsArray 验证空结果的 JSON 契约:
// data.messages 必须是数组([]), 不能是 null。
//
// dogfood 实测: 按别名筛选且无命中时, FindByRecipientRange 返回 nil 切片,
// JSON 序列化成 "messages":null, 前端 data.messages.filter(...) 抛
// TypeError → 整页白屏(React 无错误边界)。契约修复: 空结果永远输出 []。
func TestListInboxEmptyAliasResultSerializesMessagesAsArray(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{EmptyHeaderSearch: true})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))

	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163", Email: "u@163.com", IMAPHost: srv.Host, IMAPPort: srv.Port, Password: "auth-code",
	}); err != nil {
		t.Fatal(err)
	}
	// 箱里只有别人的邮件: 按本别名筛选必然无命中。
	srv.AddMessage(t, "INBOX", "noreply@example.com", "other@icloud.com", "无关邮件", time.Now().Add(-time.Minute))

	s := newWithBackend(&managerBackend{mgr: mgr}, Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
		AutoTaskFile:  t.TempDir() + "/alias_task.json",
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, "GET",
		"/api/inbox?account_id="+sum.ID+"&alias=nobody%40icloud.com&days=7", "")
	if status != 200 {
		t.Fatalf("status = %d: %s", status, body)
	}
	if strings.Contains(body, `"messages":null`) {
		t.Fatalf("空结果的 messages 不应为 null(前端会白屏): %s", body)
	}
	var out struct {
		Data InboxResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Messages == nil {
		t.Fatalf("messages 应为空数组而非 nil: %s", body)
	}
	if len(out.Data.Messages) != 0 || out.Data.Total != 0 {
		t.Fatalf("无命中时应为空, got %+v", out.Data)
	}
}
