package hme

import (
	"net/http"
	"net/url"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	"icloud-hme/internal/hmetest"
)

// newMockedClient 构造指向 mock 端点的客户端。
func newMockedClient(t *testing.T, srv *hmetest.Server, host string) *Client {
	t.Helper()
	restore := OverrideEndpointsForTest(srv.URL, srv.URL+"/setup/ws/1")
	t.Cleanup(restore)
	c, err := NewClient(nil, host, "", false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBeginLoginWithout2FACompletes(t *testing.T) {
	m := hmetest.New(t)
	c := newMockedClient(t, m, "icloud.com")

	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	if m.CompleteBody == nil {
		t.Fatal("signin/complete 未被调用")
	}
	if got, _ := m.CompleteBody["c"].(string); got != m.InitC {
		t.Fatalf("complete 请求的 c = %q, 期望 init 下发的 SRP 会话标识 %q", got, m.InitC)
	}
	if _, hits, _, _ := m.Snapshot(); hits == 0 {
		t.Fatal("accountLogin 未被调用")
	}
	if len(c.Cookies) == 0 {
		t.Fatal("登录成功后应提取到 Cookie")
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("缺少 X-APPLE-WEBAUTH-TOKEN, got %v", c.Cookies)
	}
}

func TestBeginLoginRequiresOTPThenCompleteOTP(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")

	err := c.BeginLogin("owner@example.com", "p@ssw0rd")
	if err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v, want ErrOTPRequired", err)
	}
	if err := c.CompleteOTP("123456"); err != nil {
		t.Fatalf("CompleteOTP: %v", err)
	}

	trustHits, accountLoginHits, _, verifyCode := m.Snapshot()
	if verifyCode != "123456" {
		t.Fatalf("提交的验证码 = %q", verifyCode)
	}
	if trustHits == 0 {
		t.Fatal("OTP 通过后应调用 2sv/trust")
	}
	if accountLoginHits == 0 {
		t.Fatal("OTP 通过后应调用 accountLogin")
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("OTP 登录成功后应提取到 Cookie, got %v", c.Cookies)
	}
}

func TestCompleteOTPWrongCodeKeepsPendingSession(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.CompleteOTP("000000"); err == nil {
		t.Fatal("错误验证码应报错")
	}
	// 失败后会话仍在,允许重试正确验证码(用户输错一次不该被迫重来)。
	if err := c.CompleteOTP("123456"); err != nil {
		t.Fatalf("重试正确验证码失败: %v", err)
	}
}

func TestCompleteOTPWithoutPendingLoginFails(t *testing.T) {
	m := hmetest.New(t)
	c := newMockedClient(t, m, "icloud.com")
	if err := c.CompleteOTP("123456"); err == nil {
		t.Fatal("无待验证会话时 CompleteOTP 应报错")
	}
}

func TestResendOTPUsesFirstWorkingCandidate(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.ResendOTP(); err != nil {
		t.Fatalf("ResendOTP: %v", err)
	}
	if m.ResendMethod != http.MethodGet {
		t.Fatalf("重发使用的方法 = %q, 期望先探测 GET", m.ResendMethod)
	}
}

func TestTrustedPhonesParsesNestedPayload(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	phones, err := c.TrustedPhones()
	if err != nil {
		t.Fatalf("TrustedPhones: %v", err)
	}
	if len(phones) != 1 || phones[0].ID != 2 || phones[0].NumberWithDialCode == "" {
		t.Fatalf("phones = %+v", phones)
	}
}

func TestSMSFlowCompletesLogin(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.SendSMS(2); err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if err := c.CompleteSMS(2, "654321"); err != nil {
		t.Fatalf("CompleteSMS: %v", err)
	}
	if m.SMSSendBody == nil || m.SMSVerifyBody == nil {
		t.Fatalf("SMS 请求体未记录")
	}
	if _, hits, _, _ := m.Snapshot(); hits == 0 {
		t.Fatal("短信验证通过后应调用 accountLogin")
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("短信登录成功后应提取到 Cookie, got %v", c.Cookies)
	}
}

// 国区账号必须走 setup.icloud.com.cn 的 accountLogin,而不是全球端点。
func TestSetupURLRegionSelection(t *testing.T) {
	global := &Client{Host: "icloud.com"}
	if got := global.SetupURL(); got != "https://setup.icloud.com/setup/ws/1" {
		t.Fatalf("全球区 SetupURL = %q", got)
	}
	cn := &Client{Host: "icloud.com.cn"}
	if got := cn.SetupURL(); got != "https://setup.icloud.com.cn/setup/ws/1" {
		t.Fatalf("国区 SetupURL = %q", got)
	}
}

// extractSessionCookies 必须合并多个域下发的 Cookie(idmsa 与 icloud 双域)。
func TestExtractSessionCookiesMergesDomains(t *testing.T) {
	m := hmetest.New(t)
	c := newMockedClient(t, m, "icloud.com")

	idmsaURL, _ := url.Parse(idmsaBase + "/")
	originURL, _ := url.Parse("https://www.icloud.com/")
	c.httpc.SetCookies(idmsaURL, []*fhttp.Cookie{{Name: "AUTH-ONLY-IDMSA", Value: "1", Path: "/"}})
	c.httpc.SetCookies(originURL, []*fhttp.Cookie{{Name: "WEB-ONLY-ICLOUD", Value: "2", Path: "/"}})

	cookies := c.extractSessionCookies()
	if cookies["AUTH-ONLY-IDMSA"] == "" || cookies["WEB-ONLY-ICLOUD"] == "" {
		t.Fatalf("应合并两个域的 Cookie, got %v", cookies)
	}
}

// 旧式单函数 Login 在需要 2FA 且未提供 otpProvider 时必须报错而不是静默成功。
func TestLoginWithoutOTPProviderFails(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.Login("owner@example.com", "p@ssw0rd", nil); err == nil {
		t.Fatal("需要 2FA 且无 otpProvider 时应报错")
	}
}

// 单函数 Login 提供 otpProvider 时应当完成登录(兼容旧调用方)。
func TestLoginWithOTPProviderCompletes(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	provider := func() (string, error) { return "123456", nil }
	if err := c.Login("owner@example.com", "p@ssw0rd", provider); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("应提取到 Cookie, got %v", c.Cookies)
	}
}
