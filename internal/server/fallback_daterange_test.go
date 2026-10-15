package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
)

// ====================================================================
// Web API 回退路径的日期区间过滤(端到端)
// ====================================================================
//
// 回归背景: IMAP 不可用时回退 Web API, 该路径曾完全忽略日期区间——
// 用户选「最近 1 天」却看到 60 天前的邮件。与 IMAP 路径的服务端 SEARCH
// 过滤语义不一致(审查发现)。
//
// 契约: 回退路径同样按区间过滤, total 为过滤后的数量。
func TestInboxWebFallbackRespectsDateRange(t *testing.T) {
	// 假 mccgateway: 返回 3 封线程(1 天前 / 30 天前 / 60 天前)。
	now := time.Now()
	threads := []map[string]any{
		{"threadId": "t_recent", "subject": "最近邮件", "senders": []string{"a@example.com"}, "preview": "验证码 111111", "timestamp": now.Add(-24 * time.Hour).UnixMilli()},
		{"threadId": "t_30d", "subject": "30 天前", "senders": []string{"b@example.com"}, "preview": "验证码 222222", "timestamp": now.Add(-30 * 24 * time.Hour).UnixMilli()},
		{"threadId": "t_60d", "subject": "60 天前", "senders": []string{"c@example.com"}, "preview": "验证码 333333", "timestamp": now.Add(-60 * 24 * time.Hour).UnixMilli()},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mailws2/v1/thread/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalThreadsReturned": len(threads),
			"threadList":           threads,
		})
	})
	gateway := httptest.NewServer(mux)
	defer gateway.Close()
	t.Cleanup(mail.OverrideWebGatewayForTest(gateway.URL))

	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	// 账号只有 Cookie(无 App Password): IMAP 必然失败 → 走 Web API 回退。
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateCookies(sum.ID, map[string]string{
		"X-APPLE-WEBAUTH-TOKEN": "v=1:t=x",
		"X-APPLE-WEBAUTH-USER":  "v=1:s=1:d=19657242416",
	}); err != nil {
		t.Fatalf("写入 Cookie 失败: %v", err)
	}

	b := &managerBackend{mgr: mgr}

	// 近 7 天: 只剩最近 1 封。
	r, err := b.ListInbox(InboxQuery{
		AccountID: sum.ID,
		Limit:     20,
		DateRange: mail.DateRange{Start: now.AddDate(0, 0, -7)},
	})
	if err != nil {
		t.Fatalf("回退读信失败: %v", err)
	}
	if r.Method != "web_api" {
		t.Fatalf("应走 Web API 回退, got method=%q", r.Method)
	}
	if len(r.Messages) != 1 || r.Messages[0].ID != "t_recent" {
		t.Fatalf("近 7 天应只剩 t_recent, got %+v", r.Messages)
	}
	if r.Total != 1 {
		t.Fatalf("total 应为过滤后的 1, got %d", r.Total)
	}

	// 近 90 天: 全部保留。
	r90, err := b.ListInbox(InboxQuery{
		AccountID: sum.ID,
		Limit:     20,
		DateRange: mail.DateRange{Start: now.AddDate(0, 0, -90)},
	})
	if err != nil {
		t.Fatalf("90 天回退读信失败: %v", err)
	}
	if len(r90.Messages) != 3 || r90.Total != 3 {
		t.Fatalf("90 天应保留 3 封, got %d/total %d", len(r90.Messages), r90.Total)
	}
}

// 回退路径按别名筛选同样遵守日期区间。
func TestInboxWebFallbackAliasRespectsDateRange(t *testing.T) {
	now := time.Now()
	threads := []map[string]any{
		{"threadId": "t1", "subject": "alias123@icloud.com 验证码", "senders": []string{"a@example.com"}, "preview": "111111", "timestamp": now.Add(-2 * time.Hour).UnixMilli()},
		{"threadId": "t2", "subject": "alias123@icloud.com 旧邮件", "senders": []string{"a@example.com"}, "preview": "222222", "timestamp": now.Add(-40 * 24 * time.Hour).UnixMilli()},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mailws2/v1/thread/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalThreadsReturned": len(threads),
			"threadList":           threads,
		})
	})
	gateway := httptest.NewServer(mux)
	defer gateway.Close()
	t.Cleanup(mail.OverrideWebGatewayForTest(gateway.URL))

	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateCookies(sum.ID, map[string]string{
		"X-APPLE-WEBAUTH-TOKEN": "v=1:t=x",
		"X-APPLE-WEBAUTH-USER":  "v=1:s=1:d=19657242416",
	}); err != nil {
		t.Fatalf("写入 Cookie 失败: %v", err)
	}

	b := &managerBackend{mgr: mgr}
	r, err := b.ListInbox(InboxQuery{
		AccountID: sum.ID,
		Alias:     "alias123@icloud.com",
		Limit:     20,
		DateRange: mail.DateRange{Start: now.AddDate(0, 0, -7)},
	})
	if err != nil {
		t.Fatalf("回退读信失败: %v", err)
	}
	if len(r.Messages) != 1 || r.Messages[0].ID != "t1" {
		t.Fatalf("近 7 天应只剩 t1, got %+v (total=%d)", r.Messages, r.Total)
	}
	if r.Total != 1 {
		t.Fatalf("total 应为 1, got %d", r.Total)
	}
	_ = fmt.Sprintf // 保留 fmt 引用
}
