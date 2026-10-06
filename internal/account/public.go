// Package account - 公开账号 DTO 与输入校验。
//
// HTTP 层只能序列化 account.Summary;内部 Account(含 Cookies、AppPassword、
// Proxy 等秘密)只用于持久化和内部客户端构造,绝不直接出现在响应中。
package account

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

// Summary 是账号的安全公开表示,不含任何秘密字段。
type Summary struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	RealEmail      string          `json:"real_email"`
	ICloudEmail    string          `json:"icloud_email"`
	Host           string          `json:"host"`
	Status         string          `json:"status"`
	AliasTotal     int             `json:"alias_total"`
	AliasActive    int             `json:"alias_active"`
	HasCookies     bool            `json:"has_cookies"`
	HasAppPassword bool            `json:"has_app_password"`
	HasProxy       bool            `json:"has_proxy"`
	Mailbox        *MailboxSummary `json:"mailbox,omitempty"`
	LastValidated  string          `json:"last_validated"`
	StatusMessage  string          `json:"status_message,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

// Summary 返回账号的安全快照,忽略内部 LastError。
func (a *Account) Summary() Summary {
	s := Summary{
		ID:             a.ID,
		Name:           a.Name,
		RealEmail:      a.RealEmail,
		ICloudEmail:    a.ICloudEmail,
		Host:           a.Host,
		Status:         a.Status,
		AliasTotal:     a.AliasTotal,
		AliasActive:    a.AliasActive,
		HasCookies:     len(a.Cookies) > 0,
		HasAppPassword: a.AppPassword != "",
		HasProxy:       a.Proxy != "",
		LastValidated:  a.LastValidated,
		CreatedAt:      a.CreatedAt,
	}
	if a.Mailbox != nil {
		s.Mailbox = &MailboxSummary{Provider: a.Mailbox.Provider, Email: a.Mailbox.Email, IMAPHost: a.Mailbox.IMAPHost, IMAPPort: a.Mailbox.IMAPPort}
	}
	switch a.Status {
	case "pending":
		s.StatusMessage = "等待配置或验证凭据"
	case "error":
		s.StatusMessage = "凭据验证失败"
	}
	return s
}

type MailboxSummary struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	IMAPHost string `json:"imap_host"`
	IMAPPort int    `json:"imap_port"`
}

// AddAccountInput 是添加账号的输入。
type AddAccountInput struct {
	Name        string
	ICloudEmail string
	CookieInput string
	Host        string
	Proxy       string
}

// UpdateAccountInput 是编辑账号基本信息的输入,指针字段表示可选。
type UpdateAccountInput struct {
	Name        *string
	ICloudEmail *string
	Host        *string
}

// validateName 校验名称:去空白后 1–64 字符。
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("名称不能为空")
	}
	if len([]rune(name)) > 64 {
		return "", fmt.Errorf("名称不能超过 64 个字符")
	}
	return name, nil
}

// validateHost 校验主机:只能是 icloud.com 或 icloud.com.cn。
func validateHost(host string) (string, error) {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return "icloud.com", nil
	}
	if host != "icloud.com" && host != "icloud.com.cn" {
		return "", fmt.Errorf("主机只能是 icloud.com 或 icloud.com.cn")
	}
	return host, nil
}

// normalizeEmail 规范化并校验邮箱地址。
//
// 处理真实世界的输入瑕疵(用户报障「邮箱地址格式无效」的来源):
//   - 全角＠(中文输入法)——net/mail.ParseAddress 直接拒绝,先转半角
//   - 首尾空白,含全角空格
//   - 显示名格式 `"名称" <a@b.com>`——提取出裸地址
//
// 校验规则: 必须是单一地址, 且解析结果包含 @ 与域名点号之外,
// 至少要有本地部分与域名(裸 `a@b` 也接受——iCloud 短域名历史遗留)。
// 返回规范化后的地址; 失败返回错误。
func normalizeEmail(email string) (string, error) {
	// 全角＠与全角空格 → 半角(中文输入法常见)
	cleaned := strings.ReplaceAll(email, "＠", "@")
	cleaned = strings.ReplaceAll(cleaned, "\u3000", " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "", fmt.Errorf("iCloud 邮箱不能为空")
	}

	// 先按裸地址解析(绝大多数情况); 失败再试显示名格式。
	addr, err := mail.ParseAddress(cleaned)
	if err != nil {
		// 逗号分隔的多个地址: ParseAddress 对 "a@b, c@d" 报
		// "expected single address", 此时明确拒绝——无法确定用哪个登录。
		if strings.Contains(cleaned, ",") {
			return "", fmt.Errorf("邮箱地址格式无效: 只能填写一个地址")
		}
		return "", fmt.Errorf("邮箱地址格式无效")
	}
	address := strings.TrimSpace(addr.Address)
	if address == "" || !strings.Contains(address, "@") {
		return "", fmt.Errorf("邮箱地址格式无效")
	}
	local, domain, _ := strings.Cut(address, "@")
	if local == "" || domain == "" {
		return "", fmt.Errorf("邮箱地址格式无效")
	}
	return address, nil
}

// validateEmail 校验邮箱; 校验通过时返回规范化后的值。
func validateEmail(email string) (string, error) {
	return normalizeEmail(email)
}

// validateProxy 校验代理;空表示清除。
func validateProxy(proxy string) (string, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return "", nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("代理地址格式无效")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return "", fmt.Errorf("代理地址格式无效")
	}
	return proxy, nil
}
