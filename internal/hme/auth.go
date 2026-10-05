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

// webUserAgent 与 X-Apple-I-FD-Client-Info 中的 U 保持一致。
const webUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// ErrOTPRequired 表示账号启用了双重认证,需要调用 CompleteOTP 提交验证码。
var ErrOTPRequired = errors.New("账号启用了双重认证,需要提供 2FA 验证码")

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
// 必须在 BeginLogin 返回 ErrOTPRequired 之后、对同一个 *Client 调用。
func (c *Client) CompleteOTP(code string) error {
	state := c.pendingAuth
	if state == nil {
		return fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	ep := c.endpoints()
	if err := c.submitSecurityCode(state, ep, code); err != nil {
		return err
	}
	err := c.finishLogin(state, ep)
	if err == nil {
		c.pendingAuth = nil
	}
	return err
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

	req, err := http.NewRequest("GET", fmt.Sprintf(ep.startFmt, state.frameId, state.clientId, c.oauthRedirectURI()), nil)
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
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

// captureSessionHeaders 从响应中捕获 scnt / X-Apple-ID-Session-Id。
// 每次响应(包括错误响应)都可能轮换这两个值,必须总是取最新的。
func (c *Client) captureSessionHeaders(state *authState, resp *http.Response) {
	if scnt := resp.Header.Get("scnt"); scnt != "" {
		state.scnt = scnt
	}
	if sessionID := resp.Header.Get("X-Apple-ID-Session-Id"); sessionID != "" {
		state.sessionID = sessionID
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

	switch resp.StatusCode {
	case 200:
		return nil
	case 409:
		// 需要 2FA: 409 响应下发的 session token 是后续 MFA 请求的必需头部。
		state.sessionID = resp.Header.Get("X-Apple-ID-Session-Id")
		state.scnt = resp.Header.Get("scnt")
		state.authToken = resp.Header.Get("X-Apple-Session-Token")
		return ErrOTPRequired
	case 403:
		return fmt.Errorf("用户名或密码错误")
	case 412:
		return fmt.Errorf("需要先在 appleid.apple.com 同意隐私条款")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("auth complete 失败: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// TrustedPhones 获取账号的受信任手机号列表。必须在 BeginLogin 返回 ErrOTPRequired 之后调用。
func (c *Client) TrustedPhones() ([]TrustedPhone, error) {
	state := c.pendingAuth
	if state == nil {
		return nil, fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	ep := c.endpoints()
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
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("获取手机号列表失败: HTTP %d", resp.StatusCode)
	}
	// 实际响应把列表嵌套在 phoneNumberVerification 里,兼容顶层平铺的旧结构。
	var result struct {
		PhoneNumberVerification struct {
			TrustedPhoneNumbers []TrustedPhone `json:"trustedPhoneNumbers"`
		} `json:"phoneNumberVerification"`
		TrustedPhoneNumbers []TrustedPhone `json:"trustedPhoneNumbers"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析手机号列表失败: %w", err)
	}
	phones := result.PhoneNumberVerification.TrustedPhoneNumbers
	if len(phones) == 0 {
		phones = result.TrustedPhoneNumbers
	}
	return phones, nil
}

// SendSMS 向指定受信任手机号发送短信验证码 (PUT /verify/phone, 成功 200)。
func (c *Client) SendSMS(phoneID int) error {
	state := c.pendingAuth
	if state == nil {
		return fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	ep := c.endpoints()
	body, _ := json.Marshal(map[string]interface{}{
		"phoneNumber": map[string]int{"id": phoneID},
		"mode":        "sms",
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
	if resp.StatusCode != 200 {
		return fmt.Errorf("发送短信验证码失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// CompleteSMS 提交短信验证码 (POST /verify/phone/securitycode, 成功 200),完成登录。
func (c *Client) CompleteSMS(phoneID int, code string) error {
	state := c.pendingAuth
	if state == nil {
		return fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	ep := c.endpoints()
	body, _ := json.Marshal(map[string]interface{}{
		"securityCode": map[string]string{"code": code},
		"phoneNumber":  map[string]int{"id": phoneID},
		"mode":         "sms",
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
	if resp.StatusCode != 200 {
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
// 409 之后 Apple 通常会自动推送一次,本方法用于手动重发。该端点在不同账号/
// 区域的行为不一致,因此按候选组合依次探测,任一返回 2xx 即视为成功。
func (c *Client) ResendOTP() error {
	state := c.pendingAuth
	if state == nil {
		return fmt.Errorf("无待验证的登录会话,请重新发起登录")
	}
	ep := c.endpoints()
	candidates := []struct{ method, url string }{
		{"GET", ep.verifyDevice}, // 国区 HSA2 账号实测可用 (200)
		{"PUT", ep.verifyDevice}, // icloud-photos-sync 的方式 (202)
		{"POST", ep.verifyDevice},
		{"PUT", ep.verifyDevice + "/securitycode"},
		{"GET", ep.verifyDevice + "/securitycode"},
	}
	var lastStatus int
	for _, cand := range candidates {
		req, err := http.NewRequest(cand.method, cand.url, nil)
		if err != nil {
			return err
		}
		req.Header = c.updateAuthHeaders(req.Header, state)
		req.Header.Del("Content-Type") // 该系列端点要求无请求体

		resp, err := c.httpc.Do(req)
		if err != nil {
			return err
		}
		c.captureSessionHeaders(state, resp)
		lastStatus = resp.StatusCode
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
	}
	return fmt.Errorf("请求发送验证码失败: 全部组合均被拒绝 (最后 HTTP %d)", lastStatus)
}

// submitSecurityCode 提交 2FA 验证码。
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
	if resp.StatusCode != 204 {
		return fmt.Errorf("2FA 验证失败: HTTP %d", resp.StatusCode)
	}
	c.captureSessionHeaders(state, resp)
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
	if resp.StatusCode != 204 {
		return fmt.Errorf("trust 失败: HTTP %d", resp.StatusCode)
	}
	state.authToken = resp.Header.Get("X-Apple-Session-Token")
	state.trustToken = resp.Header.Get("X-Apple-TwoSV-Trust-Token")
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
	if resp.StatusCode != 200 {
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
	header.Set("X-Apple-OAuth-Redirect-URI", c.oauthRedirectURI())
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

// oauthRedirectURI 按账号区域返回 OAuth 回调 URI (国区 www.icloud.com.cn)。
func (c *Client) oauthRedirectURI() string {
	return "https://www." + c.Host
}

// fdClientInfo 生成与浏览器格式一致的 X-Apple-I-FD-Client-Info 指纹 JSON。
// F 字段是客户端生成的随机标识,Apple 不校验具体内容,但格式必须像。
func fdClientInfo() string {
	f := ".la44j1e3" + randToken(40) + "." + randToken(28) + "." + randToken(28) + "." + randToken(3)
	return fmt.Sprintf(`{"U":%q,"L":"zh-CN","Z":"GMT+08:00","V":"1.1","F":%q}`, webUserAgent, f)
}

// randToken 生成指定长度的随机标识串 (字母数字与 _ -)。
func randToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	b := make([]byte, n)
	rnd := make([]byte, n)
	if _, err := rand.Read(rnd); err != nil {
		panic(err)
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
