// Package geo 解析代理出口 IP 的地理时区，供拟人调度按当地作息计算。
//
// 设计要点:
//   - 通过账号代理发起请求(与业务请求同一出口 IP),绝不直连;
//   - 使用带 Chrome TLS 指纹的 HTTP 客户端(与 hme.Client 一致),降低被风控概率;
//   - 多个解析服务依次回退(ipwho.is → ipinfo.io → ip-api.com;最后一个仅提供
//     明文 HTTP 免费端点, 可被同路径代理篡改——只影响调度时段选择, 风险低);
//   - 结果为 IANA 时区名(如 America/Denver),调用方用 time.LoadLocation 加载;
//   - 全部失败时返回错误,由调用方决定回退策略(绝不阻塞任务调度)。
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Resolver 解析代理出口 IP 的地理时区。实现必须并发安全。
type Resolver interface {
	// TimezoneFor 返回 proxyURL 出口 IP 的 IANA 时区名。
	// 解析失败时返回错误,由调用方决定回退策略。
	TimezoneFor(ctx context.Context, proxyURL string) (string, error)
}

// httpResolver 是 Resolver 的默认实现,通过代理请求多个 IP 地理服务。
type httpResolver struct {
	endpoints []endpoint
}

// endpoint 描述一个地理解析服务及其响应解析器。
type endpoint struct {
	url   string
	parse func([]byte) (string, error)
}

// NewResolver 创建默认解析器:依次尝试多个地理服务。
//
// 每个请求都通过账号代理发出(与业务请求同一出口 IP),绝不直连,
// 否则解析出的会是服务器所在地而非代理所在地。
func NewResolver() Resolver {
	return &httpResolver{
		endpoints: []endpoint{
			{url: "https://ipwho.is/", parse: parseIPWhoIs},
			{url: "https://ipinfo.io/json", parse: parseIPInfo},
			{url: "http://ip-api.com/json/?fields=status,message,timezone", parse: parseIPAPI},
		},
	}
}

// TimezoneFor 通过 proxyURL 请求地理服务,返回出口 IP 的 IANA 时区名。
//
// 逐个尝试端点,任一成功即返回;全部失败返回聚合错误。
// 时区名须能通过 time.LoadLocation 加载,否则视为该端点失败继续尝试。
func (r *httpResolver) TimezoneFor(ctx context.Context, proxyURL string) (string, error) {
	if len(r.endpoints) == 0 {
		return "", fmt.Errorf("地理解析器未初始化")
	}
	var lastErr error
	for _, ep := range r.endpoints {
		tz, err := r.fetchOne(ctx, proxyURL, ep)
		if err != nil {
			lastErr = err
			continue
		}
		return tz, nil
	}
	return "", fmt.Errorf("全部地理解析服务失败: %w", lastErr)
}

// fetchOne 通过指定代理请求单个端点并解析时区。
func (r *httpResolver) fetchOne(ctx context.Context, proxyURL string, ep endpoint) (string, error) {
	client, err := clientForProxy(proxyURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 %s 失败: %w", hostOf(ep.url), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("读取 %s 响应失败: %w", hostOf(ep.url), err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s 返回 HTTP %d", hostOf(ep.url), resp.StatusCode)
	}
	tz, err := ep.parse(body)
	if err != nil {
		return "", fmt.Errorf("解析 %s 响应失败: %w", hostOf(ep.url), err)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("%s 返回无效时区 %q", hostOf(ep.url), tz)
	}
	return tz, nil
}

// clientForProxy 为本次请求构造带代理的客户端。
//
// 空代理 → 直连(仅用于无代理账号);http/https/socks5 交给 tls-client
// 原生代理支持(与业务客户端同一套实现)。Chrome TLS 指纹保持一致。
func clientForProxy(proxyURL string) (tls_client.HttpClient, error) {
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(12),
		tls_client.WithClientProfile(profiles.Chrome_146),
		tls_client.WithNotFollowRedirects(),
	}
	if p := strings.TrimSpace(proxyURL); p != "" {
		options = append(options, tls_client.WithProxyUrl(p))
	}
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		// 不包装底层错误原文: tls-client 对含凭据的代理 URL 会把完整
		// `user:pass@host` 嵌进错误串, 保持「代理凭据不外泄」的不变量。
		return nil, fmt.Errorf("构造地理客户端失败(代理 %s)", hostOf(proxyURL))
	}
	return c, nil
}

// hostOf 返回 URL 的主机名(错误信息用,避免完整 URL 中的凭据外泄)。
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "地理服务"
	}
	return u.Hostname()
}

// parseIPWhoIs 解析 ipwho.is 响应: {"success":true,"timezone":{"id":"America/Denver"}}。
func parseIPWhoIs(body []byte) (string, error) {
	var resp struct {
		Success  bool `json:"success"`
		Timezone struct {
			ID string `json:"id"`
		} `json:"timezone"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.Timezone.ID == "" {
		return "", fmt.Errorf("响应缺少时区信息")
	}
	return resp.Timezone.ID, nil
}

// parseIPInfo 解析 ipinfo.io 响应: {"timezone":"America/Los_Angeles"}。
func parseIPInfo(body []byte) (string, error) {
	var resp struct {
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Timezone == "" {
		return "", fmt.Errorf("响应缺少时区信息")
	}
	return resp.Timezone, nil
}

// parseIPAPI 解析 ip-api.com 响应: {"status":"success","timezone":"America/Los_Angeles"}。
func parseIPAPI(body []byte) (string, error) {
	var resp struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Status != "success" || resp.Timezone == "" {
		if resp.Message != "" {
			return "", fmt.Errorf("服务返回失败: %s", resp.Message)
		}
		return "", fmt.Errorf("响应缺少时区信息")
	}
	return resp.Timezone, nil
}
