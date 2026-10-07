// Package account 实现多账号管理器。
//
// 负责账号 CRUD、Cookie 解析(Header String / JSON)、持久化到 accounts.json,
// 以及创建 HME 客户端和邮件客户端。对应原 Python 项目 account_manager.py。
package account

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"icloud-hme/internal/geo"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// Account 描述一个 iCloud 账号。
type Account struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	RealEmail     string            `json:"real_email"`
	ICloudEmail   string            `json:"icloud_email"`
	Cookies       map[string]string `json:"cookies"`
	Host          string            `json:"host"`
	Proxy         string            `json:"proxy,omitempty"` // HTTP/SOCKS5 代理
	AppPassword   string            `json:"app_password,omitempty"`
	Mailbox       *MailboxConfig    `json:"mailbox,omitempty"`
	Status        string            `json:"status"` // active / error
	AliasTotal    int               `json:"alias_total"`
	AliasActive   int               `json:"alias_active"`
	LastValidated string            `json:"last_validated"`
	LastError     string            `json:"last_error,omitempty"`
	CreatedAt     string            `json:"created_at"`
	// Timezone 为代理出口 IP 的地理时区（IANA 名称，如 America/Denver），
	// 由 TimezoneFor 解析并缓存；代理变更时清除。空串表示尚未解析或使用本地时区。
	Timezone string `json:"timezone,omitempty"`
}

// MailboxConfig describes an external mailbox used to receive forwarded mail.
type MailboxConfig struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	IMAPHost string `json:"imap_host"`
	IMAPPort int    `json:"imap_port"`
	Password string `json:"password,omitempty"`
}

// Manager 管理多个 iCloud 账号,线程安全。
type Manager struct {
	mu       sync.RWMutex
	accounts map[string]*Account
	dataDir  string
	dataFile string
	imapPool *mail.Pool // IMAP 长连接池
	// tzResolver 解析代理出口 IP 的地理时区;缺省为 geo.NewResolver()。
	// 惰性解析并按账号缓存(见 TimezoneFor)。测试可在同包内直接替换。
	tzResolver geo.Resolver
}

// cloneCookies 返回 Cookie map 的独立副本。
func cloneCookies(cookies map[string]string) map[string]string {
	if cookies == nil {
		return nil
	}
	cloned := make(map[string]string, len(cookies))
	for k, v := range cookies {
		cloned[k] = v
	}
	return cloned
}

// copyAccount 返回账号的深拷贝(含 Cookies map),必须在持锁时调用。
func copyAccount(acc *Account) *Account {
	if acc == nil {
		return nil
	}
	cp := *acc
	cp.Cookies = cloneCookies(acc.Cookies)
	return &cp
}

// NewManager 创建管理器。dataDir 用于存放 accounts.json。
func NewManager(dataDir string) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	m := &Manager{
		accounts: make(map[string]*Account),
		dataDir:  dataDir,
		dataFile: filepath.Join(dataDir, "accounts.json"),
		imapPool: mail.NewPool(),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Close 释放 IMAP 连接池等资源。
func (m *Manager) Close() {
	if m.imapPool != nil {
		m.imapPool.Close()
	}
}

// Reload 重新加载 accounts.json 配置文件。
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load()
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var wrapper struct {
		Accounts map[string]*Account `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return err
	}
	m.accounts = wrapper.Accounts
	if m.accounts == nil {
		m.accounts = make(map[string]*Account)
	}
	return nil
}

func (m *Manager) save() error {
	wrapper := struct {
		Accounts  map[string]*Account `json:"accounts"`
		UpdatedAt string              `json:"updated_at"`
	}{
		Accounts:  m.accounts,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.dataFile, raw, 0600)
}

// ParseCookieInput 解析 Cookie 输入,支持两种格式:
//   - Header String: "name1=value1; name2=value2; ..."
//   - JSON: {"name1":"value1","name2":"value2"}
//
// 空输入返回错误。
func ParseCookieInput(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("空白输入 — 请粘贴 Cookie Header String 或 JSON")
	}

	// JSON 格式
	if strings.HasPrefix(raw, "{") {
		var cookies map[string]string
		if err := json.Unmarshal([]byte(raw), &cookies); err == nil && cookies != nil {
			out := make(map[string]string, len(cookies))
			for k, v := range cookies {
				if v != "" {
					out[k] = v
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}

	// Header String 格式
	cookies := make(map[string]string)
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:idx])
		value := strings.TrimSpace(part[idx+1:])
		if name != "" {
			cookies[name] = value
		}
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("无法解析 Cookie 输入,请提供 Header String 或 JSON 格式")
	}
	return cookies, nil
}

// AddAccount 添加一个账号。cookieInput 可为空,后续可通过 /login 获取。
//
// cookieInput 支持 Header String 或 JSON。校验失败仍会保存账号(status=error),
// 方便用户后续修正 Cookie 后重新校验。
//
// 兼容入口:新调用方请使用 AddAccountWithInput。
func (m *Manager) AddAccount(name, cookieInput, host, proxy string) (*Account, error) {
	if host == "" {
		host = "icloud.com"
	}
	acc, err := m.newAccount(name, "", cookieInput, host, proxy)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.accounts[acc.ID] = acc
	saveErr := m.save()
	m.mu.Unlock()
	if saveErr != nil {
		return nil, saveErr
	}
	return acc, nil
}

// AddAccountWithInput 添加账号(带完整校验)。
//
// 无 Cookie 的添加路径不访问网络;有 Cookie 时在锁外对快照执行会话校验。
func (m *Manager) AddAccountWithInput(input AddAccountInput) (Summary, error) {
	name, err := validateName(input.Name)
	if err != nil {
		return Summary{}, err
	}
	email, err := validateEmail(input.ICloudEmail)
	if err != nil {
		return Summary{}, err
	}
	host, err := validateHost(input.Host)
	if err != nil {
		return Summary{}, err
	}
	proxy, err := validateProxy(input.Proxy)
	if err != nil {
		return Summary{}, err
	}
	acc, err := m.newAccount(name, email, input.CookieInput, host, proxy)
	if err != nil {
		return Summary{}, err
	}
	m.mu.Lock()
	m.accounts[acc.ID] = acc
	saveErr := m.save()
	m.mu.Unlock()
	if saveErr != nil {
		return Summary{}, saveErr
	}
	return acc.Summary(), nil
}

// newAccount 构造账号;cookieInput 非空时在锁外对快照执行会话校验。
func (m *Manager) newAccount(name, icloudEmail, cookieInput, host, proxy string) (*Account, error) {
	var cookies map[string]string
	if cookieInput != "" {
		var err error
		cookies, err = ParseCookieInput(cookieInput)
		if err != nil {
			return nil, err
		}
	} else {
		cookies = make(map[string]string)
	}

	acc := &Account{
		ID:          "acc_" + uuid.New().String()[:8],
		Name:        name,
		RealEmail:   icloudEmail,
		ICloudEmail: icloudEmail,
		Cookies:     cookies,
		Host:        host,
		Proxy:       proxy,
		Status:      "pending", // 无 Cookie 时为 pending
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	// 有 Cookie 才校验会话
	if len(cookies) > 0 {
		acc.validateCookies()
	}
	return acc, nil
}

// validateCookies 用 Cookie 校验会话并填充账号身份(在锁外对快照操作)。
func (a *Account) validateCookies() {
	host := a.Host
	if host == "" {
		host = "icloud.com"
	}
	client, err := hme.NewClient(a.Cookies, host, a.Proxy, false)
	if err != nil {
		a.Status = "error"
		a.LastError = truncate(err.Error(), 300)
		return
	}
	if err := client.ValidateSession(); err != nil {
		// validate 即使失败也可能通过 Set-Cookie 刷新部分会话状态。
		a.Cookies = client.Cookies
		a.Status = "error"
		a.LastError = truncate(err.Error(), 300)
		return
	}
	// 显式接收 validate 刷新的 Cookie，不依赖传入 map 的引用关系。
	a.Cookies = client.Cookies
	a.Status = "active"
	if info := client.AccountInfo(); info != nil {
		a.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
		if a.ICloudEmail == "" {
			a.ICloudEmail = deriveICloudEmail(info)
		}
	}
	if aliases, err := client.ListAliases(); err == nil {
		a.AliasTotal, a.AliasActive = countAliases(aliases)
	}
	a.LastValidated = time.Now().Format(time.RFC3339)
}

// UpdateMetadata 编辑账号基本信息(名称、iCloud 邮箱、主机),至少提供一个字段。
func (m *Manager) UpdateMetadata(id string, input UpdateAccountInput) (Summary, error) {
	if input.Name == nil && input.ICloudEmail == nil && input.Host == nil {
		return Summary{}, fmt.Errorf("至少需要提供一个可编辑字段")
	}
	var name, email, host *string
	if input.Name != nil {
		v, err := validateName(*input.Name)
		if err != nil {
			return Summary{}, err
		}
		name = &v
	}
	if input.ICloudEmail != nil {
		v, err := validateEmail(*input.ICloudEmail)
		if err != nil {
			return Summary{}, err
		}
		email = &v
	}
	if input.Host != nil {
		v, err := validateHost(*input.Host)
		if err != nil {
			return Summary{}, err
		}
		host = &v
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	if name != nil {
		acc.Name = *name
	}
	if email != nil {
		acc.ICloudEmail = *email
	}
	if host != nil {
		acc.Host = *host
	}
	if err := m.save(); err != nil {
		return Summary{}, err
	}
	return acc.Summary(), nil
}

// UpdateProxy 更新或清除账号代理。空字符串表示清除。
func (m *Manager) UpdateProxy(id, proxy string) (Summary, error) {
	proxy, err := validateProxy(proxy)
	if err != nil {
		return Summary{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	acc.Proxy = proxy
	// 代理变更后旧时区不再可信，清除缓存；下一次调度会按新出口 IP 重新解析。
	acc.Timezone = ""
	if err := m.save(); err != nil {
		return Summary{}, err
	}
	return acc.Summary(), nil
}

// TimezoneFor 返回账号调度应使用的 IANA 时区名：优先返回已缓存的解析结果，
// 否则通过账号代理请求地理服务解析（跟随代理出口 IP），成功后写入缓存并持久化。
//
// 无代理的账号直接返回空串（调用方回退服务器本地时区）。解析失败时返回错误，
// 调用方应回退本地时区，绝不让调度因解析失败而停摆。
func (m *Manager) TimezoneFor(id string) (string, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var proxy, cached string
	if ok {
		proxy, cached = acc.Proxy, acc.Timezone
	}
	resolver := m.tzResolver
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("账号不存在: %s", id)
	}
	if cached != "" {
		return cached, nil
	}
	if proxy == "" {
		return "", nil
	}
	if resolver == nil {
		resolver = geo.NewResolver()
	}
	tz, err := resolver.TimezoneFor(context.Background(), proxy)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if acc := m.accounts[id]; acc != nil && acc.Proxy == proxy {
		acc.Timezone = tz
		_ = m.save()
	}
	m.mu.Unlock()
	return tz, nil
}

// RemoveAccount 删除账号。
func (m *Manager) RemoveAccount(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[id]; !ok {
		return false
	}
	delete(m.accounts, id)
	_ = m.save()
	return true
}

// GetAccount 返回账号深拷贝(含 Cookies),调用方可安全使用。
func (m *Manager) GetAccount(id string) (*Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[id]
	if !ok {
		return nil, false
	}
	return copyAccount(acc), true
}

// ListAccounts 返回所有账号的深拷贝(脱敏,不含 Cookies),按活跃状态排序。
// 兼容入口:新调用方请使用 ListSummaries。
func (m *Manager) ListAccounts() []*Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Account, 0, len(m.accounts))
	for _, acc := range m.accounts {
		cp := copyAccount(acc)
		cp.Cookies = nil
		cp.AppPassword = ""
		if acc.Mailbox != nil {
			mailbox := *acc.Mailbox
			mailbox.Password = ""
			cp.Mailbox = &mailbox
		}
		out = append(out, cp)
	}
	return out
}

// ListSummaries 返回所有账号的安全摘要,排序为 active → pending → error,
// 同状态按 name、id 升序。
func (m *Manager) ListSummaries() []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Summary, 0, len(m.accounts))
	for _, acc := range m.accounts {
		out = append(out, acc.Summary())
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// statusRank 返回状态的排序权重。
func statusRank(status string) int {
	switch status {
	case "active":
		return 0
	case "pending":
		return 1
	default:
		return 2
	}
}

// HMEClient 为指定账号创建一个新的 HME 客户端。
// 必须有有效的 Cookie 才能使用 HME 功能。
func (m *Manager) HMEClient(id string, verbose bool) (*hme.Client, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if len(snap.Cookies) == 0 {
		return nil, fmt.Errorf("账号未配置 Cookie，无法使用 HME 功能")
	}
	return hme.NewClient(snap.Cookies, snap.Host, snap.Proxy, verbose)
}

// NewLoginSession 为指定账号创建一个新的登录会话(尚未提交密码)。
//
// 返回的会话由调用方驱动两段式登录:Begin → (可选 CompleteOTP/CompleteSMS) →
// Summary。密码等敏感信息只存在于内存,不落盘。
func (m *Manager) NewLoginSession(id string) (*HMELoginSession, error) {
	email, client, err := m.loginClientSnapshot(id)
	if err != nil {
		return nil, err
	}
	return &HMELoginSession{mgr: m, id: id, email: email, client: client}, nil
}

// loginClientSnapshot 取账号快照并构造一个新的登录用 hme.Client。
// 两条登录路径(两段式 NewLoginSession 与一次性 HMEClientWithPassword)
// 共用同一份账号读取/邮箱回退/客户端构造逻辑,避免行为漂移。
func (m *Manager) loginClientSnapshot(id string) (email string, client *hme.Client, err error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return "", nil, fmt.Errorf("账号不存在: %s", id)
	}

	email = snap.ICloudEmail
	if email == "" {
		email = snap.RealEmail
	}
	if email == "" {
		return "", nil, fmt.Errorf("账号未设置邮箱地址")
	}

	client, err = hme.NewClient(nil, snap.Host, snap.Proxy, true)
	if err != nil {
		return "", nil, err
	}
	return email, client, nil
}

// persistLogin 把登录得到的 Cookie 落库(含 validate 刷新)、刷新别名计数,
// 并返回脱敏摘要。两段式(HMELoginSession.Summary)与一次性
// (HMEClientWithPassword)登录路径共用同一持久化语义,避免行为漂移。
func (m *Manager) persistLogin(id string, client *hme.Client) (Summary, error) {
	// 先保存 accountLogin 返回的 Cookie,随后通过 validate 刷新会话并再次持久化。
	// 国区与美区都走同一条刷新链路,避免只保存登录阶段的临时 token。
	if err := m.SaveCookies(id, client.Cookies); err != nil {
		return Summary{}, err
	}
	if err := client.ValidateSession(); err != nil {
		// validate 的失败响应也可能携带 Set-Cookie,尽量保留服务端最新状态。
		_ = m.SaveCookies(id, client.Cookies)
		return Summary{}, err
	}

	m.mu.Lock()
	cur, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	cur.Cookies = cloneCookies(client.Cookies)
	cur.Status = "active"
	cur.LastValidated = time.Now().Format(time.RFC3339)
	cur.LastError = ""
	if info := client.AccountInfo(); info != nil {
		cur.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
		if cur.ICloudEmail == "" {
			cur.ICloudEmail = deriveICloudEmail(info)
		}
	}
	saveErr := m.save()
	m.mu.Unlock()
	if saveErr != nil {
		return Summary{}, saveErr
	}

	// 登录后立即按真实别名列表刷新计数,避免账号管理页停留在 0/0。
	// 必须在取摘要之前刷新, 否则本次响应里的计数仍是旧值。
	if aliases, listErr := client.ListAliases(); listErr == nil {
		_ = m.UpdateAliasCounts(id, aliases)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	cur, ok = m.accounts[id]
	if !ok {
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	return cur.Summary(), nil
}

// CheckAccount 检测指定账号的登录态是否仍然有效。
//
// 语义对齐参考项目 hme-manager 的 check(): 对现有 Cookie 做一次低风险的
// validate 探活(不创建/不修改任何远端资源), 并把结果落库——
//   - 有效: status=active、last_validated 刷新、last_error 清空
//   - 失效: status=error、last_error 记录可读原因(绝不写 Cookie 值)
//
// 与 UpdateCookies 的区别: 不接收新 Cookie, 纯粹探测当前凭据;
// 与 HME 业务调用的区别: 不触发创建冷却, 可随时手动执行。
func (m *Manager) CheckAccount(id string) (Summary, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	if len(snap.Cookies) == 0 {
		return Summary{}, fmt.Errorf("账号未配置 Cookie，无法检测登录状态")
	}
	if snap.Host == "" {
		snap.Host = "icloud.com"
	}

	client, err := hme.NewClient(snap.Cookies, snap.Host, snap.Proxy, false)
	var checkErr error
	if err != nil {
		checkErr = fmt.Errorf("创建客户端失败: %w", err)
	} else if err := client.ValidateSession(); err != nil {
		checkErr = err
	}

	m.mu.Lock()
	cur, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	if checkErr == nil {
		// validate 成功响应可能刷新 Cookie, 保存最新值。
		cur.Cookies = cloneCookies(client.Cookies)
		cur.Status = "active"
		cur.LastValidated = time.Now().Format(time.RFC3339)
		cur.LastError = ""
		if info := client.AccountInfo(); info != nil {
			cur.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
			if cur.ICloudEmail == "" {
				cur.ICloudEmail = deriveICloudEmail(info)
			}
		}
	} else {
		// 失败响应也可能携带 Set-Cookie, 尽量保留服务端最新状态。
		if client != nil {
			cur.Cookies = cloneCookies(client.Cookies)
		}
		cur.Status = "error"
		cur.LastError = truncate("登录态检测失败: "+checkErr.Error(), 300)
	}
	saveErr := m.save()
	result := cur.Summary()
	m.mu.Unlock()
	if saveErr != nil {
		return Summary{}, saveErr
	}
	if checkErr != nil {
		return result, checkErr
	}

	// 检测通过后顺带刷新别名计数(失败不影响检测结果)。
	if aliases, listErr := client.ListAliases(); listErr == nil {
		_ = m.UpdateAliasCounts(id, aliases)
		if updated, ok := m.GetAccount(id); ok {
			result = updated.Summary()
		}
	}
	return result, nil
}

// HMELoginSession 把 *hme.Client 的两段式登录与账号持久化绑在一起。
type HMELoginSession struct {
	mgr    *Manager
	id     string
	email  string
	client *hme.Client
}

func (s *HMELoginSession) Begin(password string) error {
	return s.client.BeginLogin(s.email, password)
}

func (s *HMELoginSession) CompleteOTP(code string) error { return s.client.CompleteOTP(code) }

func (s *HMELoginSession) CompleteSMS(phoneID int, code string) error {
	return s.client.CompleteSMS(phoneID, code)
}

func (s *HMELoginSession) ResendOTP() error { return s.client.ResendOTP() }

// PrepareDelivery 决定验证码投递方式并发起投递(推送/短信/待选)。
func (s *HMELoginSession) PrepareDelivery() (string, error) {
	return s.client.PrepareDelivery()
}

// Delivery 返回当前会话的验证码投递方式。
func (s *HMELoginSession) Delivery() string { return s.client.Delivery() }

func (s *HMELoginSession) TrustedPhones() ([]hme.TrustedPhone, error) {
	return s.client.TrustedPhones()
}

func (s *HMELoginSession) SendSMS(phoneID int) error { return s.client.SendSMS(phoneID) }

// Summary 把登录得到的 Cookie 落库(含 validate 刷新),并返回脱敏摘要。
func (s *HMELoginSession) Summary() (Summary, error) {
	return s.mgr.persistLogin(s.id, s.client)
}

// HMEClientWithPassword 为指定账号创建一个新的 HME 客户端,使用账号密码登录。
// 登录成功后会自动获取 Cookie 并保存到账号配置。
func (m *Manager) HMEClientWithPassword(id, password string, otpProvider hme.OTPProvider) (*hme.Client, error) {
	email, client, err := m.loginClientSnapshot(id)
	if err != nil {
		return nil, err
	}

	if err := client.Login(email, password, otpProvider); err != nil {
		return nil, err
	}
	if _, err := m.persistLogin(id, client); err != nil {
		return nil, err
	}
	return client, nil
}

// MailClient 为指定账号创建 IMAP 邮件客户端(每次新建, 不走连接池)。
// 需要事先设置 iCloud 邮箱和 App 专用密码。
// 高频读信请用 WithMailClient 复用长连接。
func (m *Manager) MailClient(id string) (*mail.Client, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if snap.Mailbox != nil && snap.Mailbox.Email != "" && snap.Mailbox.Password != "" {
		return mail.NewClientWithServer(snap.Mailbox.Email, snap.Mailbox.Password, snap.Mailbox.IMAPHost, snap.Mailbox.IMAPPort), nil
	}
	imapEmail := snap.ICloudEmail
	if imapEmail == "" {
		imapEmail = snap.RealEmail
	}
	if !isICloudDomain(imapEmail) {
		return nil, fmt.Errorf("账号未设置 iCloud 邮箱 (当前: %s)", imapEmail)
	}
	if snap.AppPassword == "" {
		return nil, fmt.Errorf("账号未设置 App 专用密码")
	}
	return mail.NewClient(imapEmail, snap.AppPassword), nil
}

// WithMailClient 使用连接池中的长连接执行 fn(串行/账号级)。
// fn 返回后连接保留在池中, 不会 Logout。
func (m *Manager) WithMailClient(id string, fn func(*mail.Client) error) error {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var mailbox *MailboxConfig
	if ok && acc.Mailbox != nil {
		copy := *acc.Mailbox
		mailbox = &copy
	}
	m.mu.RUnlock()
	if mailbox != nil && mailbox.Email != "" && mailbox.Password != "" {
		mc := mail.NewClientWithServer(mailbox.Email, mailbox.Password, mailbox.IMAPHost, mailbox.IMAPPort)
		if err := mc.Connect(); err != nil {
			return err
		}
		defer mc.Disconnect()
		return fn(mc)
	}
	imapEmail, appPassword, err := m.imapCreds(id)
	if err != nil {
		return err
	}
	if m.imapPool == nil {
		m.imapPool = mail.NewPool()
	}
	return m.imapPool.Do(imapEmail, appPassword, fn)
}

func (m *Manager) imapCreds(id string) (imapEmail, appPassword string, err error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return "", "", fmt.Errorf("账号不存在: %s", id)
	}
	imapEmail = snap.ICloudEmail
	if imapEmail == "" {
		imapEmail = snap.RealEmail
	}
	if !isICloudDomain(imapEmail) {
		return "", "", fmt.Errorf("账号未设置 iCloud 邮箱 (当前: %s)", imapEmail)
	}
	if snap.AppPassword == "" {
		return "", "", fmt.Errorf("账号未设置 App 专用密码")
	}
	return imapEmail, snap.AppPassword, nil
}

// SetMailbox validates and stores an external IMAP mailbox after testing it.
func (m *Manager) SetMailbox(id string, config MailboxConfig) error {
	config.Provider = strings.TrimSpace(config.Provider)
	config.Email = strings.TrimSpace(config.Email)
	config.IMAPHost = strings.TrimSpace(config.IMAPHost)
	if config.Email == "" || config.IMAPHost == "" || config.Password == "" {
		return fmt.Errorf("收件邮箱、IMAP 服务器和授权码不能为空")
	}
	if strings.Contains(config.IMAPHost, "://") || config.IMAPPort < 1 || config.IMAPPort > 65535 {
		return fmt.Errorf("IMAP 服务器或端口无效")
	}
	m.mu.RLock()
	_, ok := m.accounts[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	mc := mail.NewClientWithServer(config.Email, config.Password, config.IMAPHost, config.IMAPPort)
	if err := mc.Connect(); err != nil {
		return err
	}
	_, err := mc.InboxCount()
	mc.Disconnect()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.Mailbox = &config
	return m.save()
}

// WebMailClient 为指定账号创建 Web 邮件客户端。
// 使用 Cookie 认证，无需 App Password。
func (m *Manager) WebMailClient(id string) (*mail.WebClient, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if len(snap.Cookies) == 0 {
		return nil, fmt.Errorf("账号未配置 Cookie，无法读取邮件")
	}
	// 从 cookies 中获取 dsid
	dsid := dsidFromCookies(snap.Cookies)
	return mail.NewWebClient(snap.Cookies, dsid, snap.Host), nil
}

// dsidFromCookies 从 X-APPLE-WEBAUTH-USER cookie 解析 dsid。
//
// cookie 值形如 "v=1:s=1:d=19657242416"(含包裹引号)。必须先去掉引号再按 :d= 切分,
// 否则解析出的 dsid 会带尾随引号(如 19657242416"),拼进 dsid 查询参数后 iCloud
// mccgateway 直接返回 400。
func dsidFromCookies(cookies map[string]string) string {
	v, ok := cookies["X-APPLE-WEBAUTH-USER"]
	if !ok {
		return ""
	}
	v = strings.Trim(v, `"`)
	parts := strings.Split(v, ":d=")
	if len(parts) != 2 {
		return ""
	}
	return strings.Trim(parts[1], `"`)
}

// SetAppPassword 设置 iCloud 邮箱和 App 专用密码,并测试 IMAP 连接。
func (m *Manager) SetAppPassword(id, icloudEmail, appPassword string) error {
	// 邮箱规范化(与 AddAccount/UpdateMetadata 同一入口):
	// 全角＠/首尾空白在这里被清理, 非法格式在连接前即被拒绝,
	// 避免用户拿到「IMAP 验证失败」却看不出是邮箱格式问题。
	normalized, err := normalizeEmail(icloudEmail)
	if err != nil {
		return err
	}
	icloudEmail = normalized
	if appPassword == "" {
		return fmt.Errorf("App 专用密码不能为空")
	}

	m.mu.RLock()
	_, ok := m.accounts[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}

	// 测试连接(锁外)
	mc := mail.NewClient(icloudEmail, appPassword)
	if err := mc.Connect(); err != nil {
		return err
	}
	count, err := mc.InboxCount()
	mc.Disconnect()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.ICloudEmail = icloudEmail
	acc.AppPassword = appPassword
	if err := m.save(); err != nil {
		return err
	}
	_ = count
	return nil
}

// SaveCookies 保存指定账号的最新 Cookie（HMEClient 操作后刷新的 token）。
// 用于客户端 validate/操作过程中从 Set-Cookie 获取了新 token 后持久化。
func (m *Manager) SaveCookies(id string, cookies map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.Cookies = cloneCookies(cookies)
	return m.save()
}

// UpdateAliasCounts 按最新别名列表重算账号的别名总数/活跃数并持久化。
//
// 计数以传入列表为准整体覆盖(非累加),因此在列别名/创建/删除/停用别名或
// 会话重新校验后调用,都能让账号管理页的显示与真实状态一致。
func (m *Manager) UpdateAliasCounts(id string, aliases []hme.Alias) error {
	total, active := countAliases(aliases)
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.AliasTotal = total
	acc.AliasActive = active
	return m.save()
}

// countAliases 返回别名总数与其中的活跃数。
func countAliases(aliases []hme.Alias) (total, active int) {
	total = len(aliases)
	for _, al := range aliases {
		if al.Active {
			active++
		}
	}
	return total, active
}

// UpdateCookies 更新指定账号的 Cookie,并自动校验会话有效性。
func (m *Manager) UpdateCookies(id string, cookies map[string]string) error {
	if len(cookies) == 0 {
		return fmt.Errorf("cookies 不能为空")
	}
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}

	// 自动校验 Cookie 是否有效(锁外对快照操作)
	snap.Cookies = cookies
	if snap.Host == "" {
		snap.Host = "icloud.com"
	}
	client, err := hme.NewClient(cookies, snap.Host, snap.Proxy, false)
	if err != nil {
		snap.Status = "error"
		snap.LastError = "创建客户端失败: " + err.Error()
	} else if err := client.ValidateSession(); err != nil {
		// validate 即使失败也可能通过 Set-Cookie 刷新部分会话状态。
		snap.Cookies = client.Cookies
		snap.Status = "error"
		snap.LastError = "Cookie 校验失败: " + err.Error()
	} else {
		// 显式保存 validate 响应刷新的 Cookie，不依赖传入 map 的引用关系。
		snap.Cookies = client.Cookies
		snap.Status = "active"
		snap.LastValidated = time.Now().Format(time.RFC3339)
		snap.LastError = ""
		if info := client.AccountInfo(); info != nil {
			snap.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
			if snap.ICloudEmail == "" {
				snap.ICloudEmail = deriveICloudEmail(info)
			}
		}
		// 会话恢复后按真实别名列表重算计数,让账号管理页显示与实际一致。
		if aliases, listErr := client.ListAliases(); listErr == nil {
			snap.AliasTotal, snap.AliasActive = countAliases(aliases)
		}
	}

	m.mu.Lock()
	cur, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	cur.Cookies = snap.Cookies
	cur.Status = snap.Status
	cur.LastValidated = snap.LastValidated
	cur.LastError = snap.LastError
	cur.RealEmail = snap.RealEmail
	cur.AliasTotal = snap.AliasTotal
	cur.AliasActive = snap.AliasActive
	if cur.ICloudEmail == "" {
		cur.ICloudEmail = snap.ICloudEmail
	}
	saveErr := m.save()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return saveErr
}

// ---- 辅助函数 ----

// deriveICloudEmail 从账号身份推导 iCloud 邮箱地址(用于 IMAP 登录)。
//
// 规则:
//  1. primaryEmail 是 @icloud.com/@me.com/@mac.com → 直接用
//  2. appleId 是上述域名 → 直接用
//  3. appleId 是第三方邮箱(如 @qq.com) → 取 local part 拼 @icloud.com
func deriveICloudEmail(info *hme.AccountInfo) string {
	primary := strings.TrimSpace(info.PrimaryEmail)
	appleID := strings.TrimSpace(info.AppleID)

	if isICloudDomain(primary) {
		return primary
	}
	if isICloudDomain(appleID) {
		return appleID
	}
	if strings.Contains(appleID, "@") {
		local := strings.SplitN(appleID, "@", 2)[0]
		return local + "@icloud.com"
	}
	return firstNonEmpty(primary, appleID)
}

func isICloudDomain(email string) bool {
	return email != "" && (strings.Contains(email, "@icloud.com") ||
		strings.Contains(email, "@me.com") ||
		strings.Contains(email, "@mac.com"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
