// Package server - 可替换业务接口与 Manager 适配器。
//
// Backend 边界固定为高层业务动作,不把具体 *hme.Client 或 *mail.Client 暴露给 handler。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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
	// DateRange 是日期区间; 零值表示不限。
	DateRange mail.DateRange
	// Refresh 为 true 时绕过服务端 TTL 缓存, 强制回源(前端「查询/刷新」显式触发)。
	Refresh bool
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
	CheckAccount(string) (account.Summary, error)
	RemoveAccount(string) bool
	CreateAlias(string, string) (*hme.CreateResult, error)
	ListAliases(string) ([]hme.Alias, error)
	SetAliasActive(string, string, bool) (bool, error)
	DeleteAlias(string, string) error
	ListInbox(InboxQuery) (InboxResult, error)
	FetchPreviews(string, []string) (map[string]string, error)
	GetMessage(string, string) (*mail.FullMessage, error)
	DeleteMessage(string, string) error
	Reload() error
	// TimezoneFor 返回账号调度使用的 IANA 时区名（跟随代理出口 IP 解析并缓存）；
	// 无代理返回空串，解析失败返回错误（调用方回退本地时区）。
	TimezoneFor(string) (string, error)
}

// managerBackend 是生产 Backend,包装 *account.Manager。
type managerBackend struct {
	mgr *account.Manager
	// cache 是只读响应的 TTL 缓存(收件箱/别名列表/邮件摘要), 惰性初始化。
	// 目的: 挡掉「切页/重复点击」对上游(尤其外部收件邮箱)的重复访问,
	// 避免高频请求触发服务商风控/封禁。
	cacheMu sync.Mutex
	cache   *responseCache
}

// respCache 返回响应缓存(首次使用时惰性初始化; 测试可直接构造 managerBackend)。
func (b *managerBackend) respCache() *responseCache {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.cache == nil {
		b.cache = newResponseCache()
	}
	return b.cache
}

// 缓存有效期: 短 TTL 挡掉「切页/重复点击」的重复回源, 又不至于让用户
// 长时间看不到新邮件; 摘要(正文片段)基本不变, 可缓存更久。
// 用 var 而非 const: 测试覆盖 TTL 验证过期行为。
var (
	inboxCacheTTL   = 30 * time.Second
	aliasesCacheTTL = 30 * time.Second
	previewCacheTTL = 5 * time.Minute
)

// inboxCacheKey 生成收件箱查询的缓存键(账号+别名+分页+日期区间)。
//
// 日期区间量化到分钟: days 回退路径由 handler 用 time.Now() 生成区间, 纳秒级
// 漂移会让同一逻辑查询每次生成新键, 缓存永不命中(实测连续同参请求每次回源
// 163 各 ~1.4s)。量化到分钟后, 30s TTL 内的重复查询稳定命中; 显式 start/end
// 场景前端值本就稳定, 量化不影响正确性(TTL 上限内允许亚分钟边界的极小事后漂移)。
func inboxCacheKey(q InboxQuery) string {
	var start, end int64
	if !q.DateRange.Start.IsZero() {
		start = q.DateRange.Start.Truncate(time.Minute).Unix()
	}
	if !q.DateRange.End.IsZero() {
		end = q.DateRange.End.Truncate(time.Minute).Unix()
	}
	return fmt.Sprintf("inbox:%s:%s:%d:%d:%d:%d", q.AccountID, q.Alias, q.Limit, q.Offset, start, end)
}

// TimezoneFor 返回账号调度时区（跟随代理出口 IP）。
func (b *managerBackend) TimezoneFor(id string) (string, error) {
	return b.mgr.TimezoneFor(id)
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
	// 换绑邮箱后旧邮箱的信封/摘要缓存不再代表真实内容, 立即失效。
	b.invalidateAccountReads(id)
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
	// 登录后立即按真实别名列表刷新计数,避免账号管理页停留在 0/0。
	if aliases, listErr := client.ListAliases(); listErr == nil {
		_ = b.mgr.UpdateAliasCounts(id, aliases)
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// classifyLoginErr 把 iCloud 登录错误映射为稳定错误。
//
// 登录流程与 HME 操作不同: 登录不依赖 Cookie, 因此裸 401/403 不表示
// 「iCloud 会话失效」。各阶段语义(dogfood 实测修正):
//   - init/complete(凭据交换): 401/403 = 凭据被拒(密码错/账号不存在)
//     → INVALID_CREDENTIALS, 提示检查邮箱与密码, 而非误导用户去更新 Cookie
//   - start/federate/trust/web(流程/收尾): 4xx = 流程故障 → UPSTREAM_FAILURE
//   - validate/会话检查: 明确会话信号(session/cookie) → UPSTREAM_UNAUTHORIZED
func classifyLoginErr(err error) *BackendError {
	msg := err.Error()
	if errors.Is(err, hme.ErrOTPRequired) || strings.Contains(msg, "需要提供 OTP") || strings.Contains(msg, "需要提供 2FA") {
		return &BackendError{Status: http.StatusConflict, Code: "OTP_REQUIRED", Message: "需要提供 OTP 验证码"}
	}
	if strings.Contains(msg, "2FA 验证失败") {
		return &BackendError{Status: http.StatusUnauthorized, Code: "OTP_INVALID", Message: "OTP 验证码错误"}
	}
	// 验证码投递限流(30 秒冷却 / 5 次上限): 映射为 429, 让前端提示稍后重试。
	if strings.Contains(msg, "验证码发送次数过多") || strings.Contains(msg, "发送过于频繁") {
		return &BackendError{Status: http.StatusTooManyRequests, Code: "OTP_DELIVERY_LIMITED", Message: msg}
	}
	// 确定性投递错误(无设备/无号码/待选/缺号码): 属于用户可修正的条件,
	// 映射为 400 而不是 502——502 的「稍后重试」对这些场景是误导。
	if strings.Contains(msg, "没有可用的双重认证设备或手机号") ||
		strings.Contains(msg, "请先选择接收验证码的手机号") ||
		strings.Contains(msg, "短信会话缺少接收号码") ||
		strings.Contains(msg, "请改用两段式登录选择接收号码") {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
	}
	if strings.Contains(msg, "账号不存在") {
		return &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	if strings.Contains(msg, "未设置邮箱地址") {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
	}
	if strings.Contains(msg, "同意隐私条款") {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
	}
	if strings.Contains(msg, "用户名或密码错误") || (isCredentialStage(msg) && containsHTTPStatus(msg, "401", "403")) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "INVALID_CREDENTIALS", Message: "iCloud 邮箱或密码错误"}
	}
	if hasExplicitSessionSignal(msg) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "iCloud 登录失败,请稍后重试"}
}

// isCredentialStage 判断错误是否来自凭据交换阶段(signin init/complete)。
// 这两个阶段的 401/403 表示凭据被拒, 而不是会话问题。
func isCredentialStage(msg string) bool {
	return strings.Contains(msg, "auth init") || strings.Contains(msg, "auth complete")
}

// containsHTTPStatus 检查错误消息是否含指定 HTTP 状态码。
func containsHTTPStatus(msg string, codes ...string) bool {
	for _, code := range codes {
		if strings.Contains(msg, "HTTP "+code) || strings.Contains(msg, "status: "+code) {
			return true
		}
	}
	return false
}

// hasExplicitSessionSignal 检查错误是否携带明确的会话失效信号。
//
// 只认明确关键词而非裸状态码: 登录流程没有 Cookie, 裸 401/403 更可能是
// 凭据问题或流程故障, 提示「更新 Cookie」会把用户引向错误操作。
func hasExplicitSessionSignal(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "session") || strings.Contains(m, "cookie") ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "会话校验失败") ||
		strings.Contains(m, "421")
}

// CheckAccount 检测账号登录态是否有效(validate 探活 + 状态落库)。
func (b *managerBackend) CheckAccount(id string) (account.Summary, error) {
	sum, err := b.mgr.CheckAccount(id)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "账号不存在") {
			return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		if strings.Contains(msg, "未配置 Cookie") {
			return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "账号未配置 Cookie，无法检测登录状态"}
		}
		// 检测失败时账号状态已置 error, 返回摘要与稳定错误码。
		if hasExplicitSessionSignal(msg) || isSessionError(msg) {
			return sum, &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话已失效，请重新登录或更新 Cookie"}
		}
		return sum, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "登录态检测失败，请稍后重试"}
	}
	return sum, nil
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
	// 自动任务在任何上游提示后都必须立即暂停，因此单次创建不在
	// 客户端内部重试；下一次尝试只能由用户显式恢复任务后发起。
	result, err := client.CreateAlias(label, 1)
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		return nil, classifyUpstreamErr("创建邮箱失败", err)
	}
	// 新别名已产生: 别名列表缓存立即失效, 下一次读取拿最新列表。
	b.invalidateAliases(accountID)
	return result, nil
}

// ListAliases 列出账号的 HME 别名。
//
// 顺带按最新列表刷新账号的别名总数/活跃数:别名页在创建/删除/停用后都会重新
// 拉取本接口,因此这里回写计数即可让账号管理页的显示持续与真实状态一致。
//
// 读路径带 TTL 缓存: 反复切页/切账号不再每次都打 iCloud API;
// 别名写操作(创建/删除/停用/激活)后由对应方法失效缓存。
func (b *managerBackend) ListAliases(accountID string) ([]hme.Alias, error) {
	key := "aliases:" + accountID
	if cached, ok := b.respCache().get(key); ok {
		if aliases, ok := cached.([]hme.Alias); ok {
			return aliases, nil
		}
	}
	client, err := b.mgr.HMEClient(accountID, false)
	if err != nil {
		return nil, mapAccountErr(err)
	}
	aliases, err := client.ListAliases()
	_ = b.mgr.SaveCookies(accountID, client.Cookies)
	if err != nil {
		return nil, classifyUpstreamErr("获取别名列表失败", err)
	}
	_ = b.mgr.UpdateAliasCounts(accountID, aliases)
	b.respCache().set(key, aliases, aliasesCacheTTL)
	return aliases, nil
}

// invalidateAliases 让某账号的别名列表缓存失效(写操作后调用)。
func (b *managerBackend) invalidateAliases(accountID string) {
	b.respCache().invalidate("aliases:" + accountID)
}

// invalidateAccountReads 让某账号的收件箱与摘要缓存失效(凭据/邮箱等
// 影响读取内容的配置变更后调用)。
func (b *managerBackend) invalidateAccountReads(accountID string) {
	cache := b.respCache()
	cache.invalidate("inbox:" + accountID + ":")
	cache.invalidate("preview:" + accountID + ":")
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
	// 别名状态已变化: 列表缓存失效, 下一次读取拿最新状态。
	b.invalidateAliases(accountID)
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
	// 别名已删除: 列表缓存失效, 下一次读取拿最新列表。
	b.invalidateAliases(accountID)
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
	// 读路径先查 TTL 缓存(除非显式 refresh): 重复切页/点击不再回源上游。
	cacheKey := inboxCacheKey(q)
	if !q.Refresh {
		if cached, ok := b.respCache().get(cacheKey); ok {
			if result, ok := cached.(InboxResult); ok {
				return result, nil
			}
		}
	}
	result, err := b.listInboxUncached(q)
	if err != nil {
		return result, err
	}
	// JSON 契约: messages 永远是数组([]), 不能是 null。
	// 空结果时上游返回 nil 切片, 直接序列化成 "messages":null 会让前端
	// data.messages.filter(...) 抛 TypeError → 整页白屏(dogfood 实测)。
	if result.Messages == nil {
		result.Messages = []mail.Message{}
	}
	b.respCache().set(cacheKey, result, inboxCacheTTL)
	return result, nil
}

// listInboxUncached 是 ListInbox 的无缓存实现(实际访问 IMAP/Web API)。
func (b *managerBackend) listInboxUncached(q InboxQuery) (InboxResult, error) {
	// 优先使用 IMAP 连接池 (App Password 认证,复用长连接);
	// 整体加超时: 卡住的 IMAP 不该让请求无限等待。
	// 信封阶段单封 ~500B, 预算按信封量级收紧(每封 0.3s, 仍保底 15s)。
	var imapMessages []mail.Message
	imapTotal := 0
	poolErr := withIMAPTimeout(imapEnvelopeBudget(q.Limit), func() error {
		return b.mgr.WithMailClient(q.AccountID, func(mc *mail.Client) error {
			var e error
			if q.Alias != "" {
				imapMessages, imapTotal, e = mc.FindByRecipientRange(q.Alias, q.Limit, q.Offset, q.DateRange)
			} else {
				imapMessages, imapTotal, e = mc.ListInboxPageRange(q.Limit, q.Offset, q.DateRange)
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
		slog.Error("读取邮件失败: IMAP 与 Web API 均失败",
			"account", q.AccountID, "imap_err", poolErr.Error(), "webapi_err", err.Error())
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
// ids 为列表接口返回的对外 ID(可能带文件夹前缀), 逐个解析后按文件夹批量拉取。
// 摘要内容基本不变, 按 (账号, 邮件ID) 缓存, 重复请求(如刷新后重新渐进加载)不再回源。
func (b *managerBackend) FetchPreviews(accountID string, ids []string) (map[string]string, error) {
	cache := b.respCache()
	out := map[string]string{}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		key := "preview:" + accountID + ":" + id
		if cached, ok := cache.get(key); ok {
			if preview, ok := cached.(string); ok {
				out[id] = preview
				continue
			}
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out, nil
	}

	refs := make([]mail.MessageRef, 0, len(missing))
	for _, id := range missing {
		folder, uid, err := mail.ParseMessageID(id)
		if err != nil {
			return nil, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "参数错误: 邮件 ID 无效"}
		}
		refs = append(refs, mail.MessageRef{Folder: folder, UID: uid})
	}
	previews := map[string]string{}
	err := withIMAPTimeout(imapOverallTimeout, func() error {
		return b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
			var e error
			previews, e = mc.FetchPreviewsRefs(refs)
			return e
		})
	})
	if err != nil {
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取摘要失败"}
	}
	for id, preview := range previews {
		out[id] = preview
		cache.set("preview:"+accountID+":"+id, preview, previewCacheTTL)
	}
	return out, nil
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

func (b *managerBackend) GetMessage(accountID, id string) (*mail.FullMessage, error) {
	folder, uid, perr := mail.ParseMessageID(id)
	if perr != nil {
		return nil, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "参数错误: 邮件 ID 无效"}
	}
	// 走连接池(与列表/摘要同一长连接): 反复点开验证码邮件不该每次都
	// TLS+LOGIN——对外部收件邮箱是高频异常访问信号。
	var message *mail.FullMessage
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		var e error
		message, e = mc.GetFullFrom(folder, uid)
		return e
	})
	if err != nil {
		if be := asAccountBackendError(err); be != nil {
			return nil, be
		}
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件详情失败"}
	}
	return message, nil
}

func (b *managerBackend) DeleteMessage(accountID, id string) error {
	folder, uid, perr := mail.ParseMessageID(id)
	if perr != nil {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "参数错误: 邮件 ID 无效"}
	}
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		return mc.DeleteFrom(folder, uid)
	})
	if err != nil {
		if be := asAccountBackendError(err); be != nil {
			return be
		}
		return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "删除邮件失败"}
	}
	// 邮件已删除: 该账号的收件箱缓存立即失效, 下一次读取回源(不残留已删邮件)。
	b.respCache().invalidate("inbox:" + accountID + ":")
	return nil
}

// Reload 重新加载配置。
func (b *managerBackend) Reload() error {
	if err := b.mgr.Reload(); err != nil {
		return &BackendError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "重新加载配置失败"}
	}
	// accounts.json 可能被手工修改(换绑邮箱/换凭据): 全部读缓存失效。
	b.respCache().reset()
	return nil
}

// asAccountBackendError 把账号管理器的「配置级」错误(账号不存在/缺凭据)
// 映射为稳定 BackendError; 其余(连接/协议故障)返回 nil, 由调用方按上游故障处理。
func asAccountBackendError(err error) *BackendError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "账号不存在") ||
		strings.Contains(msg, "未设置 iCloud 邮箱") ||
		strings.Contains(msg, "未设置 App 专用密码") {
		return mapAccountErr(err)
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
//
// 对客户端保持稳定的错误码与固定文案(不泄露上游细节),但把真实原因写进服务日志:
// 会话失效记 warn,其余上游故障记 error。这样线上出现 502/401 时,可从日志直接看到
// 底层是连接失败、超时、无效响应还是缺少服务端点,而无需改动对外契约。
func classifyUpstreamErr(fixedMsg string, err error) *BackendError {
	if err == nil {
		return nil
	}
	if isSessionError(err.Error()) {
		slog.Warn("上游会话失效", "action", fixedMsg, "err", capUpstreamReason(err))
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	slog.Error("上游请求失败", "action", fixedMsg, "err", capUpstreamReason(err))
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: fixedMsg}
}

// isSessionError 判断错误是否由会话失效引起。
// 421 Misdirected Request 是 iCloud 邮件侧在 Cookie 失效时的返回码,
// 必须与会话错误同类处理,否则会被误报为 502 UPSTREAM_FAILURE。
//
// 注意: 不匹配「认证」一词——「账号启用了双重认证」这类业务消息会被误判
// (dogfood 实测: OTP 场景误报为会话失效)。
func isSessionError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") || strings.Contains(m, "403") ||
		strings.Contains(m, "421") ||
		strings.Contains(m, "session") || strings.Contains(m, "cookie") ||
		strings.Contains(m, "unauthorized") ||
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
