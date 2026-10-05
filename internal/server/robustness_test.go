package server

import (
	"net/http"
	"strings"
	"testing"
)

// 请求体超限必须被拒绝(413),而不是让超大 JSON 进入解析。
// 历史问题: maxBodyBytes 只声明未使用, 1 MiB 上限实际未生效。
func TestRequestBodyLimitEnforced(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	huge := `{"password":"` + strings.Repeat("A", 2<<20) + `"}`
	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/accounts/acc_1/login/begin", huge)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应 413, got %d: %s", status, body[:minInt(200, len(body))])
	}
	if !strings.Contains(body, "请求体过大") {
		t.Fatalf("错误文案应说明请求体过大: %s", body)
	}
}

// 正常大小的请求体不受影响。
func TestRequestBodyLimitAllowsNormalRequests(t *testing.T) {
	fb := &fakeBackend{}
	_, ts := newTestServer(t, fb)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-tasks", `{"account_id":"acc_1","mode":"scheduled","interval_minutes":20,"batch_count":1,"target_count":2}`)
	if status == http.StatusRequestEntityTooLarge {
		t.Fatalf("正常请求不应被 413 拦截: %s", body)
	}
}

// 421 是 iCloud 的会话失效信号(mail 服务在 Cookie 失效时返回 421),
// 必须与会话错误同类处理,而不是误报 502。
func TestIsSessionErrorRecognizes421(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"HTTP 421: Misdirected Request", true},
		{"mail 421 session expired", true},
		{"HTTP 502: Bad Gateway", false},
		{"连接失败: timeout", false},
	}
	for _, tc := range cases {
		if got := isSessionError(tc.msg); got != tc.want {
			t.Fatalf("isSessionError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
