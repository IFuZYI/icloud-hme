// Package server - 可替换业务接口与 Manager 适配器。
//
// Backend 边界固定为高层业务动作,不把具体 *hme.Client 或 *mail.Client 暴露给 handler。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// BackendError 是后端返回的稳定错误,携带 HTTP 状态码与稳定错误码。
type BackendError struct {
	Status  int
	Code    string
	Message string
}

func (e *BackendError) Error() string { return e.Message }

// InboxQuery 是收件箱查询参数。
type InboxQuery struct {
	AccountID string
	Alias     string
	Limit     int
	Offset    int
	Days      int
}

// InboxResult 是收件箱查询结果。
type InboxResult struct {
	AccountID string         `json:"account_id"`
	Alias     string         `json:"alias,omitempty"`
	Count     int            `json:"count"`
	Messages  []mail.Message `json:"messages"`
	Method    string         `json:"method"`
	// Total 是符合日期过滤的邮件总数(用于「加载更多」判断是否还有下一页)。
	Total int `json:"total"`
	// Offset 是本页起始偏移(新→旧), 与请求参数一致。
	Offset int `json:"offset"`
	// Warning 说明降级读取的原因(如 IMAP 不可用),为空表示一切正常。
	Warning string `json:"warning,omitempty"`
}

// Backend 是可替换的业务接口;handler 只依赖本接口,测试使用内存 fake。
type Backend interface {
	ListAccounts() []account.Summary
	AddAccount(account.AddAccountInput) (account.Summary, error)
	UpdateAccount(string, account.UpdateAccountInput) (account.Summary, error)
	UpdateProxy(string, string) (account.Summary, error)
	UpdateCookies(string, string) (account.Summary, error)
	SetAppPassword(string, string, string) (account.Summary, error)
	SetMailbox(string, account.MailboxConfig) (account.Summary, error)
	LoginAccount(string, string, string) (account.Summary, error)
	RemoveAccount(string) bool
	CreateAlias(string, string) (*hme.CreateResult, error)
	ListAliases(string) ([]hme.Alias, error)
	SetAliasActive(string, string, bool) (bool, error)
	DeleteAlias(string, string) error
	ListInbox(InboxQuery) (InboxResult, error)
	FetchPreviews(string, []uint32) (map[string]string, error)
	GetMessage(string, uint32) (*mail.FullMessage, error)
	DeleteMessage(string, uint32) error
	Reload() error
}

// managerBackend 是生产 Backend,包装 *account.Manager。
type managerBackend struct {
	mgr *account.Manager
}

// ListAccounts 返回账号安全摘要列表。
func (b *managerBackend) ListAccounts() []account.Summary {
	return b.mgr.ListSummaries()
}

// AddAccount 添加账号。
func (b *managerBackend) AddAccount(in account.AddAccountInput) (account.Summary, error) {
	sum, err := b.mgr.AddAccountWithInput(in)
	if err != nil {
		return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: err.Error()}
	}
	return sum, nil
}

// UpdateAccount 编辑账号基本信息。
func (b *managerBackend) UpdateAccount(id string, in account.UpdateAccountInput) (account.Summary, error) {
	sum, err := b.mgr.UpdateMetadata(id, in)
	if err != nil {
		return account.Summary{}, mapAccountErr(err)
	}
	return sum, nil
}

// UpdateProxy 更新或清除账号代理。
func (b *managerBackend) UpdateProxy(id, proxy string) (account.Summary, error) {
	sum, err := b.mgr.UpdateProxy(id, proxy)
	if err != nil {
		return account.Summary{}, mapAccountErr(err)
	}
	return sum, nil
}

// UpdateCookies 更新账号 Cookie。cookies 为原始文本(Header String 或 JSON)。
func (b *managerBackend) UpdateCookies(id, cookies string) (account.Summary, error) {
	parsed, err := account.ParseCookieInput(cookies)
	if err != nil {
		return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: err.Error()}
	}
	if err := b.mgr.UpdateCookies(id, parsed); err != nil {
		return account.Summary{}, mapAccountErr(err)
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// SetAppPassword 设置 iCloud 邮箱与 App 专用密码并测试 IMAP 连接。
func (b *managerBackend) SetAppPassword(id, icloudEmail, appPassword string) (account.Summary, error) {
	if err := b.mgr.SetAppPassword(id, icloudEmail, appPassword); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "账号不存在") {
			return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		if strings.Contains(msg, "不能为空") {
			return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
		}
		// IMAP 连接失败属于上游错误,不拼接详细错误
		return account.Summary{}, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "IMAP 验证失败,请检查邮箱与 App 专用密码"}
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// SetMailbox configures and verifies an external IMAP mailbox.
func (b *managerBackend) SetMailbox(id string, config account.MailboxConfig) (account.Summary, error) {
	if err := b.mgr.SetMailbox(id, config); err != nil {
		if strings.Contains(err.Error(), "账号不存在") {
			return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		return account.Summary{}, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "收件邮箱验证失败,请检查邮箱、授权码和 IMAP 配置"}
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// LoginAccount 使用 iCloud 密码登录账号,成功只返回 Summary,绝不返回 Cookies。
func (b *managerBackend) LoginAccount(id, password, otpCode string) (account.Summary, error) {
	var otpProvider hme.OTPProvider
	if otpCode != "" {
		otp := otpCode
		otpProvider = func() (string, error) { return otp, nil }
	}

	client, err := b.mgr.HMEClientWithPassword(id, password, otpProvider)
	if err != nil {
		return account.Summary{}, classifyLoginErr(err)
	}
	_ = client
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// classifyLoginErr 把 iCloud 登录错误映射为稳定错误。
func classifyLoginErr(err error) *BackendError {
	msg := err.Error()
	if strings.Contains(msg, "需要提供 OTP") {
		return &BackendError{Status: http.StatusConflict, Code: "OTP_REQUIRED", Message: "需要提供 OTP 验证码"}
	}
	if strings.Contains(msg, "2FA 验证失败") {
		return &BackendError{Status: http.StatusUnauthorized, Code: "OTP_INVALID", Message: "OTP 验证码错误"}
	}
	if strings.Contains(msg, "账号不存在") {
		return &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	if isSessionError(msg) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "iCloud 登录失败,请稍后重试"}
}

// RemoveAccount 删除账号。
func (b *managerBackend) RemoveAccount(id string) bool {
	return b.mgr.RemoveAccount(id)
}

// CreateAlias 创建 HME 别名。
func (b *managerBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	client, err := b.mgr.HMEClient(accountID, false)
	if err != nil {
		return nil, mapAccountErr(err)
	}
	result, err := client.CreateAlias(label, 5)
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		return nil, classifyUpstreamErr("创建邮箱失败", err)
	}
	return result, nil
}

// ListAliases 列出账号的 HME 别名。
func (b *managerBackend) ListAliases(accountID string) ([]hme.Alias, error) {
	client, err := b.mgr.HMEClient(accountID, false)
	if err != nil {
		return nil, mapAccountErr(err)
	}
	aliases, err := client.ListAliases()
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		return nil, classifyUpstreamErr("获取别名列表失败", err)
	}
	return aliases, nil
}

// SetAliasActive 停用或激活别名。
func (b *managerBackend) SetAliasActive(accountID, anonymousID string, active bool) (bool, error) {
	client, err := b.mgr.HMEClient(accountID, false)
	if err != nil {
		return false, mapAccountErr(err)
	}
	var success bool
	if active {
		success, err = client.ReactivateHME(anonymousID)
	} else {
		success, err = client.DeactivateHME(anonymousID)
	}
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		msg := "操作失败"
		if !active {
			msg = "停用失败"
		} else {
			msg = "激活失败"
		}
		return false, classifyUpstreamErr(msg, err)
	}
	return success, nil
}

// DeleteAlias 删除别名。
func (b *managerBackend) DeleteAlias(accountID, anonymousID string) error {
	client, err := b.mgr.HMEClient(accountID, false)
	if err != nil {
		return mapAccountErr(err)
	}
	err = client.Delete(anonymousID)
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		return classifyUpstreamErr("删除失败", err)
	}
	return nil
}

// errIMAPTimeout 表示 IMAP 路径超出总时间预算。
var errIMAPTimeout = errors.New("IMAP 读取超时")

// withIMAPTimeout 给 IMAP 读取加总时间预算, 超时返回 errIMAPTimeout。
//
// 注意: 超时只是放弃等待结果, 后台 goroutine 里的读取仍会跑完(连接池会在
// 下一次借用时探测坏连接并重建), 这里不做强杀——go-imap 连接非并发安全,
// 强杀复用中的连接反而会毒化池。
func withIMAPTimeout(d time.Duration, fn func() error) error {
	errc := make(chan error, 1)
	go func() { errc <- fn() }()
	select {
	case err := <-errc:
		return err
	case <-time.After(d):
		return fmt.Errorf("%w（%v）", errIMAPTimeout, d)
	}
}

// imapOverallTimeout 是单次收件箱请求里 IMAP 路径的总时间预算。
//
// 没有它时, 慢速/卡死的 IMAP 会把请求拖到分钟级(实测 3m30s), 浏览器早已
// 放弃。超时后按普通失败降级 Web API(秒级), 用户始终有结果可看。
const imapOverallTimeout = 30 * time.Second

// imapPerMessageBudget 是每封邮件的时间预算(基于实测: iCloud IMAP 单封
// 16KB partial fetch 约 1.3s, 留 3s 余量覆盖慢请求与解析)。
const imapPerMessageBudget = 3 * time.Second

// imapEnvelopePerMessageBudget 是信封阶段(无正文, 单封 ~500B)每封的预算。
const imapEnvelopePerMessageBudget = 300 * time.Millisecond

// imapBudget 返回正文阶段的总时间预算: 每封 3s, 下限 30s, 上限 60s。
func imapBudget(limit int) time.Duration {
	if limit <= 0 {
		return imapOverallTimeout
	}
	d := time.Duration(limit) * imapPerMessageBudget
	if d < imapOverallTimeout {
		d = imapOverallTimeout
	}
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// imapEnvelopeBudget 返回信封阶段的总时间预算: 每封 0.3s, 下限 15s, 上限 45s。
// 信封小而快, 预算比正文阶段紧, 快速失败尽快降级。
func imapEnvelopeBudget(limit int) time.Duration {
	if limit <= 0 {
		return 15 * time.Second
	}
	d := time.Duration(limit) * imapEnvelopePerMessageBudget
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	if d > 45*time.Second {
		d = 45 * time.Second
	}
	return d
}

// ListInbox 读取收件箱摘要:IMAP (App Password) 优先,Web API (Cookie) 回退。
//
// 渐进式加载: 只返回信封(Preview 空), 前端用 FetchPreviews 分批补摘要;
// Total 为符合日期过滤的总数, 前端据 offset+count<Total 判断可加载更多。
func (b *managerBackend) ListInbox(q InboxQuery) (InboxResult, error) {
	// 优先使用 IMAP 连接池 (App Password 认证,复用长连接);
	// 整体加超时: 卡住的 IMAP 不该让请求无限等待。
	// 信封阶段单封 ~500B, 预算按信封量级收紧(每封 0.3s, 仍保底 15s)。
	var imapMessages []mail.Message
	imapTotal := 0
	poolErr := withIMAPTimeout(imapEnvelopeBudget(q.Limit), func() error {
		return b.mgr.WithMailClient(q.AccountID, func(mc *mail.Client) error {
			var e error
			if q.Alias != "" {
				imapMessages, imapTotal, e = mc.FindByRecipient(q.Alias, q.Limit, q.Offset, q.Days)
			} else {
				imapMessages, imapTotal, e = mc.ListInboxPage(q.Limit, q.Offset, q.Days)
			}
			return e
		})
	})
	if poolErr == nil {
		return InboxResult{
			AccountID: q.AccountID,
			Alias:     q.Alias,
			Count:     len(imapMessages),
			Messages:  imapMessages,
			Method:    "imap",
			Total:     imapTotal,
			Offset:    q.Offset,
		}, nil
	}
	// IMAP 失败,继续尝试 Web API:降级原因随响应返回,避免用户把"读不到"误当成"没有邮件"
	degradeReason := summarizeIMAPFailure(poolErr)

	// 回退到 Web API (Cookie 认证,无需 App Password)。
	// Web API 无分页/日期过滤, 一次拉回后在内存切片分页。
	wmc, err := b.mgr.WebMailClient(q.AccountID)
	if err != nil {
		return InboxResult{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "无可用邮件客户端: 需要 App Password 或 Cookie"}
	}

	warning := "IMAP 不可用，已回退 Web API"
	var fetched []mail.Message
	if q.Alias != "" {
		warning += "（仅能按主题/发件人本地匹配，收件人信息缺失）"
		fetched, err = wmc.FindByAlias(q.Alias, 100)
	} else {
		fetched, err = wmc.ListInbox(100)
	}
	if err != nil {
		// 两条路径都失败时把上游原因写进服务日志, 便于定位(Cookie 过期/
		// 网络不通等), 响应仍保持稳定的错误码与文案。
		log.Printf("mail: IMAP 与 Web API 均失败 account=%s imap=%v webapi=%v", q.AccountID, poolErr, err)
		return InboxResult{}, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件失败"}
	}
	messages, total := slicePage(fetched, q.Offset, q.Limit)
	return InboxResult{
		AccountID: q.AccountID,
		Alias:     q.Alias,
		Count:     len(messages),
		Messages:  messages,
		Method:    "web_api",
		Total:     total,
		Offset:    q.Offset,
		Warning:   warning + "：" + degradeReason,
	}, nil
}

// slicePage 对(新→旧)列表做内存分页: 返回本页与总数。
func slicePage(list []mail.Message, offset, limit int) ([]mail.Message, int) {
	total := len(list)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []mail.Message{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return list[offset:end], total
}

// FetchPreviews 批量补齐邮件摘要(渐进式加载第二阶段)。
func (b *managerBackend) FetchPreviews(accountID string, uids []uint32) (map[string]string, error) {
	previews := map[string]string{}
	err := withIMAPTimeout(imapOverallTimeout, func() error {
		return b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
			var e error
			previews, e = mc.FetchPreviews(uids)
			return e
		})
	})
	if err != nil {
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取摘要失败"}
	}
	return previews, nil
}

// upstreamReasonLimit 与账号管理器记录上游错误时的长度约定一致(300)。
// 注意: 这里按 rune 截断(客户端可读、不劈开 UTF-8), 管理器侧是按字节存日志,
// 两者刻意不同, 不要合并语义。
const upstreamReasonLimit = 300

// summarizeIMAPFailure 把 IMAP 失败原因转成给用户看的短说明。
//
// "未配置凭据" 属于正常配置(只用 Cookie), 不应把内部错误原文抛给前端;
// 其余真实故障保留截断后的上游说明, 便于用户定位问题(该接口在登录态之后)。
func summarizeIMAPFailure(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "未设置 App 专用密码") || strings.Contains(msg, "未设置 iCloud 邮箱") {
		return "账号未配置 IMAP 凭据，改用 Cookie 读取"
	}
	return capUpstreamReason(err)
}

// capUpstreamReason 截断要回显给客户端的上游错误, 避免把服务端返回的任意长度文本
// (甚至凭据相关的细节)整段塞进响应。
func capUpstreamReason(err error) string {
	runes := []rune(err.Error())
	if len(runes) <= upstreamReasonLimit {
		return string(runes)
	}
	return string(runes[:upstreamReasonLimit]) + "…"
}

func (b *managerBackend) GetMessage(accountID string, uid uint32) (*mail.FullMessage, error) {
	mc, err := b.mgr.MailClient(accountID)
	if err != nil {
		return nil, mapAccountErr(err)
	}
	if err := mc.Connect(); err != nil {
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件失败"}
	}
	defer mc.Disconnect()
	message, err := mc.GetFull(uid)
	if err != nil {
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件详情失败"}
	}
	return message, nil
}

func (b *managerBackend) DeleteMessage(accountID string, uid uint32) error {
	mc, err := b.mgr.MailClient(accountID)
	if err != nil {
		return mapAccountErr(err)
	}
	if err := mc.Connect(); err != nil {
		return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "删除邮件失败"}
	}
	defer mc.Disconnect()
	if err := mc.Delete(uid); err != nil {
		return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "删除邮件失败"}
	}
	return nil
}

// Reload 重新加载配置。
func (b *managerBackend) Reload() error {
	if err := b.mgr.Reload(); err != nil {
		return &BackendError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "重新加载配置失败"}
	}
	return nil
}

// mapAccountErr 把账号管理器错误映射为稳定错误。
func mapAccountErr(err error) *BackendError {
	msg := err.Error()
	if strings.Contains(msg, "账号不存在") {
		return &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	if strings.Contains(msg, "Cookie") && strings.Contains(msg, "未配置") {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "账号未配置 Cookie"}
	}
	return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
}

// classifyUpstreamErr 把上游 (iCloud) 错误映射为稳定错误,不拼接上游响应体。
func classifyUpstreamErr(fixedMsg string, err error) *BackendError {
	if err == nil {
		return nil
	}
	if isSessionError(err.Error()) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: fixedMsg}
}

// isSessionError 判断错误是否由会话失效引起。
func isSessionError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") || strings.Contains(m, "403") ||
		strings.Contains(m, "session") || strings.Contains(m, "cookie") ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "认证") ||
		strings.Contains(m, "会话校验失败")
}

// asBackendError 提取 BackendError,非 BackendError 统一为 INTERNAL_ERROR。
func asBackendError(err error) *BackendError {
	var be *BackendError
	if errors.As(err, &be) {
		return be
	}
	return &BackendError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "内部错误"}
}

// cookieInputToJSON 把 handler 解析出的 map 转回 JSON 文本,交给 ParseCookieInput。
func cookieInputToJSON(cookies map[string]string) string {
	raw, err := json.Marshal(cookies)
	if err != nil {
		return ""
	}
	return string(raw)
}
