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
	ID             string `json:"id"`
	Name           string `json:"name"`
	RealEmail      string `json:"real_email"`
	ICloudEmail    string `json:"icloud_email"`
	Host           string `json:"host"`
	Status         string `json:"status"`
	AliasTotal     int    `json:"alias_total"`
	AliasActive    int    `json:"alias_active"`
	HasCookies     bool   `json:"has_cookies"`
	HasAppPassword bool   `json:"has_app_password"`
	HasProxy       bool   `json:"has_proxy"`
	LastValidated  string `json:"last_validated"`
	StatusMessage  string `json:"status_message,omitempty"`
	CreatedAt      string `json:"created_at"`
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
	switch a.Status {
	case "pending":
		s.StatusMessage = "等待配置或验证凭据"
	case "error":
		s.StatusMessage = "凭据验证失败"
	}
	return s
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

// validateEmail 校验邮箱:用 net/mail.ParseAddress 并要求地址值等于输入。
func validateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("iCloud 邮箱不能为空")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("邮箱地址格式无效")
	}
	if addr.Address != email {
		return fmt.Errorf("邮箱地址格式无效")
	}
	return nil
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
