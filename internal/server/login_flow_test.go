package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
)

// errOTPInvalidForTest 模拟 2FA 验证码错误(与 hme 内部文案一致,供 classifyLoginErr 识别)。
var errOTPInvalidForTest = errors.New("2FA 验证失败: HTTP 401")

// fakeLoginSession 是脚本化的登录会话,替代真实 hme.Client。
type fakeLoginSession struct {
	beginErr  error
	otpErr    error
	codes     []string
	resendHit bool
	smsSent   int
	phonesHit bool
}

func (f *fakeLoginSession) Begin(password string) error { return f.beginErr }

func (f *fakeLoginSession) CompleteOTP(code string) error {
	f.codes = append(f.codes, code)
	return f.otpErr
}

func (f *fakeLoginSession) CompleteSMS(phoneID int, code string) error {
	f.codes = append(f.codes, code)
	return f.otpErr
}

func (f *fakeLoginSession) ResendOTP() error {
	f.resendHit = true
	return nil
}

func (f *fakeLoginSession) TrustedPhones() ([]hme.TrustedPhone, error) {
	f.phonesHit = true
	return []hme.TrustedPhone{{ID: 2, NumberWithDialCode: "+86 138****1234"}}, nil
}

func (f *fakeLoginSession) SendSMS(phoneID int) error {
	f.smsSent++
	return nil
}

func (f *fakeLoginSession) Summary() (account.Summary, error) {
	return account.Summary{ID: "acc_1", Name: "主号", Status: "active"}, nil
}

// newLoginTestServer 构造带 fake 登录会话的测试服务。
func newLoginTestServer(t *testing.T, session *fakeLoginSession) (*Server, *httptest.Server) {
	t.Helper()
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_1", Name: "主号", Status: "active"}}}
	s, ts := newTestServer(f)
	s.newLoginSession = func(accountID string) (LoginSession, error) {
		if accountID != "acc_1" && accountID != "acc_secret" {
			return nil, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		return session, nil
	}
	return s, ts
}

func beginLogin(t *testing.T, base, session, csrf string) string {
	t.Helper()
	status, body := aliasTaskRequest(t, base, session, csrf, http.MethodPost, "/api/accounts/acc_1/login/begin", `{"password":"p@ssw0rd"}`)
	if status != http.StatusOK {
		t.Fatalf("begin = %d: %s", status, body)
	}
	var out struct {
		Data struct {
			Status    string `json:"status"`
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Status != "otp_required" {
		t.Fatalf("begin status = %q, want otp_required", out.Data.Status)
	}
	if out.Data.SessionID == "" {
		t.Fatal("begin 未返回 session_id")
	}
	return out.Data.SessionID
}

func TestLoginBeginOTPRequiredReturnsSession(t *testing.T) {
	session := &fakeLoginSession{beginErr: hme.ErrOTPRequired}
	_, ts := newLoginTestServer(t, session)
	s, csrf := login(t, ts, "admin-pass-2026-strong")
	base := ts.URL
	defer ts.Close()
	_ = beginLogin(t, base, s, csrf)
}

func TestLoginBeginDoneWithoutOTP(t *testing.T) {
	session := &fakeLoginSession{}
	_, ts := newLoginTestServer(t, session)
	defer ts.Close()
	s, csrf := login(t, ts, "admin-pass-2026-strong")
	base := ts.URL

	status, body := aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/begin", `{"password":"p@ssw0rd"}`)
	if status != http.StatusOK {
		t.Fatalf("begin = %d: %s", status, body)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.Data["status"] != "done" {
		t.Fatalf("data = %+v", out.Data)
	}
	if _, has := out.Data["cookies"]; has {
		t.Fatal("响应不得包含 cookies")
	}
}

func TestLoginOTPFlowAndWrongSession(t *testing.T) {
	session := &fakeLoginSession{beginErr: hme.ErrOTPRequired}
	_, ts := newLoginTestServer(t, session)
	defer ts.Close()
	s, csrf := login(t, ts, "admin-pass-2026-strong")
	base := ts.URL
	sid := beginLogin(t, base, s, csrf)

	status, body := aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/otp", `{"session_id":"nope","code":"123456"}`)
	if status == http.StatusOK {
		t.Fatalf("未知 session 不应成功: %s", body)
	}

	status, body = aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/otp", `{"session_id":"`+sid+`","code":"123456"}`)
	if status != http.StatusOK {
		t.Fatalf("otp = %d: %s", status, body)
	}
	if len(session.codes) != 1 || session.codes[0] != "123456" {
		t.Fatalf("验证码未送达: %+v", session.codes)
	}

	// 成功后被消费,重复提交应失败。
	status, _ = aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/otp", `{"session_id":"`+sid+`","code":"123456"}`)
	if status == http.StatusOK {
		t.Fatal("已消费的 session 不应再次成功")
	}
}

func TestLoginOTPFailureKeepsSessionForRetry(t *testing.T) {
	session := &fakeLoginSession{beginErr: hme.ErrOTPRequired, otpErr: errOTPInvalidForTest}
	_, ts := newLoginTestServer(t, session)
	defer ts.Close()
	s, csrf := login(t, ts, "admin-pass-2026-strong")
	base := ts.URL
	sid := beginLogin(t, base, s, csrf)

	status, _ := aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/otp", `{"session_id":"`+sid+`","code":"000000"}`)
	if status == http.StatusOK {
		t.Fatal("错误验证码不应成功")
	}

	// 输错一次后应仍可重试(session 未被消费)。
	session.otpErr = nil
	status, body := aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/otp", `{"session_id":"`+sid+`","code":"123456"}`)
	if status != http.StatusOK {
		t.Fatalf("重试 = %d: %s", status, body)
	}
}

func TestLoginPhonesResendSMS(t *testing.T) {
	session := &fakeLoginSession{beginErr: hme.ErrOTPRequired}
	_, ts := newLoginTestServer(t, session)
	defer ts.Close()
	s, csrf := login(t, ts, "admin-pass-2026-strong")
	base := ts.URL
	sid := beginLogin(t, base, s, csrf)

	req, _ := http.NewRequest(http.MethodGet, base+"/api/accounts/acc_1/login/phones?session_id="+sid, nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: s})
	status, body, _ := do(t, req)
	if status != http.StatusOK || !contains(body, "138****1234") {
		t.Fatalf("phones = %d: %s", status, body)
	}

	status, body = aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/resend", `{"session_id":"`+sid+`"}`)
	if status != http.StatusOK || !session.resendHit {
		t.Fatalf("resend = %d: %s", status, body)
	}

	status, body = aliasTaskRequest(t, base, s, csrf, http.MethodPost, "/api/accounts/acc_1/login/sms", `{"session_id":"`+sid+`","phone_id":2}`)
	if status != http.StatusOK || session.smsSent != 1 {
		t.Fatalf("sms = %d: %s", status, body)
	}
}

// 旧接口保持兼容: password+otp_code 一次性提交。
func TestLegacyLoginEndpointStillWorks(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{{ID: "acc_secret", Name: "秘密账号", Status: "active"}}}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/accounts/acc_secret/login", `{"password":"p@ssw0rd-2026"}`)
	if status != http.StatusOK {
		t.Fatalf("legacy login = %d: %s", status, body)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
