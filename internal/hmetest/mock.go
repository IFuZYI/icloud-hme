// Package hmetest 提供 iCloud 认证端点的进程内 mock,供 hme/account/server 各层测试
// 复用,避免测试访问真实 idmsa/setup 域名。
//
// 不导入 hme 包:调用方自行通过 hme.OverrideEndpointsForTest 指向本 mock。
package hmetest

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Server 是 mock idmsa + setup 的组合服务。
type Server struct {
	URL string

	// RequireOTP 为 true 时 signin/complete 返回 409,必须先提交验证码。
	RequireOTP bool
	// ExpectedCode 是 securitycode 端点的正确验证码。
	ExpectedCode string
	// InitC 是 signin/init 下发的 SRP 会话标识。
	InitC string

	mu sync.Mutex

	CompleteBody    map[string]any
	CompleteHeaders http.Header
	VerifyCode      string
	VerifyHeaders   http.Header
	TrustHits       int
	AccountLoginHit int
	ValidateHits    int
	ResendMethod    string
	SMSSendBody     map[string]any
	SMSVerifyBody   map[string]any
	PhoneListHits   int
}

// New 启动 mock 服务,测试结束自动关闭。
func New(t testing.TB) *Server {
	t.Helper()
	m := &Server{RequireOTP: false, ExpectedCode: "123456", InitC: "srp-c-token-1"}

	mux := http.NewServeMux()

	mux.HandleFunc("/appleauth/auth/authorize/signin", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Apple-Auth-Attributes", "auth-attr-1")
		w.Header().Set("scnt", "scnt-start")
		w.Header().Set("X-Apple-ID-Session-Id", "session-start")
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/appleauth/auth/federate", func(w http.ResponseWriter, r *http.Request) {
		// 起始响应下发的 scnt/session 必须已经带到后续请求。
		if r.Header.Get("scnt") != "scnt-start" || r.Header.Get("X-Apple-ID-Session-Id") != "session-start" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessage":"missing initial session headers"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/appleauth/auth/signin/init", func(w http.ResponseWriter, r *http.Request) {
		for _, h := range []string{"X-Apple-OAuth-Client-Id", "X-Apple-Frame-Id", "X-Apple-I-FD-Client-Info", "X-Apple-Auth-Attributes"} {
			if r.Header.Get(h) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errorCode":-20101,"errorMessage":"missing ` + h + `"}`))
				return
			}
		}
		// 响应轮换 scnt/sessionId,后续 complete 必须用新值。
		w.Header().Set("scnt", "scnt-init")
		w.Header().Set("X-Apple-ID-Session-Id", "session-init")
		w.Header().Set("Content-Type", "application/json")
		salt := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
		b := make([]byte, 256)
		for i := range b {
			b[i] = 0xAB
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iteration": 3,
			"salt":      salt,
			"protocol":  "s2k",
			"b":         base64.StdEncoding.EncodeToString(b),
			"c":         m.InitC,
		})
	})

	mux.HandleFunc("/appleauth/auth/signin/complete", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		m.mu.Lock()
		m.CompleteBody = payload
		m.CompleteHeaders = r.Header.Clone()
		requireOTP := m.RequireOTP
		m.mu.Unlock()

		// c 必须是 init 下发的 SRP 会话标识(旧实现误传 OAuth client id)。
		if got, _ := payload["c"].(string); got != m.InitC {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorCode":-20101,"errorMessage":"wrong srp session"}`))
			return
		}
		for _, h := range []string{"X-Apple-OAuth-Client-Id", "X-Apple-OAuth-Redirect-URI", "X-Apple-OAuth-Response-Mode",
			"X-Apple-OAuth-Response-Type", "X-Apple-OAuth-State", "X-Apple-Widget-Key", "X-Apple-Frame-Id",
			"X-Apple-Domain-Id", "X-Apple-I-FD-Client-Info"} {
			if r.Header.Get(h) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errorCode":-20101,"errorMessage":"missing ` + h + `"}`))
				return
			}
		}
		if r.Header.Get("scnt") != "scnt-init" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errorMessage":"stale scnt"}`))
			return
		}
		if requireOTP {
			w.Header().Set("X-Apple-ID-Session-Id", "session-2fa")
			w.Header().Set("scnt", "scnt-2fa")
			w.Header().Set("X-Apple-Session-Token", "session-token-2fa")
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/appleauth/auth/verify/trusteddevice/securitycode", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		code := ""
		if sc, ok := payload["securityCode"].(map[string]any); ok {
			code, _ = sc["code"].(string)
		}
		m.mu.Lock()
		m.VerifyHeaders = r.Header.Clone()
		m.VerifyCode = code
		expected := m.ExpectedCode
		m.mu.Unlock()
		if r.Header.Get("scnt") != "scnt-2fa" || r.Header.Get("X-Apple-Session-Token") != "session-token-2fa" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if code != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("scnt", "scnt-after-otp")
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/appleauth/auth/verify/trusteddevice", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.ResendMethod = r.Method
		m.mu.Unlock()
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/appleauth/auth/2sv/trust", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.TrustHits++
		m.mu.Unlock()
		w.Header().Set("X-Apple-Session-Token", "session-token-final")
		w.Header().Set("X-Apple-TwoSV-Trust-Token", "trust-token-final")
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/appleauth/auth", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.PhoneListHits++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":2,"numberWithDialCode":"+86 138****1234"}]}}`))
	})

	mux.HandleFunc("/appleauth/auth/verify/phone", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		m.mu.Lock()
		m.SMSSendBody = payload
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/appleauth/auth/verify/phone/securitycode", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		m.mu.Lock()
		m.SMSVerifyBody = payload
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/setup/ws/1/accountLogin", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.AccountLoginHit++
		m.mu.Unlock()
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "v=1:t=mocktoken", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "X-APPLE-WEBAUTH-USER", Value: `v=1:s=1:d=12345`, Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dsInfo":{"dsid":"12345"}}`))
	})

	mux.HandleFunc("/setup/ws/1/validate", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.ValidateHits++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webservices":{"premiummailsettings":{"url":"https://p01-maildomainws.icloud.com/v2/hme"}},"dsInfo":{"dsid":"12345","appleId":"owner@example.com","primaryEmail":"owner@example.com"}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m.URL = srv.URL
	return m
}

// Snapshot 返回锁保护下的关键状态副本。
func (m *Server) Snapshot() (trustHits, accountLoginHits, validateHits int, verifyCode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.TrustHits, m.AccountLoginHit, m.ValidateHits, m.VerifyCode
}
