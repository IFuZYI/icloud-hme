package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 邮件清理 HTTP 接口: 权限 + 账号范围
// ====================================================================
//
// 契约:
//   - 全部清理接口需 inbox.purge 权限(与 inbox.delete 分离);
//   - 账号范围: preview/create 用 body account_id 判定;
//     purge-tasks/:id 用任务真实归属(不接受自述);
//   - 范围外 → 403 ACCOUNT_NOT_ALLOWED; 任务路由伪装 404 防探测;
//   - 同账号并发 → 409 PURGE_IN_PROGRESS。

// newPurgeHTTPHarness 组装带 HTTP 服务的清理测试环境。
func newPurgeHTTPHarness(t *testing.T, n int) (*Server, *httptestServer, *mailtest.Server) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	now := time.Now()
	for i := 0; i < n; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@icloud.com",
			"旧邮件", now.AddDate(0, 0, -10).Add(time.Duration(i)*time.Second))
	}
	c := mail.NewClientWithServer("u@icloud.com", "app-password", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)

	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主账号"}, {ID: "acc_2", Name: "备账号"}}}
	s, ts := newTestServer(t, f)
	s.purge.withClient = func(accountID string, fn func(*mail.Client) error) error {
		return fn(c)
	}
	s.purge.sleep = func(time.Duration) {}
	return s, &httptestServer{ts}, srv
}

// httptestServer 轻包装(便于复用 do() 的签名)。
type httptestServer struct{ ts *httptest.Server }

// 权限: 无 inbox.purge 的秘钥 → 403 PERMISSION_DENIED。
func TestPurgeRequiresPurgePermission(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 3)
	_ = s
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.list") // 只有 list

	body := `{"account_id":"acc_1","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req, _ := http.NewRequest("POST", hts.ts.URL+"/api/inbox/purge/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "PERMISSION_DENIED") {
		t.Fatalf("无 purge 权限应 403, got %d %s", code, raw)
	}
}

// 账号范围: 范围外账号的预览 → 403。
func TestPurgePreviewScopeDenied(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 3)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")

	body := `{"account_id":"acc_2","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req, _ := http.NewRequest("POST", hts.ts.URL+"/api/inbox/purge/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("范围外预览应 403 ACCOUNT_NOT_ALLOWED, got %d %s", code, raw)
	}
}

// 预览: 范围内账号返回统计。
func TestPurgePreviewScopedAllows(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 7)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")

	body := `{"account_id":"acc_1","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req, _ := http.NewRequest("POST", hts.ts.URL+"/api/inbox/purge/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("范围内预览应 200, got %d %s", code, raw)
	}
	if !strings.Contains(raw, `"total":7`) {
		t.Fatalf("预览应统计 7 封: %s", raw)
	}
}

// 任务路由: 范围外任务伪装 404(防探测)。
func TestPurgeTaskScopeHidesAs404(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 3)
	// 直接造一个 acc_2 的任务(模拟其它账号创建)。
	clock := &purgeTestClock{now: time.Now()}
	s.purge.now = clock.Now
	tk, err := s.purge.create("acc_2", "备账号", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")

	req, _ := http.NewRequest("POST", hts.ts.URL+"/api/purge-tasks/"+tk.ID+"/pause", nil)
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 404 {
		t.Fatalf("范围外任务应伪装 404, got %d %s", code, raw)
	}
}

// 列表: 只返回范围内账号的任务。
func TestPurgeTaskListScoped(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 3)
	tk1, _ := s.purge.create("acc_1", "主账号", time.Now(), time.Now())
	_, _ = s.purge.create("acc_2", "备账号", time.Now(), time.Now())
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")

	req, _ := http.NewRequest("GET", hts.ts.URL+"/api/purge-tasks", nil)
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("列表应 200, got %d %s", code, raw)
	}
	if strings.Contains(raw, "备账号") {
		t.Fatalf("列表泄露范围外账号任务: %s", raw)
	}
	if !strings.Contains(raw, tk1.ID) {
		t.Fatalf("列表应含范围内任务: %s", raw)
	}
}

// 同账号并发: 第二个任务 → 409 PURGE_IN_PROGRESS。
func TestPurgeConflict409(t *testing.T) {
	s, hts, _ := newPurgeHTTPHarness(t, 3)
	// 先占用 acc_1。
	if _, err := s.purge.create("acc_1", "主账号", time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	session, csrf := login(t, hts.ts, "admin-pass-2026-strong")
	_ = session
	_ = csrf

	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.purge")
	body := `{"account_id":"acc_1","range_start":"2020-01-01T00:00:00Z","range_end":"2030-01-01T00:00:00Z"}`
	req, _ := http.NewRequest("POST", hts.ts.URL+"/api/inbox/purge", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key.Key)
	code, raw, _ := do(t, req)
	if code != 409 || !strings.Contains(raw, "PURGE_IN_PROGRESS") {
		t.Fatalf("同账号并发应 409, got %d %s", code, raw)
	}
}
