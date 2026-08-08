// Package account 实现多账号管理器。
//
// 负责账号 CRUD、Cookie 解析(Header String / JSON)、持久化到 accounts.json,
// 以及创建 HME 客户端和邮件客户端。对应原 Python 项目 account_manager.py。
package account

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	Status        string            `json:"status"` // active / error
	AliasTotal    int               `json:"alias_total"`
	AliasActive   int               `json:"alias_active"`
	LastValidated string            `json:"last_validated"`
	LastError     string            `json:"last_error,omitempty"`
	CreatedAt     string            `json:"created_at"`
}

// Manager 管理多个 iCloud 账号,线程安全。
type Manager struct {
	mu       sync.RWMutex
	accounts map[string]*Account
	dataDir  string
	dataFile string
}

// copyAccount 返回账号的深拷贝(含 Cookies map),必须在持锁时调用。
func copyAccount(acc *Account) *Account {
	if acc == nil {
		return nil
	}
	cp := *acc
	if acc.Cookies != nil {
		cp.Cookies = make(map[string]string, len(acc.Cookies))
		for k, v := range acc.Cookies {
			cp.Cookies[k] = v
		}
	}
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
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
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
	if err := validateEmail(input.ICloudEmail); err != nil {
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
	acc, err := m.newAccount(name, input.ICloudEmail, input.CookieInput, host, proxy)
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
		a.Status = "error"
		a.LastError = truncate(err.Error(), 300)
		return
	}
	a.Status = "active"
	if info := client.AccountInfo(); info != nil {
		a.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
		if a.ICloudEmail == "" {
			a.ICloudEmail = deriveICloudEmail(info)
		}
	}
	if aliases, err := client.ListAliases(); err == nil {
		a.AliasTotal = len(aliases)
		for _, al := range aliases {
			if al.Active {
				a.AliasActive++
			}
		}
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
		if err := validateEmail(*input.ICloudEmail); err != nil {
			return Summary{}, err
		}
		v := strings.TrimSpace(*input.ICloudEmail)
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
	if err := m.save(); err != nil {
		return Summary{}, err
	}
	return acc.Summary(), nil
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

// HMEClientWithPassword 为指定账号创建一个新的 HME 客户端,使用账号密码登录。
// 登录成功后会自动获取 Cookie 并保存到账号配置。
func (m *Manager) HMEClientWithPassword(id, password string, otpProvider hme.OTPProvider) (*hme.Client, error) {
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

	email := snap.ICloudEmail
	if email == "" {
		email = snap.RealEmail
	}
	if email == "" {
		return nil, fmt.Errorf("账号未设置邮箱地址")
	}

	client, err := hme.NewClient(nil, snap.Host, snap.Proxy, true)
	if err != nil {
		return nil, err
	}

	if err := client.Login(email, password, otpProvider); err != nil {
		return nil, err
	}

	// 保存登录后的 Cookie 到账号(网络完成后重新加锁,以 id 查找当前对象写回)
	m.mu.Lock()
	if cur, ok := m.accounts[id]; ok {
		cur.Cookies = client.Cookies
		cur.Status = "active"
		cur.LastValidated = time.Now().Format(time.RFC3339)
		cur.LastError = ""
		m.save()
	}
	m.mu.Unlock()

	return client, nil
}

// MailClient 为指定账号创建 IMAP 邮件客户端。
// 需要事先设置 iCloud 邮箱和 App 专用密码。
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
	dsid := ""
	if v, ok := snap.Cookies["X-APPLE-WEBAUTH-USER"]; ok {
		// 解析 "v=1:s=1:d=22789132008" 格式
		parts := strings.Split(v, ":d=")
		if len(parts) == 2 {
			dsid = parts[1]
		}
	}
	return mail.NewWebClient(snap.Cookies, dsid, snap.Host), nil
}

// SetAppPassword 设置 iCloud 邮箱和 App 专用密码,并测试 IMAP 连接。
func (m *Manager) SetAppPassword(id, icloudEmail, appPassword string) error {
	if icloudEmail == "" {
		return fmt.Errorf("iCloud 邮箱不能为空")
	}
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
	acc.Cookies = cookies
	return m.save()
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
		snap.Status = "error"
		snap.LastError = "Cookie 校验失败: " + err.Error()
	} else {
		snap.Status = "active"
		snap.LastValidated = time.Now().Format(time.RFC3339)
		snap.LastError = ""
		if info := client.AccountInfo(); info != nil {
			snap.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
			if snap.ICloudEmail == "" {
				snap.ICloudEmail = deriveICloudEmail(info)
			}
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
