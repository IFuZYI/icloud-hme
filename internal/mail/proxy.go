// 代理拨号: 让 IMAP 连接与 HME API 走同一账号出口。
//
// go-imap 的 DialWithDialerTLS 只接受实现了 Dial(network, addr) 的拨号器,
// 因此这里先经代理建立到目标地址的 TCP 隧道(HTTP CONNECT 或 SOCKS5),
// 再由 go-imap 在隧道上叠加到 IMAP 服务器的 TLS。
package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// proxyDialer 通过代理建立 TCP 隧道, 供 go-imap 的 DialWithDialerTLS 使用。
type proxyDialer struct {
	proxyURL *url.URL
	timeout  time.Duration
	// tlsConfig 是 HTTPS 代理自身的 TLS 配置; nil 表示系统根证书 + 主机名校验。
	// 仅测试通过 newProxyDialerWithTLS 注入自签证书, 生产不暴露可写全局。
	tlsConfig *tls.Config
}

// newProxyDialer 解析并校验账号代理地址。
func newProxyDialer(rawURL string, timeout time.Duration) (*proxyDialer, error) {
	return newProxyDialerWithTLS(rawURL, timeout, nil)
}

// newProxyDialerWithTLS 与 newProxyDialer 相同, 但可注入 HTTPS 代理的 TLS 配置(仅测试)。
func newProxyDialerWithTLS(rawURL string, timeout time.Duration, tlsCfg *tls.Config) (*proxyDialer, error) {
	normalized, err := normalizeProxyURL(rawURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("代理地址无效")
	}
	return &proxyDialer{proxyURL: u, timeout: timeout, tlsConfig: tlsCfg}, nil
}

// normalizeProxyURL 校验代理地址并按协议补全默认端口:
// http=80, https=443, socks5/socks5h=1080。
//
// 必须集中归一化: tls-client 只给 http/https 补默认端口, socks5://host
// 会以 "missing port in address" 失败; IMAP 与 Web 回退需要同一份地址。
// 错误信息不得包含原始 URL(可能带 user:pass)。
func normalizeProxyURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("代理地址无效")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("不支持的代理协议: %s", u.Scheme)
	}
	if u.Port() == "" {
		port := "1080"
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
		u.Host = net.JoinHostPort(u.Hostname(), port)
	}
	return u.String(), nil
}

// Dial 建立经代理到 addr 的 TCP 隧道。
func (d *proxyDialer) Dial(network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("代理仅支持 TCP 连接")
	}
	// CONNECT 请求行/Host 头直接使用 addr, 拒绝所有控制字符与空格,
	// 防止请求行/头注入。
	for i := 0; i < len(addr); i++ {
		if addr[i] < 0x20 || addr[i] == 0x7f || addr[i] == ' ' {
			return nil, fmt.Errorf("IMAP 服务器地址无效")
		}
	}
	var (
		conn net.Conn
		err  error
	)
	switch d.proxyURL.Scheme {
	case "http", "https":
		conn, err = d.dialHTTPConnect(addr)
	case "socks5", "socks5h":
		conn, err = d.dialSOCKS5(addr)
	default:
		err = fmt.Errorf("不支持的代理协议: %s", d.proxyURL.Scheme)
	}
	if err != nil {
		return nil, err
	}
	// 与 *net.Dialer.Timeout 语义一致: 覆盖 go-imap 在 DialTLS 返回前
	// 读取服务器问候的阶段(见 dialIMAP 注释)。
	if d.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(d.timeout))
	}
	return conn, nil
}

// proxyAddr 返回代理服务器的 host:port; URL 未写端口时按协议补默认端口,
// 与 tls-client/HME 的行为保持一致(http=80, https=443, socks5=1080)。
func (d *proxyDialer) proxyAddr() string {
	if d.proxyURL.Port() != "" {
		return d.proxyURL.Host
	}
	switch d.proxyURL.Scheme {
	case "http":
		return net.JoinHostPort(d.proxyURL.Hostname(), "80")
	case "https":
		return net.JoinHostPort(d.proxyURL.Hostname(), "443")
	default:
		return net.JoinHostPort(d.proxyURL.Hostname(), "1080")
	}
}

// dialSOCKS5 通过 SOCKS5 代理建立隧道(支持用户名/密码认证)。
func (d *proxyDialer) dialSOCKS5(addr string) (net.Conn, error) {
	var auth *xproxy.Auth
	if u := d.proxyURL.User; u != nil {
		pass, _ := u.Password()
		auth = &xproxy.Auth{User: u.Username(), Password: pass}
	}
	forward := &net.Dialer{Timeout: d.timeout}
	pd, err := xproxy.SOCKS5("tcp", d.proxyAddr(), auth, forward)
	if err != nil {
		return nil, fmt.Errorf("创建 SOCKS5 代理拨号器失败: %w", err)
	}
	ctx := context.Background()
	if d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}
	if cd, ok := pd.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", addr)
	}
	return pd.Dial("tcp", addr)
}

// dialHTTPConnect 通过 HTTP/HTTPS 代理的 CONNECT 方法建立隧道。
func (d *proxyDialer) dialHTTPConnect(addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.timeout}
	conn, err := dialer.Dial("tcp", d.proxyAddr())
	if err != nil {
		return nil, fmt.Errorf("连接代理失败: %w", err)
	}
	// 先设 deadline 再握手: HTTPS 代理的 TLS 握手也必须受超时约束,
	// 否则代理接受 TCP 却不响应时 Connect 会无界阻塞。
	if d.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(d.timeout))
	}
	if d.proxyURL.Scheme == "https" {
		tlsCfg := d.tlsConfig
		if tlsCfg == nil {
			tlsCfg = &tls.Config{}
		}
		tlsCfg = tlsCfg.Clone()
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = d.proxyURL.Hostname()
		}
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("代理 TLS 握手失败: %w", err)
		}
		conn = tlsConn
	}

	var req strings.Builder
	fmt.Fprintf(&req, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if u := d.proxyURL.User; u != nil {
		pass, _ := u.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pass))
		fmt.Fprintf(&req, "Proxy-Authorization: Basic %s\r\n", auth)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("写入代理 CONNECT 请求失败: %w", err)
	}

	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)
	statusLine, err := tp.ReadLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}
	// 形如 "HTTP/1.1 200 Connection established"。
	parts := strings.SplitN(statusLine, " ", 3)
	if len(parts) < 2 || parts[1] != "200" {
		_ = conn.Close()
		return nil, fmt.Errorf("代理 CONNECT 失败: %s", statusLine)
	}
	if _, err := tp.ReadMIMEHeader(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取代理响应头失败: %w", err)
	}
	// CONNECT 响应后若有预读字节, 必须交还给上层, 否则 TLS 握手会丢数据。
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn 让已被 bufio 预读的连接继续可读。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
