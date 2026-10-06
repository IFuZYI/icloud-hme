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
	// FailValidate 为 true 时 /validate 返回 401(模拟 Cookie 失效)。
	FailValidate bool
	// ExpectedCode 是 securitycode 端点的正确验证码。
	ExpectedCode string
	// InitC 是 signin/init 下发的 SRP 会话标识。
	InitC string

	mu sync.Mutex

	CompleteBody    map[string]any
	VerifyCode      string
	TrustHits       int
	AccountLoginHit int
	ValidateHits    int
	ResendMethod    string
	ResendHits      int
	// PushTriggerMethod 记录触发推送所用的方法 (期望 PUT)。
	PushTriggerMethod string
	// PushTriggerHits 是触发推送端点的命中次数。
	PushTriggerHits int
	// ConflictOnSubmit 为 true 时提交正确验证码返回 409(模拟 2026 年
	// 起 idmsa 对「已接受」验证码的行为)。
	ConflictOnSubmit bool
	// ConflictWithToken 为 true 时上述 409 附带 X-Apple-Session-Token,
	// 表示 Apple 实际已接受验证码(rclone #9488)。
	ConflictWithToken bool
	// AuthStateBody 覆盖 /appleauth/auth 的响应体(测试各条手机号解析路径)。
	// 为空时用默认的顶层 phoneNumberVerification 结构。
	AuthStateBody string
	SMSSendBody   map[string]any
	SMSVerifyBody map[string]any
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

	// 同一个路径承担两种语义, 按方法区分:
	//   PUT  (无请求体) → 触发向受信任设备推送验证码 (2026+ 流程)
	//   POST (带 code)  → 提交验证码校验
	mux.HandleFunc("/appleauth/auth/verify/trusteddevice/securitycode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			// 触发推送必须携带 409 下发的会话头, 否则视为未授权。
			if r.Header.Get("scnt") != "scnt-2fa" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			m.mu.Lock()
			m.PushTriggerMethod = r.Method
			m.PushTriggerHits++
			m.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		code := ""
		if sc, ok := payload["securityCode"].(map[string]any); ok {
			code, _ = sc["code"].(string)
		}
		m.mu.Lock()
		m.VerifyCode = code
		expected := m.ExpectedCode
		conflict := m.ConflictOnSubmit
		m.mu.Unlock()
		if r.Header.Get("scnt") != "scnt-2fa" || r.Header.Get("X-Apple-Session-Token") != "session-token-2fa" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if code != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// 2026 年起 idmsa 可能对「已接受」的验证码返回 409; 附带
		// X-Apple-Session-Token 才是「实际成功」的信号(rclone #9488)。
		if conflict {
			if m.ConflictWithToken {
				w.Header().Set("X-Apple-Session-Token", "session-token-post-otp")
			}
			w.Header().Set("scnt", "scnt-after-otp")
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("scnt", "scnt-after-otp")
		w.WriteHeader(http.StatusNoContent)
	})

	// 旧流程端点: 2026 年后 Apple 不再通过它推送验证码, 一律 405。
	// 保留 handler 是为了断言实现不再依赖这条路径。
	mux.HandleFunc("/appleauth/auth/verify/trusteddevice", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.ResendMethod = r.Method
		m.ResendHits++
		m.mu.Unlock()
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
		body := m.AuthStateBody
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body == "" {
			body = `{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":2,"numberWithDialCode":"+86 138****1234"}]}}`
		}
		_, _ = w.Write([]byte(body))
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
		fail := m.FailValidate
		m.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid session"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// serviceURL 指回 mock 自身(不含路径后缀, 与真实 Apple 响应同构:
		// 代码会拼 /v2/hme/list、/v1/hme/generate 等)。避免测试打到真实
		// p01-maildomainws 域名(3s+ 网络超时, 且违背「测试不访问网络」)。
		serviceURL := m.URL
		_, _ = w.Write([]byte(`{"webservices":{"premiummailsettings":{"url":"` + serviceURL + `"}},"dsInfo":{"dsid":"12345","appleId":"owner@example.com","primaryEmail":"owner@example.com"}}`))
	})

	mux.HandleFunc("/v2/hme/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"hmeEmails":[]}}`))
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
