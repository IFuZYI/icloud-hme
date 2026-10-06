package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"icloud-hme/internal/account"
)

// TestCheckAccountEndpoint 验证 POST /api/accounts/:id/check 的成功路径:
// 返回脱敏摘要、需要会话与 CSRF。
func TestCheckAccountEndpoint(t *testing.T) {
	f := &fakeBackend{
		accounts:     []account.Summary{{ID: "acc_1", Name: "主号", Status: "active", LastValidated: "2026-10-06T12:00:00Z"}},
		checkSummary: account.Summary{ID: "acc_1", Name: "主号", Status: "active", LastValidated: "2026-10-06T12:00:00Z"},
	}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/accounts/acc_1/check", `{}`)
	if status != http.StatusOK {
		t.Fatalf("check = %d: %s", status, body)
	}
	if f.checkedID != "acc_1" {
		t.Fatalf("Backend 收到的 ID = %q", f.checkedID)
	}
	var out struct {
		Data account.Summary `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Status != "active" || out.Data.ID != "acc_1" {
		t.Fatalf("data = %+v", out.Data)
	}
	// 脱敏: 响应不得含秘密字段本体(has_cookies/has_app_password 是布尔标志, 允许)
	if contains(body, `"cookies"`) || contains(body, `"app_password"`) || contains(body, `"proxy"`) {
		t.Fatalf("响应泄露秘密字段: %s", body)
	}
}

// TestCheckAccountEndpointSessionExpired 验证会话失效时返回
// UPSTREAM_UNAUTHORIZED(前端据此提示重新登录/更新 Cookie)。
func TestCheckAccountEndpointSessionExpired(t *testing.T) {
	f := &fakeBackend{
		accounts: []account.Summary{{ID: "acc_1", Name: "主号", Status: "error"}},
		checkErr: &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话已失效，请重新登录或更新 Cookie"},
	}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/accounts/acc_1/check", `{}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("check = %d: %s", status, body)
	}
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "UPSTREAM_UNAUTHORIZED" {
		t.Fatalf("code = %q", out.Code)
	}
}

// TestCheckAccountEndpointRequiresCSRF 验证写接口的 CSRF 保护未被绕过。
func TestCheckAccountEndpointRequiresCSRF(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号", Status: "active"}}}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts/acc_1/check", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	status, _, _ := do(t, req)
	if status != http.StatusForbidden {
		t.Fatalf("无 CSRF 的请求 = %d, 期望 403", status)
	}
}
