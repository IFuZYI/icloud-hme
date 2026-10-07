package hme

import (
	"net/http"
	"net/url"
	"testing"
	"time"

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

func TestResendOTPUsesPutSecurityCodeEndpoint(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.ResendOTP(); err != nil {
		t.Fatalf("ResendOTP: %v", err)
	}
	// 2026 年后 Apple 要求显式 PUT /verify/trusteddevice/securitycode (无请求体)
	// 才会推送验证码; 旧的 GET/POST verify/trusteddevice 组合已失效。
	if m.PushTriggerMethod != http.MethodPut {
		t.Fatalf("触发推送的方法 = %q, 期望 PUT", m.PushTriggerMethod)
	}
	if m.PushTriggerHits != 1 {
		t.Fatalf("触发推送命中 = %d, 期望恰好 1 次", m.PushTriggerHits)
	}
	if m.ResendHits != 0 {
		t.Fatalf("不应再探测旧端点 verify/trusteddevice, 命中 %d 次", m.ResendHits)
	}
}

// 2026 年起 idmsa 对「已接受」的验证码可能返回 409 并同时下发
// X-Apple-Session-Token(rclone #9488)。必须把 409+token 视为成功,
// 否则用户收到正确验证码却无法完成登录。
func TestCompleteOTPAccepts409WithSessionToken(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.ConflictOnSubmit = true
	m.ConflictWithToken = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.CompleteOTP("123456"); err != nil {
		t.Fatalf("409 + session token 应视为成功, got: %v", err)
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("登录成功后应提取到 Cookie, got %v", c.Cookies)
	}
}

// 409 但未下发 session token 时仍是失败(不能盲目接受所有 409)。
func TestCompleteOTPRejects409WithoutToken(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.ConflictOnSubmit = true
	// ConflictWithToken 保持 false: 409 无 token, 表示被拒。
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.CompleteOTP("123456"); err == nil {
		t.Fatal("409 无 session token 不应成功")
	}
}

// 错误验证码(401)仍是失败。
func TestCompleteOTPRejectsWrongCode(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.CompleteOTP("000000"); err == nil {
		t.Fatal("错误验证码不应成功")
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

// TestTrustedPhonesParsesAllPaths 钉住 4 条解析路径。
// 2026 年起 Apple 把 trustedPhoneNumbers 移到了
// twoSV.bridgeInitiateData.phoneNumberVerification (icloudpd #1325);
// 旧的两条路径必须继续兼容, 否则老账号或区域差异会让短信通道拿不到号码。
func TestTrustedPhonesParsesAllPaths(t *testing.T) {
	const phones = `[{"id":7,"numberWithDialCode":"+86 139****0007"}]`
	cases := []struct {
		name string
		body string
	}{
		{"路径1-顶层 phoneNumberVerification", `{"phoneNumberVerification":{"trustedPhoneNumbers":` + phones + `}}`},
		{"路径2-twoSV.bridgeInitiateData", `{"twoSV":{"bridgeInitiateData":{"phoneNumberVerification":{"trustedPhoneNumbers":` + phones + `}}}}`},
		{"路径3-twoSV.phoneNumberVerification", `{"twoSV":{"phoneNumberVerification":{"trustedPhoneNumbers":` + phones + `}}}`},
		{"路径4-顶层平铺", `{"trustedPhoneNumbers":` + phones + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := hmetest.New(t)
			m.RequireOTP = true
			m.AuthStateBody = tc.body
			c := newMockedClient(t, m, "icloud.com")
			if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
				t.Fatalf("BeginLogin err = %v", err)
			}
			got, err := c.TrustedPhones()
			if err != nil {
				t.Fatalf("TrustedPhones: %v", err)
			}
			if len(got) != 1 || got[0].ID != 7 {
				t.Fatalf("phones = %+v, 期望解析出 id=7", got)
			}
		})
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

// ====================================================================
// 验证码投递路由 (参考 any-auto-register 的 prepare_verification)
//
// 账号没有受信任设备 (noTrustedDevices=true) 时, 推送验证码无处可去,
// 必须自动改走短信; 有受信任设备时才推送。否则用户会陷入
// 「登录成功但永远收不到验证码」。
// ====================================================================

// 有受信任设备(默认) → PrepareDelivery 推送, 且恰好命中一次推送端点。
func TestDeliveryRoutesPushWhenTrustedDeviceExists(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery()
	if err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if delivery != DeliveryPush || c.Delivery() != DeliveryPush {
		t.Fatalf("delivery = %q / %q, want %q", delivery, c.Delivery(), DeliveryPush)
	}
	if m.PushTriggerHits != 1 {
		t.Fatalf("有受信任设备时应推送一次, got %d", m.PushTriggerHits)
	}
	if m.SMSSendHits != 0 {
		t.Fatalf("有受信任设备时不应自动发短信, got %d", m.SMSSendHits)
	}
}

// 无受信任设备 + 1 个手机号 → 自动改走短信, 不推送。
func TestDeliveryRoutesSMSWhenNoTrustedDevices(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	// 显式设置 pushMode: 断言它透传到短信请求体(不再是死字段)。
	m.PhonePushMode = "sms"
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery()
	if err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if delivery != DeliverySMS || c.Delivery() != DeliverySMS {
		t.Fatalf("delivery = %q / %q, want %q", delivery, c.Delivery(), DeliverySMS)
	}
	// 投递准备必须读取 /appleauth/auth 的 noTrustedDevices 标志。
	if m.AuthStateHits != 1 {
		t.Fatalf("/appleauth/auth 应命中一次, got %d", m.AuthStateHits)
	}
	if m.PushTriggerHits != 0 {
		t.Fatalf("无受信任设备时不应推送(推送无处可去), got %d 次", m.PushTriggerHits)
	}
	if m.SMSSendHits != 1 {
		t.Fatalf("无受信任设备时应自动发短信一次, got %d", m.SMSSendHits)
	}
	// 短信发送的 mode 必须取自该手机号的 pushMode 字段(默认 sms)。
	if m.SMSSendBody == nil {
		t.Fatal("短信请求体未记录")
	}
	phone, ok := m.SMSSendBody["phoneNumber"].(map[string]any)
	if !ok {
		t.Fatalf("短信请求体缺少 phoneNumber 对象: %v", m.SMSSendBody)
	}
	id, ok := phone["id"].(float64)
	if !ok || int(id) != 2 {
		t.Fatalf("短信应发往手机号 id=2, got %v", m.SMSSendBody)
	}
	if mode, _ := m.SMSSendBody["mode"].(string); mode != "sms" {
		t.Fatalf("短信 mode = %q, want sms", mode)
	}
}

// 无受信任设备 + 多个手机号 → 需用户选择 (sms_selection_required), 不自动发送。
func TestDeliverySelectionRequiredWithMultiplePhones(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	m.AuthStateBody = `{"noTrustedDevices":true,"phoneNumberVerification":{"trustedPhoneNumbers":[` +
		`{"id":2,"numberWithDialCode":"+86 138****1234","pushMode":"sms"},` +
		`{"id":3,"numberWithDialCode":"+86 139****5678","pushMode":"sms"}]}}`
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery()
	if err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if delivery != DeliverySMSSelect || c.Delivery() != DeliverySMSSelect {
		t.Fatalf("delivery = %q / %q, want %q", delivery, c.Delivery(), DeliverySMSSelect)
	}
	if m.SMSSendHits != 0 || m.PushTriggerHits != 0 {
		t.Fatalf("多手机号时不应自动发送, sms=%d push=%d", m.SMSSendHits, m.PushTriggerHits)
	}
	phones, err := c.TrustedPhones()
	if err != nil || len(phones) != 2 {
		t.Fatalf("应能列出 2 个手机号: %v / %+v", err, phones)
	}
	// 用户选定手机号后走显式短信。
	if err := c.SendSMS(3); err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if c.Delivery() != DeliverySMS || m.SMSSendHits != 1 {
		t.Fatalf("选定手机号后应走短信: delivery=%q sms=%d", c.Delivery(), m.SMSSendHits)
	}
}

// 无受信任设备且无手机号 → 明确报错, 而不是静默等一个永远收不到的验证码。
func TestDeliveryFailsWhenNoPhoneAtAll(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	m.AuthStateBody = `{"noTrustedDevices":true,"phoneNumberVerification":{"trustedPhoneNumbers":[]}}`
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if _, err := c.PrepareDelivery(); err == nil {
		t.Fatal("无设备无手机号时应明确报错")
	}
}

// 选定短信后重发应重发短信(而非推送); 多手机号待选时重发应被拒绝。
func TestResendFollowsSelectedDelivery(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	c := newMockedClient(t, m, "icloud.com")
	now := time.Unix(1000000, 0)
	c.now = func() time.Time { return now }
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if _, err := c.PrepareDelivery(); err != nil { // 自动发短信
		t.Fatalf("PrepareDelivery: %v", err)
	}
	now = now.Add(31 * time.Second)
	if err := c.ResendOTP(); err != nil {
		t.Fatalf("重发: %v", err)
	}
	if m.SMSSendHits != 2 {
		t.Fatalf("重发应走短信(共 2 次), got %d", m.SMSSendHits)
	}
	if m.PushTriggerHits != 0 {
		t.Fatalf("短信通道下不应推送, got %d", m.PushTriggerHits)
	}
}

// 多手机号待选时, 重发应先要求选择手机号(不能盲发到未知号码)。
func TestResendRequiresSelectionWhenMultiplePhones(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	m.AuthStateBody = `{"noTrustedDevices":true,"phoneNumberVerification":{"trustedPhoneNumbers":[` +
		`{"id":2,"numberWithDialCode":"+86 138****1234","pushMode":"sms"},` +
		`{"id":3,"numberWithDialCode":"+86 139****5678","pushMode":"sms"}]}}`
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if _, err := c.PrepareDelivery(); err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if err := c.ResendOTP(); err == nil {
		t.Fatal("多手机号待选时重发应被拒绝")
	}
	if m.SMSSendHits != 0 || m.PushTriggerHits != 0 {
		t.Fatalf("不应发出任何投递, sms=%d push=%d", m.SMSSendHits, m.PushTriggerHits)
	}
}

// 投递限流: 同一会话内 30 秒冷却 + 最多 5 次, 防止触发 Apple 节流。
func TestDeliveryRateLimitEnforced(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	c := newMockedClient(t, m, "icloud.com")
	now := time.Unix(1000000, 0)
	c.now = func() time.Time { return now }
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if _, err := c.PrepareDelivery(); err != nil { // 第 1 次
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if err := c.ResendOTP(); err == nil {
		t.Fatal("冷却期内的重发应被拒绝")
	}
	now = now.Add(31 * time.Second)
	if err := c.ResendOTP(); err != nil { // 第 2 次
		t.Fatalf("冷却后的重发应成功: %v", err)
	}
	for i := 0; i < 3; i++ { // 第 3-5 次
		now = now.Add(31 * time.Second)
		if err := c.ResendOTP(); err != nil {
			t.Fatalf("第 %d 次重发应成功: %v", i+3, err)
		}
	}
	now = now.Add(31 * time.Second)
	if err := c.ResendOTP(); err == nil {
		t.Fatal("超过 5 次投递应被拒绝")
	}
	if m.PushTriggerHits != 5 {
		t.Fatalf("实际推送 %d 次, 期望恰好 5 次", m.PushTriggerHits)
	}
}

// 自动短信投递必须贯通验证码提交端点: PrepareDelivery 自动发短信后,
// CompleteOTP 提交的验证码必须到达 /verify/phone/securitycode, 而不是
// trusteddevice(设备侧没有这个验证码, 用户表现为「收到短信却验证不过」)。
// 反变异: CompleteOTP 不按 state.delivery 路由时此测试失败(mock 的
// device 侧 VerifyCode 会记录验证码, 而 phone 侧请求体为空)。
func TestAutoSMSCodeSubmitsToPhoneEndpoint(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery() // 无受信任设备 + 单手机号 → 自动短信
	if err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if delivery != DeliverySMS {
		t.Fatalf("delivery = %q, want %q", delivery, DeliverySMS)
	}
	if err := c.CompleteOTP("123456"); err != nil {
		t.Fatalf("CompleteOTP: %v", err)
	}
	// 验证码必须落在 phone 端点: mock 的 phone 侧请求体记录到 SMSVerifyBody。
	if m.SMSVerifyBody == nil {
		t.Fatal("验证码未提交到 /verify/phone/securitycode(自动短信会话端点错配)")
	}
	if code, _ := m.SMSVerifyBody["securityCode"].(map[string]any)["code"].(string); code != "123456" {
		t.Fatalf("phone 端点收到的验证码 = %q, want 123456", code)
	}
	if m.VerifyCode != "" {
		t.Fatalf("验证码不应提交到 device 端点, got %q", m.VerifyCode)
	}
	// phone 请求体必须携带选定号码与 mode, 供 Apple 定位校验目标。
	if phone, ok := m.SMSVerifyBody["phoneNumber"].(map[string]any); !ok || int(phone["id"].(float64)) != 2 {
		t.Fatalf("phone 端点应携带号码 id=2, got %v", m.SMSVerifyBody)
	}
	if mode, _ := m.SMSVerifyBody["mode"].(string); mode != "sms" {
		t.Fatalf("phone 端点 mode = %q, want sms", mode)
	}
}

// Login 单函数入口在多手机号待选时必须报错(不能盲发到未知号码)。
func TestLoginSingleShotRejectsSelectionRequired(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	m.AuthStateBody = `{"noTrustedDevices":true,"phoneNumberVerification":{"trustedPhoneNumbers":[` +
		`{"id":2,"numberWithDialCode":"+86 138****1234","pushMode":"sms"},` +
		`{"id":3,"numberWithDialCode":"+86 139****5678","pushMode":"sms"}]}}`
	c := newMockedClient(t, m, "icloud.com")
	provider := func() (string, error) { return "123456", nil }
	if err := c.Login("owner@example.com", "p@ssw0rd", provider); err == nil {
		t.Fatal("多手机号待选时单函数 Login 应明确报错")
	}
}

// ====================================================================
// 成功状态码兼容性: Apple 对成功响应不一定返回恰好 200
//
// 回归背景(生产日志实测): GET /appleauth/auth 返回 HTTP 201(2xx 成功),
// 旧实现只接受 ==200 → 误报「读取 Apple 双重认证状态失败」→ 502
// 「iCloud 登录失败,请稍后重试」, 用户完全无法登录。
// 参考实现(any-auto-register)对全部端点使用 response.ok(任意 2xx)。
// ====================================================================

// 投递准备在 /appleauth/auth 返回 201 时必须成功(而非误报失败)。
func TestDeliveryAccepts201FromAuthState(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.AuthStateStatus = http.StatusCreated // 201: 生产实测的成功形态
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery()
	if err != nil {
		t.Fatalf("201 是成功状态, PrepareDelivery 不应报错: %v", err)
	}
	if delivery != DeliveryPush {
		t.Fatalf("delivery = %q, want %q", delivery, DeliveryPush)
	}
	if m.PushTriggerHits != 1 {
		t.Fatalf("应推送一次, got %d", m.PushTriggerHits)
	}
}

// 无受信任设备 + 201: 自动短信通道同样必须贯通(201 不得中断路由)。
func TestDeliveryRoutesSMSWith201AuthState(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.NoTrustedDevices = true
	m.AuthStateStatus = http.StatusCreated
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	delivery, err := c.PrepareDelivery()
	if err != nil {
		t.Fatalf("PrepareDelivery: %v", err)
	}
	if delivery != DeliverySMS || m.SMSSendHits != 1 {
		t.Fatalf("delivery=%q sms=%d, want sms/1", delivery, m.SMSSendHits)
	}
}

// 短信发送返回 201(成功)时不得误报失败; 且必须记录号码供提交使用。
func TestSendSMSAccepts201(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.SMSSendStatus = http.StatusCreated
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.SendSMS(2); err != nil {
		t.Fatalf("201 是成功状态, SendSMS 不应报错: %v", err)
	}
	if c.Delivery() != DeliverySMS {
		t.Fatalf("delivery = %q, want sms", c.Delivery())
	}
}

// 短信验证码提交返回 201(成功)时必须完成登录。
func TestCompleteSMSAccepts201(t *testing.T) {
	m := hmetest.New(t)
	m.RequireOTP = true
	m.SMSVerifyStatus = http.StatusCreated
	c := newMockedClient(t, m, "icloud.com")
	if err := c.BeginLogin("owner@example.com", "p@ssw0rd"); err != ErrOTPRequired {
		t.Fatalf("BeginLogin err = %v", err)
	}
	if err := c.SendSMS(2); err != nil {
		t.Fatalf("SendSMS: %v", err)
	}
	if err := c.CompleteSMS(2, "654321"); err != nil {
		t.Fatalf("201 是成功状态, CompleteSMS 不应报错: %v", err)
	}
	if c.Cookies["X-APPLE-WEBAUTH-TOKEN"] == "" {
		t.Fatalf("登录成功后应提取到 Cookie, got %v", c.Cookies)
	}
}
