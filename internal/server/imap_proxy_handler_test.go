package server

import (
	"net/http"
	"strings"
	"testing"

	"icloud-hme/internal/account"
)

// TestUpdateProxyHandlerForwardsIMAPProxy 验证 PUT /api/accounts/:id/proxy
// 支持只更新 IMAP 代理(proxy 省略 = 不改动账号代理)。
func TestUpdateProxyHandlerForwardsIMAPProxy(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号"}}}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "PUT", "/api/accounts/acc_1/proxy", `{"imap_proxy":"socks5://u:p@127.0.0.1:1080"}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, body)
	}
	if f.proxyID != "acc_1" {
		t.Fatalf("账号 ID 应为 acc_1, 实际 %q", f.proxyID)
	}
	if f.proxyValue != nil {
		t.Fatalf("未提供的 proxy 应为 nil(不改动), 实际 %v", *f.proxyValue)
	}
	if f.imapProxyVal == nil || *f.imapProxyVal != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("imap_proxy 应转发, 实际 %v", f.imapProxyVal)
	}
}

// TestUpdateProxyHandlerForwardsBoth 验证同时提供两个代理时都转发。
func TestUpdateProxyHandlerForwardsBoth(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号"}}}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "PUT", "/api/accounts/acc_1/proxy",
		`{"proxy":"http://u:p@127.0.0.1:8080","imap_proxy":"socks5://u:p@127.0.0.1:1080"}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, body)
	}
	if f.proxyValue == nil || *f.proxyValue != "http://u:p@127.0.0.1:8080" {
		t.Fatalf("proxy 应转发, 实际 %v", f.proxyValue)
	}
	if f.imapProxyVal == nil || *f.imapProxyVal != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("imap_proxy 应转发, 实际 %v", f.imapProxyVal)
	}
}

// TestCreateAccountForwardsIMAPProxy 验证创建账号时 imap_proxy 被转发到
// account.AddAccountInput(否则创建后 IMAP 会直连)。
func TestCreateAccountForwardsIMAPProxy(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "POST", "/api/accounts",
		`{"name":"主号","icloud_email":"a@icloud.com","proxy":"http://u:p@127.0.0.1:8080","imap_proxy":"socks5://u:p@127.0.0.1:1080"}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("期望创建成功, 得到 %d: %s", status, body)
	}
	if f.addedInput.Proxy != "http://u:p@127.0.0.1:8080" {
		t.Fatalf("proxy 未转发: %q", f.addedInput.Proxy)
	}
	if f.addedInput.IMAPProxy != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("imap_proxy 未转发: %q", f.addedInput.IMAPProxy)
	}
}

// TestUpdateProxyHandlerNeverEchoesProxy 验证响应体只暴露布尔, 不回显代理
// 地址或凭据(代理串含 user:pass, 泄漏即凭据泄漏)。
func TestUpdateProxyHandlerNeverEchoesProxy(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号", HasProxy: true, HasIMAPProxy: true}}}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "PUT", "/api/accounts/acc_1/proxy",
		`{"proxy":"http://user:secret@127.0.0.1:8080","imap_proxy":"socks5://user:secret@127.0.0.1:1080"}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, body)
	}
	for _, leak := range []string{"secret", "user:", "127.0.0.1", "socks5://", "http://"} {
		if strings.Contains(body, leak) {
			t.Fatalf("响应体不得包含 %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"has_imap_proxy":true`) {
		t.Fatalf("应只暴露布尔 has_imap_proxy: %s", body)
	}
}

// TestUpdateProxyHandlerEmptyBodyRejected 验证两个字段都缺失时返回 400, 而不是
// 静默地把两个代理都清掉。
func TestUpdateProxyHandlerEmptyBodyRejected(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号"}}}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "PUT", "/api/accounts/acc_1/proxy", `{}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("空请求体期望 400, 得到 %d: %s", status, body)
	}
}

// TestUpdateProxyHandlerExplicitEmptyClears 验证显式空串表示清除, 会原样转发
// 指针(非 nil), 与「未提供」区分开。
func TestUpdateProxyHandlerExplicitEmptyClears(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号"}}}
	_, ts := newTestServer(t, f)

	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "PUT", "/api/accounts/acc_1/proxy", `{"imap_proxy":""}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, body)
	}
	if f.imapProxyVal == nil || *f.imapProxyVal != "" {
		t.Fatalf("显式空串应转发为清除, 实际 %v", f.imapProxyVal)
	}
	if f.proxyValue != nil {
		t.Fatalf("未提供的 proxy 不应被改动, 实际 %v", *f.proxyValue)
	}
}
