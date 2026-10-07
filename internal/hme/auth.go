// Package hme - iCloud 认证模块
//
// 基于 iCloud 网页端真实抓包实现完整 SRP 登录流程,支持双重认证 (2FA) 与
// 短信验证码两条 MFA 通道。相比旧实现(单函数 Login + 简化请求头),本版本修复:
//
//  1. signin/complete 的 "c" 必须回传 init 响应下发的 SRP 会话标识,而不是
//     OAuth client id(旧实现因此被 idmsa 以 -20101 拒绝);
//  2. 认证请求必须补齐浏览器实际发送的 X-Apple-OAuth-* / X-Apple-Frame-Id /
//     X-Apple-I-FD-Client-Info 等一整套头(缺失同样触发 -20101);
//  3. scnt / X-Apple-ID-Session-Id 每次响应都可能轮换,必须总是取最新值,
//     否则 MFA 提交会被拒;
//  4. 409 之后必须先提交 securitycode 才能走 2sv/trust 与 accountLogin;
//  5. accountLogin 按账号区域选择 setup.icloud.com(.cn),国区硬编码全球端点会失败;
//  6. 两段式 API(BeginLogin/CompleteOTP)让 2FA 状态保存在同一个 Client 上,
//     不依赖调用方重新登录。
package hme

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/pbkdf2"

	http "github.com/bogdanfinn/fhttp"
	"icloud-hme/internal/srp"
)

// AuthEndpoints iCloud 认证 API 端点(默认值;测试可整体覆盖)。
const (
	OAuthClientID = "d39ba9916b7251055b22c7f910e2ea796ee65e98b2ddecea8f5dde8d9d1a815d"

	idmsaBase = "https://idmsa.apple.com"

	authStartFmt = idmsaBase + "/appleauth/auth/authorize/signin?frame_id=auth-%[1]s&language=en_US&skVersion=7&iframeId=auth-%[1]s&client_id=%[2]s&redirect_uri=%[3]s&response_type=code&response_mode=web_message&state=auth-%[1]s&authVersion=latest"

	authFederate     = idmsaBase + "/appleauth/auth/federate?isRememberMeEnabled=true"
	authInit         = idmsaBase + "/appleauth/auth/signin/init"
	authComplete     = idmsaBase + "/appleauth/auth/signin/complete?isRememberMeEnabled=true"
	authVerifyDevice = idmsaBase + "/appleauth/auth/verify/trusteddevice"
	submitSecurity   = idmsaBase + "/appleauth/auth/verify/%s/securitycode"
	authTrust        = idmsaBase + "/appleauth/auth/2sv/trust"
	authInfo         = idmsaBase + "/appleauth/auth"
	authVerifyPhone  = idmsaBase + "/appleauth/auth/verify/phone"
	authPhoneCode    = idmsaBase + "/appleauth/auth/verify/phone/securitycode"
)

// webUserAgent 是登录与 Web API 共用的浏览器标识, 与 X-Apple-I-FD-Client-Info
// 的 U 字段、sec-ch-ua 头保持一致(版本漂移会让同一客户端对不同 Apple 端点
// 声称不同浏览器版本, 风控行为不可预测)。
const webUserAgent = defaultUserAgent

// ErrOTPRequired 表示账号启用了双重认证,需要调用 CompleteOTP 提交验证码。
var ErrOTPRequired = errors.New("账号启用了双重认证,需要提供 2FA 验证码")

// 验证码投递方式。与 any-auto-register 的 delivery 语义对齐:
//   - trusted_devices: 推送到受信任设备(账号有受信任设备时的默认路径);
//   - sms: 短信验证码已自动发出(无受信任设备且只有一个手机号);
//   - sms_selection_required: 无受信任设备且有多个手机号, 需用户选择接收号码。
const (
	DeliveryPush      = "trusted_devices"
	DeliverySMS       = "sms"
	DeliverySMSSelect = "sms_selection_required"
)

// 投递限流: 同一登录会话内防止触发 Apple 侧节流。
const (
	maxDeliveries    = 5
	deliveryCooldown = 30 * time.Second
)

// errDeliveryRateLimited 表示投递次数/频率超出限制。
var errDeliveryRateLimited = errors.New("验证码发送次数过多，请稍后重试")

// authEndpoints 是可注入的端点集合,默认指向 idmsa/setup 生产域名。
// 测试通过 OverrideEndpointsForTest 整体替换,避免真实网络访问。
type authEndpoints struct {
	startFmt     string
	federate     string
	init         string
	complete     string
	verifyDevice string
	submitSecFmt string
	trust        string
	info         string
	verifyPhone  string
	phoneCode    string
	setupBase    string // 不含 /accountLogin 与 /validate 后缀
}

func defaultAuthEndpoints() authEndpoints {
	return authEndpoints{
		startFmt:     authStartFmt,
		federate:     authFederate,
		init:         authInit,
		complete:     authComplete,
		verifyDevice: authVerifyDevice,
		submitSecFmt: submitSecurity,
		trust:        authTrust,
		info:         authInfo,
		verifyPhone:  authVerifyPhone,
		phoneCode:    authPhoneCode,
	}
}

// OverrideEndpointsForTest 把认证端点指向测试服务器,返回恢复函数。
// setupBase 形如 http://127.0.0.1:PORT/setup/ws/1,accountLogin/validate 由它派生。
func OverrideEndpointsForTest(idmsaBaseURL, setupBase string) func() {
	prev := authEndpointsOverride
	authEndpointsOverride = &authEndpoints{
		startFmt:     idmsaBaseURL + "/appleauth/auth/authorize/signin?frame_id=auth-%[1]s&language=en_US&skVersion=7&iframeId=auth-%[1]s&client_id=%[2]s&redirect_uri=%[3]s&response_type=code&response_mode=web_message&state=auth-%[1]s&authVersion=latest",
		federate:     idmsaBaseURL + "/appleauth/auth/federate?isRememberMeEnabled=true",
		init:         idmsaBaseURL + "/appleauth/auth/signin/init",
		complete:     idmsaBaseURL + "/appleauth/auth/signin/complete?isRememberMeEnabled=true",
		verifyDevice: idmsaBaseURL + "/appleauth/auth/verify/trusteddevice",
		submitSecFmt: idmsaBaseURL + "/appleauth/auth/verify/%s/securitycode",
		trust:        idmsaBaseURL + "/appleauth/auth/2sv/trust",
		info:         idmsaBaseURL + "/appleauth/auth",
		verifyPhone:  idmsaBaseURL + "/appleauth/auth/verify/phone",
		phoneCode:    idmsaBaseURL + "/appleauth/auth/verify/phone/securitycode",
		setupBase:    setupBase,
	}
	return func() { authEndpointsOverride = prev }
}

// authEndpointsOverride 非 nil 时覆盖默认端点(仅测试使用)。
var authEndpointsOverride *authEndpoints

func (c *Client) endpoints() authEndpoints {
	if authEndpointsOverride != nil {
		return *authEndpointsOverride
	}
	ep := defaultAuthEndpoints()
	ep.setupBase = c.SetupURL()
	return ep
}

// OTPProvider 双重认证回调函数,返回 2FA 验证码(旧接口,保留兼容)。
type OTPProvider func() (string, error)

// TrustedPhone 账号的受信任手机号。
type TrustedPhone struct {
	ID                 int    `json:"id"`
	NumberWithDialCode string `json:"numberWithDialCode"` // 脱敏显示,如 +86 138****1234
	// PushMode 是该号码的投递方式(Apple 下发, 默认 "sms")。
	PushMode string `json:"pushMode"`
}

// authStateInfo 是从 GET /appleauth/auth 解析出的验证码投递决策依据。
type authStateInfo struct {
	// NoTrustedDevices 表示账号没有任何受信任设备(推送无处可去)。
	NoTrustedDevices bool
	// Phones 是受信任手机号列表(可能为空)。
	Phones []TrustedPhone
}

// isSuccessStatus 判断 HTTP 状态是否为 2xx 成功。
//
// 回归背景(生产日志实测): Apple 对成功响应不一定返回恰好 200——
// GET /appleauth/auth 实测返回 201。参考实现(any-auto-register)对全部
// 端点使用 response.ok(任意 2xx); 严格 ==200 会把成功误判为失败
// (表现为「读取 Apple 双重认证状态失败: HTTP 201」→ 502 无法登录)。
func isSuccessStatus(code int) bool {
	return code >= 200 && code < 300
}

// fetchAuthState 读取 GET /appleauth/auth 并解析投递决策依据。
//
// 响应结构随 Apple 发版漂移: trustedPhoneNumbers 可能在
// phoneNumberVerification、twoSV.bridgeInitiateData.phoneNumberVerification、
// twoSV.phoneNumberVerification 或顶层(icloudpd #1325); noTrustedDevices 同理。
func (c *Client) fetchAuthState(state *authState, ep authEndpoints) (*authStateInfo, error) {
	req, err := http.NewRequest("GET", ep.info, nil)
	if err != nil {
		return nil, err
	}
	req.Header = c.updateAuthHeaders(req.Header, state)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	c.captureSessionHeaders(state, resp)
	body, _ := io.ReadAll(resp.Body)
	if !isSuccessStatus(resp.StatusCode) {
		return nil, fmt.Errorf("读取 Apple 双重认证状态失败: HTTP %d", resp.StatusCode)
	}
	info, err := parseAuthState(body)
	if err != nil {
		return nil, fmt.Errorf("解析双重认证状态失败: %w", err)
	}
	return info, nil
}

// parseAuthState 解析 /appleauth/auth 响应, 兼容全部已知嵌套路径。
//
// noTrustedDevices 可能出现在顶层, 也可能在 phoneNumberVerification 内
// (参考 any-auto-register 的 _auth_state: 无 authenticationType 时它会解包
// phoneNumberVerification 再读该字段)。两处都检查, 任一为 true 即视为无设备。
func parseAuthState(body []byte) (*authStateInfo, error) {
	type phoneVerification struct {
		TrustedPhoneNumbers []TrustedPhone `json:"trustedPhoneNumbers"`
		NoTrustedDevices    bool           `json:"noTrustedDevices"`
	}
	type authStatePayload struct {
		NoTrustedDevices        bool              `json:"noTrustedDevices"`
		PhoneNumberVerification phoneVerification `json:"phoneNumberVerification"`
		TwoSV                   struct {
			PhoneNumberVerification phoneVerification `json:"phoneNumberVerification"`
			BridgeInitiateData      struct {
				PhoneNumberVerification phoneVerification `json:"phoneNumberVerification"`
			} `json:"bridgeInitiateData"`
		} `json:"twoSV"`
		TrustedPhoneNumbers []TrustedPhone `json:"trustedPhoneNumbers"`
	}
	var result authStatePayload
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	phones := result.PhoneNumberVerification.TrustedPhoneNumbers
	if len(phones) == 0 {
		phones = result.TwoSV.BridgeInitiateData.PhoneNumberVerification.TrustedPhoneNumbers
	}
	if len(phones) == 0 {
		phones = result.TwoSV.PhoneNumberVerification.TrustedPhoneNumbers
	}
	if len(phones) == 0 {
		phones = result.TrustedPhoneNumbers
	}
	// pushMode 缺省为 sms(Apple 只在部分响应里下发该字段)。
	for i := range phones {
		if strings.TrimSpace(phones[i].PushMode) == "" {
			phones[i].PushMode = "sms"
		}
	}
	noTrustedDevices := result.NoTrustedDevices ||
		result.PhoneNumberVerification.NoTrustedDevices ||
		result.TwoSV.PhoneNumberVerification.NoTrustedDevices ||
		result.TwoSV.BridgeInitiateData.PhoneNumberVerification.NoTrustedDevices
	return &authStateInfo{NoTrustedDevices: noTrustedDevices, Phones: phones}, nil
}

// pushCode 触发向受信任设备推送验证码(含投递限流)。
func (c *Client) pushCode(state *authState, ep authEndpoints) error {
	if err := checkDeliveryLimit(state, c.nowFunc()); err != nil {
		return err
	}
	if err := c.requestPushCode(ep); err != nil {
		return err
	}
	recordDelivery(state, c.nowFunc())
	return nil
}

// requestPushCode 执行实际的推送请求。
func (c *Client) requestPushCode(ep authEndpoints) error {
	state := c.pendingAuth
	req, err := http.NewRequest("PUT", ep.verifyDevice+"/securitycode", nil)
	if err != nil {
		return err
	}
	req.Header = c.updateAuthHeaders(req.Header, state)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.captureSessionHeaders(state, resp)
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("请求发送验证码失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// sendSMSCode 向指定手机号发送短信验证码(含投递限流), 并记录选定号码。
func (c *Client) sendSMSCode(state *authState, ep authEndpoints, phoneID int, mode string) error {
	if err := checkDeliveryLimit(state, c.nowFunc()); err != nil {
		return err
	}
	if strings.TrimSpace(mode) == "" {
		mode = "sms"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"phoneNumber": map[string]int{"id": phoneID},
		"mode":        mode,
	})
	req, err := http.NewRequest("PUT", ep.verifyPhone, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.captureSessionHeaders(state, resp)
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("发送短信验证码失败: HTTP %d", resp.StatusCode)
	}
	recordDelivery(state, c.nowFunc())
	state.smsPhoneID = phoneID
	state.smsMode = mode
	return nil
}

// TrustedPhones 获取账号的受信任手机号列表。必须在 BeginLogin 返回 ErrOTPRequired 之后调用。
func (c *Client) TrustedPhones() ([]TrustedPhone, error) {
	state, err := c.pending()
	if err != nil {
		return nil, err
	}
	ep := c.endpoints()
	info, err := c.fetchAuthState(state, ep)
	if err != nil {
		return nil, err
	}
	state.phones = info.Phones
	return info.Phones, nil
}

// authState 保存认证过程中的状态。
type authState struct {
	username   string
	frameId    string
	clientId   string
	srpSession string // init 响应的 "c" 字段(SRP 会话标识),complete 必须回传
	authAttr   string
	sessionID  string
	scnt       string
	authToken  string
	trustToken string
	dsid       string
	// delivery 记录当前验证码投递方式(push/sms/sms_select)。
	delivery string
	// smsPhoneID/smsMode 是已选定的短信接收号码与发送模式。
	smsPhoneID int
	smsMode    string
	// deliveries/lastDelivery 用于投递限流(最多 5 次, 每次间隔 ≥30 秒)。
	deliveries   int
	lastDelivery time.Time
	// phones 缓存本次会话读到的受信任手机号。
	phones []TrustedPhone
}

// pendingAuth 是等待 2FA 的登录会话状态;由 BeginLogin 保存,CompleteOTP 消费。
// 每个 Client 同时只允许一个待验证会话(与浏览器单标签页行为一致)。

// Login 是单函数登录入口(保留旧签名,内部走两段式实现)。
//
// 需要 2FA 时必须提供 otpProvider,且它会同步等待验证码;交互式场景请改用
// BeginLogin/CompleteOTP,以避免在 HTTP 请求内阻塞等待。
func (c *Client) Login(username, password string, otpProvider OTPProvider) error {
	err := c.BeginLogin(username, password)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrOTPRequired) {
		return err
	}
	if otpProvider == nil {
		return fmt.Errorf("账号启用了双重认证,需要提供 OTP")
	}
	// 2026 年起 409 不再自动推送: 必须先决定投递方式(推送/短信)并触发,
	// 否则验证码永远不会到达任何设备(旧实现因此陷入无限等待)。
	delivery, err := c.PrepareDelivery()
	if err != nil {
		return fmt.Errorf("准备验证码投递失败: %w", err)
	}
	// 多手机号待选: 单函数入口无法让用户选择号码, 明确报错而不是盲发到未知号码。
	if delivery == DeliverySMSSelect {
		return fmt.Errorf("账号有多个受信任手机号,请改用两段式登录选择接收号码")
	}
	code, codeErr := otpProvider()
	if codeErr != nil {
		return fmt.Errorf("获取 2FA 验证码失败: %w", codeErr)
	}
	return c.CompleteOTP(code)
}

// BeginLogin 开始登录流程(阶段一)。
//
// 返回 nil 表示登录完成,Cookie 已就绪 (c.Cookies)。
// 返回 ErrOTPRequired 表示需要 2FA,此时必须对同一个 *Client 调用 CompleteOTP。
// 返回其他错误表示登录失败(密码错误、网络问题等)。
func (c *Client) BeginLogin(username, password string) error {
	ep := c.endpoints()
	state := &authState{username: username}

	if err := c.authStart(state, ep); err != nil {
		return fmt.Errorf("auth start: %w", err)
	}
	if err := c.authFederate(state, ep); err != nil {
		return fmt.Errorf("auth federate: %w", err)
	}

	params := srp.GetParams(2048)
	params.NoUserNameInX = true
	srpClient := srp.NewSRPClient(params, nil)

	initResp, err := c.authInit(state, ep, base64.StdEncoding.EncodeToString(srpClient.GetABytes()))
	if err != nil {
		return fmt.Errorf("auth init: %w", err)
	}

	bDec, err := base64.StdEncoding.DecodeString(initResp.B)
	if err != nil {
		return fmt.Errorf("decode B: %w", err)
	}
	saltDec, err := base64.StdEncoding.DecodeString(initResp.Salt)
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}

	passHash := sha256.Sum256([]byte(password))
	passKey := pbkdf2.Key(passHash[:], saltDec, initResp.Iteration, 32, sha256.New)
	srpClient.ProcessClientChanllenge([]byte(username), passKey, saltDec, bDec)

	m1 := base64.StdEncoding.EncodeToString(srpClient.M1)
	m2 := base64.StdEncoding.EncodeToString(srpClient.M2)
	if err := c.authComplete(state, ep, m1, m2); err != nil {
		if errors.Is(err, ErrOTPRequired) {
			c.pendingAuth = state
			return ErrOTPRequired
		}
		return fmt.Errorf("auth complete: %w", err)
	}
	return c.finishLogin(state, ep)
}

// CompleteOTP 提交 2FA 验证码(阶段二),完成登录。
//
// 必须按会话的实际投递通道路由: 短信通道(自动短信/已选号码)的验证码
// 在 /verify/phone/securitycode 校验, 提交到 trusteddevice 会静默失败
// (设备侧根本没有这个验证码, 用户表现为「收到了短信却验证不过」)。
// 必须在 BeginLogin 返回 ErrOTPRequired 之后、对同一个 *Client 调用。
func (c *Client) CompleteOTP(code string) error {
	state, err := c.pending()
	if err != nil {
		return err
	}
	switch {
	case state.delivery == DeliverySMS:
		if state.smsPhoneID <= 0 {
			return fmt.Errorf("短信会话缺少接收号码,请重新选择手机号")
		}
		return c.completeSMS(state, state.smsPhoneID, code)
	case state.delivery == DeliverySMSSelect:
		return fmt.Errorf("请先选择接收验证码的手机号")
	}
	ep := c.endpoints()
	if err := c.submitSecurityCode(state, ep, code); err != nil {
		return err
	}
	if err := c.finishLogin(state, ep); err != nil {
		return err
	}
	c.pendingAuth = nil
	return nil
}

// pending 返回进行中的登录会话; 无会话时给出统一错误。
// 五个入口(CompleteOTP/CompleteSMS/SendSMS/TrustedPhones/ResendOTP)共用。
func (c *Client) pending() (*authState, error) {
	if c.pendingAuth == nil {
		return nil, fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	return c.pendingAuth, nil
}

// nowFunc 返回可注入的时钟, 缺省 time.Now。
func (c *Client) nowFunc() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// Delivery 返回当前会话的验证码投递方式(空串表示尚未决定)。
func (c *Client) Delivery() string {
	if c.pendingAuth == nil {
		return ""
	}
	return c.pendingAuth.delivery
}

// checkDeliveryLimit 在发起一次投递前校验限流(最多 5 次, 每次间隔 ≥30 秒)。
//
// 参考 any-auto-register: 频繁请求验证码会触发 Apple 侧节流, 之后连
// 正常请求都会被拒; 本地先拦一道比被上游拦更可控。
func checkDeliveryLimit(state *authState, now time.Time) error {
	if state.deliveries >= maxDeliveries {
		return fmt.Errorf("%w: 已达到 5 次上限", errDeliveryRateLimited)
	}
	if !state.lastDelivery.IsZero() && now.Sub(state.lastDelivery) < deliveryCooldown {
		return fmt.Errorf("%w: 请等待 %d 秒", errDeliveryRateLimited, int(deliveryCooldown.Seconds()))
	}
	return nil
}

// recordDelivery 记录一次成功的投递(用于限流)。
func recordDelivery(state *authState, now time.Time) {
	state.deliveries++
	state.lastDelivery = now
}

// PrepareDelivery 决定验证码投递方式并发起投递。
//
// 参考 any-auto-register 的 prepare_verification:
//   - 账号有受信任设备 → 推送(push);
//   - 无受信任设备且只有一个手机号 → 自动改走短信(sms);
//   - 无受信任设备且有多个手机号 → 需用户选择(sms_select);
//   - 无受信任设备且无手机号 → 明确报错(推送无处可去, 等也等不到)。
//
// 返回投递方式; 必须在 BeginLogin 返回 ErrOTPRequired 之后调用。
func (c *Client) PrepareDelivery() (string, error) {
	state, err := c.pending()
	if err != nil {
		return "", err
	}
	ep := c.endpoints()
	authStateBody, err := c.fetchAuthState(state, ep)
	if err != nil {
		return "", err
	}
	noTrustedDevices := authStateBody.NoTrustedDevices
	state.phones = authStateBody.Phones
	switch {
	case noTrustedDevices && len(state.phones) == 1:
		phone := state.phones[0]
		if err := c.sendSMSCode(state, ep, phone.ID, phone.PushMode); err != nil {
			return "", err
		}
		state.delivery = DeliverySMS
	case noTrustedDevices && len(state.phones) > 1:
		state.delivery = DeliverySMSSelect
	case noTrustedDevices:
		return "", fmt.Errorf("账号没有可用的双重认证设备或手机号,无法接收验证码")
	default:
		if err := c.pushCode(state, ep); err != nil {
			return "", err
		}
		state.delivery = DeliveryPush
	}
	return state.delivery, nil
}

// finishLogin 登录收尾: 信任设备 → 获取 Web Cookie → 保存到 Client。
func (c *Client) finishLogin(state *authState, ep authEndpoints) error {
	if err := c.getTrust(state, ep); err != nil {
		return fmt.Errorf("get trust: %w", err)
	}
	if err := c.authenticateWeb(state, ep); err != nil {
		return fmt.Errorf("authenticate web: %w", err)
	}
	cookies := c.extractSessionCookies()
	c.Cookies = cookies
	c.log("登录成功,获取到 %d 个 Cookie", len(cookies))
	if len(cookies) == 0 {
		return fmt.Errorf("登录成功但未获取到会话 Cookie")
	}
	// 登录完成后立即摘掉 jar: 之后该 client 只走 request() 手动管理 Cookie,
	// 保留 jar 会导致 Cookie 头翻倍(见 NewClient 注释)。
	c.httpc.SetCookieJar(nil)
	return nil
}

// ---- 认证流程各步骤 ----

// authStart 初始化 frameId 与 clientId,并捕获服务端下发的初始会话凭证。
func (c *Client) authStart(state *authState, ep authEndpoints) error {
	state.frameId = strings.ToLower(uuid.New().String())
	state.clientId = OAuthClientID

	req, err := http.NewRequest("GET", fmt.Sprintf(ep.startFmt, state.frameId, state.clientId, c.Origin()), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", webUserAgent)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	state.authAttr = resp.Header.Get("X-Apple-Auth-Attributes")
	c.captureSessionHeaders(state, resp)
	return nil
}

// authFederate 提交用户名。
func (c *Client) authFederate(state *authState, ep authEndpoints) error {
	data := `{"accountName":"` + state.username + `","rememberMe":true}`
	req, err := http.NewRequest("POST", ep.federate, bytes.NewReader([]byte(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

// authInitResp authInit 响应。
type authInitResp struct {
	Iteration int    `json:"iteration"`
	Salt      string `json:"salt"`
	Protocol  string `json:"protocol"`
	B         string `json:"b"`
	C         string `json:"c"`
}

// authInit 初始化 SRP 认证。
func (c *Client) authInit(state *authState, ep authEndpoints, a string) (*authInitResp, error) {
	reqBody := map[string]interface{}{
		"a":           a,
		"accountName": state.username,
		"protocols":   []string{"s2k", "s2k_fo"},
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", ep.init, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 捕获服务端下发的会话凭证,后续请求必须回传,否则 complete 会被拒绝 (-20101)。
	c.captureSessionHeaders(state, resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if !isSuccessStatus(resp.StatusCode) {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result authInitResp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	// 保存 SRP 会话标识,signin/complete 的 "c" 字段必须回传它
	// (不是 OAuth client id —— 那会以 -20101 被拒)。
	state.srpSession = result.C
	return &result, nil
}

// captureSessionHeaders 从响应中捕获 scnt / X-Apple-ID-Session-Id /
// X-Apple-Session-Token。一律「非空才覆盖」——Apple 并非每个响应都带全部
// 头部, 无条件赋值会把此前捕获的值清空, 导致后续 MFA 请求缺头被拒。
// 每次响应(包括错误响应)都可能轮换这些值, 必须总是取最新的非空值。
func (c *Client) captureSessionHeaders(state *authState, resp *http.Response) {
	if scnt := resp.Header.Get("scnt"); scnt != "" {
		state.scnt = scnt
	}
	if sessionID := resp.Header.Get("X-Apple-ID-Session-Id"); sessionID != "" {
		state.sessionID = sessionID
	}
	if token := resp.Header.Get("X-Apple-Session-Token"); token != "" {
		state.authToken = token
	}
}

// authComplete 提交 SRP 响应。
//
// 需要 2FA 时保存会话凭证与 session token 并返回 ErrOTPRequired。
// 注意: 全新密码 SRP 登录不应携带 authType (浏览器抓包中 "authType":"hsa2"
// 与真实信任令牌配套出现,空 trustTokens + hsa2 会被 400)。
func (c *Client) authComplete(state *authState, ep authEndpoints, m1, m2 string) error {
	reqBody := map[string]interface{}{
		"accountName": state.username,
		"rememberMe":  true,
		"trustTokens": []string{},
		"m1":          m1,
		"c":           state.srpSession, // init 响应下发的 SRP 会话标识
		"m2":          m2,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", ep.complete, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 每次响应(包括 401/409)都可能轮换 scnt/sessionID,必须取最新值。
	c.captureSessionHeaders(state, resp)

	switch {
	case isSuccessStatus(resp.StatusCode):
		// 任意 2xx 均为成功(Apple 不保证恰好 200)。
		return nil
	case resp.StatusCode == http.StatusConflict:
		// 需要 2FA: 409 响应下发的 session token 是后续 MFA 请求的必需头部,
		// 已由上方 captureSessionHeaders 按「非空才覆盖」捕获。
		return ErrOTPRequired
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("用户名或密码错误")
	case resp.StatusCode == http.StatusPreconditionFailed:
		return fmt.Errorf("需要先在 appleid.apple.com 同意隐私条款")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("auth complete 失败: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// SendSMS 向指定受信任手机号发送短信验证码 (PUT /verify/phone, 成功 200)。
//
// 选定号码后会话的投递方式切换为短信: 后续 ResendOTP 会重发短信而非推送。
func (c *Client) SendSMS(phoneID int) error {
	state, err := c.pending()
	if err != nil {
		return err
	}
	ep := c.endpoints()
	mode := "sms"
	for _, p := range state.phones {
		if p.ID == phoneID && strings.TrimSpace(p.PushMode) != "" {
			mode = p.PushMode
			break
		}
	}
	if err := c.sendSMSCode(state, ep, phoneID, mode); err != nil {
		return err
	}
	state.delivery = DeliverySMS
	return nil
}

// CompleteSMS 提交短信验证码 (POST /verify/phone/securitycode, 成功 200),完成登录。
func (c *Client) CompleteSMS(phoneID int, code string) error {
	state, err := c.pending()
	if err != nil {
		return err
	}
	return c.completeSMS(state, phoneID, code)
}

// completeSMS 是短信验证码提交的共享实现(CompleteSMS 与 CompleteOTP 的
// 短信通道路由共用)。成功后清空会话。
func (c *Client) completeSMS(state *authState, phoneID int, code string) error {
	ep := c.endpoints()
	mode := state.smsMode
	if strings.TrimSpace(mode) == "" {
		mode = "sms"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"securityCode": map[string]string{"code": code},
		"phoneNumber":  map[string]int{"id": phoneID},
		"mode":         mode,
	})
	req, err := http.NewRequest("POST", ep.phoneCode, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.captureSessionHeaders(state, resp)
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("短信验证码校验失败: HTTP %d", resp.StatusCode)
	}
	err = c.finishLogin(state, ep)
	if err == nil {
		c.pendingAuth = nil
	}
	return err
}

// ResendOTP 请求 Apple 向受信任设备推送 2FA 验证码。
//
// 2026 年起 Apple 更改了流程: 409 之后不再自动推送, 必须显式
// PUT /appleauth/auth/verify/trusteddevice/securitycode (无请求体) 才会
// 向受信任设备下发验证码。旧实现探测的 GET/POST verify/trusteddevice
// 组合已全部失效(实测 405/500, 且不产生任何推送)。
// 成功状态为 202(部分账号返回 200/204), 任一 2xx 即视为成功。
func (c *Client) ResendOTP() error {
	state, err := c.pending()
	if err != nil {
		return err
	}
	ep := c.endpoints()
	// 已选短信通道 → 重发短信; 否则走设备推送。
	// (多手机号待选时先要求选择, 避免重发到未知号码。)
	switch state.delivery {
	case DeliverySMSSelect:
		return fmt.Errorf("请先选择接收验证码的手机号")
	case DeliverySMS:
		if state.smsPhoneID > 0 {
			return c.sendSMSCode(state, ep, state.smsPhoneID, state.smsMode)
		}
		// 短信会话却缺号码: 不能静默改推——无受信任设备的账号根本收不到推送,
		// 会重现「登录成功但永远收不到验证码」的故障。
		return fmt.Errorf("短信会话缺少接收号码,请重新选择手机号")
	}
	return c.pushCode(state, ep)
}

// submitSecurityCode 提交 2FA 验证码。
//
// 成功形态有两种:
//   - 204/200: 常规成功;
//   - 409 且响应带 X-Apple-Session-Token: 2026 年起 idmsa 对「已接受」的
//     验证码可能返回 409(响应体 securityCode.valid=true)并同时下发
//     session token——token 才是真正的地面事实(rclone #9488)。
//     若把这种 409 当失败, 用户会陷入「验证码正确却永远登录不上」。
func (c *Client) submitSecurityCode(state *authState, ep authEndpoints, code string) error {
	reqBody := map[string]interface{}{
		"securityCode": map[string]string{"code": code},
	}
	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", fmt.Sprintf(ep.submitSecFmt, "trusteddevice"), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 先捕获头: 409 成功形态同样轮换 scnt/session token, 后续 finishLogin 依赖。
	c.captureSessionHeaders(state, resp)

	if resp.StatusCode == http.StatusConflict && resp.Header.Get("X-Apple-Session-Token") != "" {
		return nil
	}
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("2FA 验证失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// getTrust 获取 trust token。
func (c *Client) getTrust(state *authState, ep authEndpoints) error {
	req, err := http.NewRequest("GET", ep.trust, nil)
	if err != nil {
		return err
	}
	req.Header = c.updateAuthHeaders(req.Header, state)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("trust 失败: HTTP %d", resp.StatusCode)
	}
	// 非空才覆盖: 204 缺头时保留此前捕获的 authToken(captureSessionHeaders 同约定)。
	if token := resp.Header.Get("X-Apple-Session-Token"); token != "" {
		state.authToken = token
	}
	if trust := resp.Header.Get("X-Apple-TwoSV-Trust-Token"); trust != "" {
		state.trustToken = trust
	}
	return nil
}

// authenticateWeb 认证 iCloud Web 服务。
//
// 国区账号 (Host=icloud.com.cn) 必须请求 setup.icloud.com.cn,
// 请求 setup.icloud.com 会被拒绝并导致登录失败。
func (c *Client) authenticateWeb(state *authState, ep authEndpoints) error {
	body := fmt.Sprintf(`{"dsWebAuthToken":"%s","accountCountryCode":"USA","extended_login":true,"trustToken":"%s"}`,
		state.authToken, state.trustToken)

	req, err := http.NewRequest("POST", ep.setupBase+"/accountLogin", bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.Origin())
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("auth web 失败: HTTP %d (host=%s)", resp.StatusCode, c.Host)
	}

	var result struct {
		DsInfo struct {
			Dsid string `json:"dsid"`
		} `json:"dsInfo"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	state.dsid = result.DsInfo.Dsid

	// 复制 idmsa.apple.com 的 Cookie 到当前账号所属域名。
	u1, _ := url.Parse(idmsaBase)
	u2, _ := url.Parse("https://" + c.Host)
	cookies := c.httpc.GetCookies(u1)
	c.httpc.SetCookies(u2, cookies)
	return nil
}

// extractSessionCookies 提取 session token Cookie。
//
// Cookie 分散在多个域下: idmsa.apple.com (认证) 与 *.icloud.com / *.icloud.com.cn
// (Web 会话,由 accountLogin 的 Set-Cookie 写入)。逐一读取并合并,
// 避免单一域读取遗漏 (.com.cn 公共后缀下 jar 域匹配容易踩坑)。
// 当前端点所在域同样纳入(生产即 setup 域;测试覆盖端点的场景依赖这一条)。
func (c *Client) extractSessionCookies() map[string]string {
	cookies := make(map[string]string)
	domains := []string{
		idmsaBase,
		"https://" + c.Host,
		"https://www." + c.Host,
		"https://setup." + c.Host,
	}
	if u, err := url.Parse(c.endpoints().setupBase); err == nil && u.Host != "" {
		origin := u.Scheme + "://" + u.Host
		known := false
		for _, d := range domains {
			if d == origin {
				known = true
				break
			}
		}
		if !known {
			domains = append(domains, origin)
		}
	}
	for _, d := range domains {
		u, err := url.Parse(d)
		if err != nil {
			continue
		}
		for _, cookie := range c.httpc.GetCookies(u) {
			if cookie.Value != "" {
				cookies[cookie.Name] = cookie.Value
			}
		}
	}
	return cookies
}

// updateAuthHeaders 按浏览器实际请求补齐认证头。
//
// 对照 icloud.com(.cn) 前端抓包,idmsa 要求一整套 X-Apple-OAuth-* / Frame-Id /
// FD-Client-Info 头,缺失时 signin/complete 会以 -20101 拒绝——与密码是否正确无关。
func (c *Client) updateAuthHeaders(header http.Header, state *authState) http.Header {
	if state.scnt != "" {
		header.Set("scnt", state.scnt)
	}
	if state.sessionID != "" {
		header.Set("X-Apple-ID-Session-Id", state.sessionID)
	}
	if state.authAttr != "" {
		header.Set("X-Apple-Auth-Attributes", state.authAttr)
	}
	// MFA 阶段 (409 之后) 的请求必须携带 session token。
	if state.authToken != "" {
		header.Set("X-Apple-Session-Token", state.authToken)
	}

	header.Set("X-Apple-OAuth-Client-Id", state.clientId)
	header.Set("X-Apple-OAuth-Client-Type", "firstPartyAuth")
	header.Set("X-Apple-OAuth-Redirect-URI", c.Origin())
	header.Set("X-Apple-OAuth-Require-Grant-Code", "true")
	header.Set("X-Apple-OAuth-Response-Mode", "web_message")
	header.Set("X-Apple-OAuth-Response-Type", "code")
	header.Set("X-Apple-OAuth-State", state.frameId)
	header.Set("X-Apple-Widget-Key", state.clientId)
	header.Set("X-Apple-Frame-Id", state.frameId)
	header.Set("X-Apple-Domain-Id", "6")
	header.Set("X-Apple-Locale", "zh_CN")
	header.Set("X-Apple-Offer-Security-Upgrade", "1")
	header.Set("X-Apple-Privacy-Consent", "true")
	header.Set("X-Apple-Privacy-Consent-Accepted", "true")
	header.Set("X-Apple-I-FD-Client-Info", fdClientInfo())

	header.Set("X-Requested-With", "XMLHttpRequest")
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	header.Set("Referer", idmsaBase+"/")
	header.Set("Origin", idmsaBase)
	header.Set("User-Agent", webUserAgent)
	return header
}

// fdClientInfo 生成与浏览器格式一致的 X-Apple-I-FD-Client-Info 指纹 JSON。
// F 字段是客户端生成的随机标识,Apple 不校验具体内容,但格式必须像。
func fdClientInfo() string {
	f := ".la44j1e3" + randToken(40) + "." + randToken(28) + "." + randToken(28) + "." + randToken(3)
	return fmt.Sprintf(`{"U":%q,"L":"zh-CN","Z":"GMT+08:00","V":"1.1","F":%q}`, webUserAgent, f)
}

// randToken 生成指定长度的随机标识串 (字母数字与 _ -), 用于设备指纹。
// 随机源失败时回退为确定性占位串(与仓库 randomHash 的约定一致):
// 指纹只影响风控评分, 不值得让整个进程 panic。
func randToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	b := make([]byte, n)
	rnd := make([]byte, n)
	if _, err := rand.Read(rnd); err != nil {
		for i := range b {
			b[i] = alphabet[i%len(alphabet)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = alphabet[int(rnd[i])%len(alphabet)]
	}
	return string(b)
}

// Validate 验证当前 Cookie 是否有效。
func (c *Client) Validate() (bool, error) {
	if len(c.Cookies) == 0 {
		return false, fmt.Errorf("无 Cookie")
	}
	if err := c.ValidateSession(); err != nil {
		return false, err
	}
	return true, nil
}
