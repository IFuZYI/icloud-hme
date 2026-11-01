package mail

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"

	"icloud-hme/internal/mailtest"
)

// fakeHTTPConnectProxy 是进程内 HTTP CONNECT 代理。
//
// target 固定为本地假 IMAP 服务器地址: 测试用不可解析的 imap.example.test
// 作为客户端目标, 只有经代理才能到达服务器; 代理记录收到的 CONNECT 目标,
// 从而证明流量确实走了代理而不是直连。
type fakeHTTPConnectProxy struct {
	ln     net.Listener
	target string

	mu       sync.Mutex
	connects []string
	auth     []string
}

func newFakeHTTPConnectProxy(t *testing.T, target string) *fakeHTTPConnectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 HTTP 代理失败: %v", err)
	}
	p := &fakeHTTPConnectProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *fakeHTTPConnectProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *fakeHTTPConnectProxy) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	p.mu.Lock()
	p.connects = append(p.connects, req.Host)
	p.auth = append(p.auth, req.Header.Get("Proxy-Authorization"))
	p.mu.Unlock()

	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}

// URL 返回带 Basic 认证信息的代理地址(账号代理允许 http://user:pass@host:port)。
func (p *fakeHTTPConnectProxy) URL() string {
	return "http://user:pass@" + p.ln.Addr().String()
}

func (p *fakeHTTPConnectProxy) Connects() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.connects...)
}

func (p *fakeHTTPConnectProxy) AuthHeaders() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.auth...)
}

// fakeSOCKS5Proxy 是进程内 SOCKS5 代理(支持用户名/密码认证)。
// target 固定为本地假 IMAP 服务器; 记录客户端请求的目标地址与认证凭据。
type fakeSOCKS5Proxy struct {
	ln          net.Listener
	target      string
	requireAuth bool
	user, pass  string

	mu      sync.Mutex
	targets []string
	auth    []string
}

func newFakeSOCKS5Proxy(t *testing.T, target, user, pass string) *fakeSOCKS5Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动假 SOCKS5 代理失败: %v", err)
	}
	p := &fakeSOCKS5Proxy{ln: ln, target: target, requireAuth: user != "", user: user, pass: pass}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *fakeSOCKS5Proxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *fakeSOCKS5Proxy) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	ver, err := br.ReadByte()
	if err != nil || ver != 5 {
		return
	}
	n, err := br.ReadByte()
	if err != nil {
		return
	}
	methods := make([]byte, n)
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if p.requireAuth {
		if !bytes.Contains(methods, []byte{2}) {
			_, _ = conn.Write([]byte{5, 0xff})
			return
		}
		if _, err := conn.Write([]byte{5, 2}); err != nil {
			return
		}
		if err := p.readAuth(br, conn); err != nil {
			return
		}
	} else {
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
	}

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[0] != 5 || hdr[1] != 1 { // 仅支持 CONNECT
		return
	}
	host, err := readSOCKSAddr(br, hdr[3])
	if err != nil {
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBuf))))
	p.mu.Lock()
	p.targets = append(p.targets, target)
	p.mu.Unlock()

	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}) // connection refused
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}

func (p *fakeSOCKS5Proxy) readAuth(br *bufio.Reader, conn net.Conn) error {
	ver, err := br.ReadByte()
	if err != nil {
		return err
	}
	ulen, err := br.ReadByte()
	if err != nil {
		return err
	}
	user := make([]byte, ulen)
	if _, err := io.ReadFull(br, user); err != nil {
		return err
	}
	plen, err := br.ReadByte()
	if err != nil {
		return err
	}
	pass := make([]byte, plen)
	if _, err := io.ReadFull(br, pass); err != nil {
		return err
	}
	p.mu.Lock()
	p.auth = append(p.auth, string(user)+":"+string(pass))
	p.mu.Unlock()
	if ver != 1 || string(user) != p.user || string(pass) != p.pass {
		_, _ = conn.Write([]byte{1, 1})
		return fmt.Errorf("认证失败")
	}
	_, err = conn.Write([]byte{1, 0})
	return err
}

func readSOCKSAddr(br *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 3:
		n, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	case 4:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	default:
		return "", fmt.Errorf("未知地址类型 %d", atyp)
	}
}

// URL 返回带认证信息的 SOCKS5 代理地址。
func (p *fakeSOCKS5Proxy) URL() string {
	if p.user == "" {
		return "socks5://" + p.ln.Addr().String()
	}
	return "socks5://" + p.user + ":" + p.pass + "@" + p.ln.Addr().String()
}

func (p *fakeSOCKS5Proxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *fakeSOCKS5Proxy) Auth() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.auth...)
}

// TestConnectThroughSOCKS5Proxy 验证 IMAP 连接会经账号的 SOCKS5 代理到达
// 服务器, 并携带代理认证凭据。
func TestConnectThroughSOCKS5Proxy(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	proxy := newFakeSOCKS5Proxy(t, net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)), "socks-user", "socks-pass")

	prevTLS := imapTLSConfig
	imapTLSConfig = &tls.Config{InsecureSkipVerify: true} // 仅测试: 进程内假服务器为自签证书
	t.Cleanup(func() { imapTLSConfig = prevTLS })

	const target = "imap.example.test:993"
	c := NewClientWithServer("u@163.com", "auth-code", "imap.example.test", 993).WithProxy(proxy.URL())
	if err := c.Connect(); err != nil {
		t.Fatalf("经 SOCKS5 代理连接应成功: %v", err)
	}
	defer c.Disconnect()

	if got := proxy.Targets(); len(got) != 1 || got[0] != target {
		t.Fatalf("代理应收到 1 次 CONNECT %s, 实际 %v", target, got)
	}
	if got := proxy.Auth(); len(got) != 1 || got[0] != "socks-user:socks-pass" {
		t.Fatalf("代理认证凭据应为 socks-user:socks-pass, 实际 %v", got)
	}
}

// TestConnectThroughHTTPConnectProxy 验证 IMAP 连接会经账号的 HTTP 代理
// (CONNECT 隧道)到达服务器, 而不是直连。直连时 imap.example.test 不可解析,
// 只有代理转发才能成功; 同时断言 CONNECT 目标与 Basic 认证头。
func TestConnectThroughHTTPConnectProxy(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	proxy := newFakeHTTPConnectProxy(t, net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))

	prevTLS := imapTLSConfig
	imapTLSConfig = &tls.Config{InsecureSkipVerify: true} // 仅测试: 进程内假服务器为自签证书
	t.Cleanup(func() { imapTLSConfig = prevTLS })

	const target = "imap.example.test:993"
	c := NewClientWithServer("u@163.com", "auth-code", "imap.example.test", 993).WithProxy(proxy.URL())
	if err := c.Connect(); err != nil {
		t.Fatalf("经 HTTP CONNECT 代理连接应成功: %v", err)
	}
	defer c.Disconnect()

	got := proxy.Connects()
	if len(got) != 1 || got[0] != target {
		t.Fatalf("代理应收到 1 次 CONNECT %s, 实际 %v", target, got)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	auths := proxy.AuthHeaders()
	if len(auths) != 1 || auths[0] != wantAuth {
		t.Fatalf("代理认证头应为 %q, 实际 %v", wantAuth, auths)
	}
}

// TestProxyDialerDefaultsPorts 验证代理 URL 省略端口时按协议补默认端口:
// 账号校验允许省略端口, tls-client 也按默认端口处理; IMAP 拨号不能因此
// 报 "missing port in address"。
func TestProxyDialerDefaultsPorts(t *testing.T) {
	cases := []struct {
		name  string
		proxy string
		want  string
	}{
		{"http 默认 80", "http://proxy.example.com", "proxy.example.com:80"},
		{"https 默认 443", "https://proxy.example.com", "proxy.example.com:443"},
		{"socks5 默认 1080", "socks5://proxy.example.com", "proxy.example.com:1080"},
		{"显式端口保持", "socks5://proxy.example.com:1081", "proxy.example.com:1081"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := newProxyDialer(tc.proxy, time.Second)
			if err != nil {
				t.Fatalf("newProxyDialer(%q): %v", tc.proxy, err)
			}
			if got := d.proxyAddr(); got != tc.want {
				t.Fatalf("proxyAddr() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProxyDialerRejectsControlChars 验证 CONNECT 目标地址中的控制字符
// 与空格被拒绝, 防止请求行/ Host 头注入。
func TestProxyDialerRejectsControlChars(t *testing.T) {
	d, err := newProxyDialer("http://127.0.0.1:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		addr string
	}{
		{"CR", "imap.example.test:993\rX-Evil: 1"},
		{"LF", "imap.example.test:993\nX-Evil: 1"},
		{"CRLF", "imap.example.test:993\r\nX-Evil: 1"},
		{"NUL", "imap.example.test:993\x00"},
		{"DEL", "imap.example.test:993\x7f"},
		{"空格", "imap.example.test:993 X-Evil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Dial("tcp", tc.addr); err == nil || !strings.Contains(err.Error(), "IMAP 服务器地址无效") {
				t.Fatalf("含控制字符的地址应被拒绝并说明原因, 实际: %v", err)
			}
		})
	}
}

// TestHTTPSProxyHandshakeBounded 验证 HTTPS 代理的 TLS 握手受超时约束:
// 代理接受 TCP 却不应答 TLS 时, Dial 必须按 deadline 返回, 不能无界阻塞
// (无界阻塞会让上层超时后的 goroutine 一直持有连接池锁)。
func TestHTTPSProxyHandshakeBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// 接受连接但从不响应 TLS 握手。
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	d, err := newProxyDialer("https://"+ln.Addr().String(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := d.Dial("tcp", "imap.example.test:993")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("代理不响应 TLS 握手时应报错")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("代理 TLS 握手未受 deadline 约束, Dial 无界阻塞")
	}
}

// TestHTTPSProxyTunnelsCONNECT 验证 HTTPS 代理真实可用: 客户端先与代理
// 完成 TLS 握手, 再发 CONNECT, 隧道内数据能双向透传。
func TestHTTPSProxyTunnelsCONNECT(t *testing.T) {
	// 回显服务器作为隧道目标。
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() }(conn)
		}
	}()

	var (
		mu       sync.Mutex
		connects []string
	)
	proxySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		connects = append(connects, r.Host)
		mu.Unlock()

		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("代理测试服务器不支持 Hijack")
			return
		}
		clientConn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer clientConn.Close()
		upstream, err := net.Dial("tcp", echoLn.Addr().String())
		if err != nil {
			_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		defer upstream.Close()
		_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, clientConn) }()
		_, _ = io.Copy(clientConn, upstream)
	}))
	defer proxySrv.Close()

	d, err := newProxyDialerWithTLS("https://"+proxySrv.Listener.Addr().String(), 2*time.Second, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.Dial("tcp", "imap.example.test:993")
	if err != nil {
		t.Fatalf("经 HTTPS 代理建立隧道失败: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("写入隧道失败: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取隧道失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("隧道回显 = %q, want ping", buf)
	}

	mu.Lock()
	got := append([]string(nil), connects...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "imap.example.test:993" {
		t.Fatalf("代理应收到 1 次 CONNECT imap.example.test:993, 实际 %v", got)
	}
}

// TestDialIMAPWithoutProxyConnectsDirectly 验证真实直连分支: 未配置代理时
// dialIMAP 直接 TLS 连接服务器, 不会套用任何默认代理。
func TestDialIMAPWithoutProxyConnectsDirectly(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})

	prevTLS := imapTLSConfig
	imapTLSConfig = &tls.Config{InsecureSkipVerify: true} // 仅测试: 进程内假服务器为自签证书
	t.Cleanup(func() { imapTLSConfig = prevTLS })

	c, err := dialIMAP(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)), "")
	if err != nil {
		t.Fatalf("无代理直连应成功: %v", err)
	}
	_ = c.Logout()
}

// TestConnectWithoutProxyDialsDirect 验证未配置代理时仍直连(回归):
// 拨号层收到的 proxy 必须为空, 不能凭空套用某个代理。
func TestConnectWithoutProxyDialsDirect(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})

	var (
		calls    int
		gotProxy string
	)
	restore := OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		calls++
		gotProxy = proxy
		return mailtest.DialInsecure(addr)
	})
	t.Cleanup(restore)

	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("无代理连接应成功: %v", err)
	}
	defer c.Disconnect()

	if calls != 1 {
		t.Fatalf("应拨号 1 次, 实际 %d", calls)
	}
	if gotProxy != "" {
		t.Fatalf("无代理账号的拨号 proxy 应为空, 实际 %q", gotProxy)
	}
}

// TestConnectRejectsUnsupportedProxyScheme 验证不支持的代理协议在拨号前
// 就被拒绝(错误信息不包含原始 URL, 避免泄露凭据)。
func TestConnectRejectsUnsupportedProxyScheme(t *testing.T) {
	const proxyWithSecret = "ftp://user:proxy-secret@127.0.0.1:21"
	c := NewClientWithServer("u@163.com", "auth-code", "imap.example.test", 993).WithProxy(proxyWithSecret)
	err := c.Connect()
	if err == nil {
		t.Fatal("不支持的代理协议应报错")
	}
	if !strings.Contains(err.Error(), "不支持的代理协议") {
		t.Fatalf("错误应说明协议不支持, 实际: %v", err)
	}
	if strings.Contains(err.Error(), "proxy-secret") {
		t.Fatalf("错误信息不得泄露代理凭据: %v", err)
	}
}

// TestPoolSeparatesConnectionsByProxy 验证连接池按代理隔离连接:
// 同一账号换代理后不得复用旧出口的连接, 否则新代理配置不生效,
// 流量仍从旧 IP 出去(风控场景下等于代理没换)。
func TestPoolSeparatesConnectionsByProxy(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})

	var (
		mu     sync.Mutex
		dialed []string
	)
	restore := OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		mu.Lock()
		dialed = append(dialed, proxy)
		mu.Unlock()
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	})
	t.Cleanup(restore)

	pool := NewPool()
	t.Cleanup(pool.Close)

	const (
		proxyA = "socks5://127.0.0.1:1080"
		proxyB = "socks5://127.0.0.1:1081"
	)
	noop := func(*Client) error { return nil }
	for _, proxy := range []string{proxyA, proxyA, proxyB} {
		if err := pool.Do("u@163.com", "auth-code", proxy, noop); err != nil {
			t.Fatalf("pool.Do(proxy=%s): %v", proxy, err)
		}
	}

	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()
	if len(got) != 2 || got[0] != proxyA || got[1] != proxyB {
		t.Fatalf("同一代理应复用连接、换代理应新建连接, 期望 [%s %s], 实际 %v", proxyA, proxyB, got)
	}
}

// TestPoolClosedConnDoesNotReconnect 验证已关闭的池项不会再建连:
// Close 之后仍持有 pc 指针的 Do 必须换新池项, 不能在旧出口上重建孤儿连接。
func TestPoolClosedConnDoesNotReconnect(t *testing.T) {
	pc := &pooledConn{
		closed: true,
		newClient: func() *Client {
			t.Fatal("已关闭的池项不得再调用 newClient")
			return nil
		},
	}
	if err := pc.ensure(time.Minute, time.Minute); err == nil {
		t.Fatal("已关闭的池项 ensure 应返回错误")
	}
}

// TestPoolUnlocksOnFnPanic 验证 fn panic 时不会把 pc.mu 永久锁住:
// 否则该账号后续所有读信都会阻塞(handler 由 net/http 恢复 panic 后进程仍在)。
func TestPoolUnlocksOnFnPanic(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	}))
	pool := NewPool()
	t.Cleanup(pool.Close)

	func() {
		defer func() { _ = recover() }()
		_ = pool.Do("a@icloud.com", "code", "", func(*Client) error {
			panic("boom")
		})
	}()

	// panic 后协议状态未知: 连接必须被掐断, 不能留给下一次 Do 复用。
	deadline := time.Now().Add(2 * time.Second)
	for srv.ActiveConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.ActiveConns(); got != 0 {
		t.Fatalf("fn panic 后旧连接应已断开, 服务端仍有 %d 条活跃连接", got)
	}

	done := make(chan error, 1)
	go func() {
		done <- pool.Do("a@icloud.com", "code", "", func(*Client) error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("panic 后第二次 Do 应成功: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fn panic 后 pc.mu 未释放, 后续 Do 永久阻塞")
	}
}

// TestPoolQueuedDoAbortsAfterClose 验证已排队等 pc.mu 的 Do 在池关闭后
// 立即放弃, 不会用旧连接发起新的 TLS+LOGIN。
func TestPoolQueuedDoAbortsAfterClose(t *testing.T) {
	pool := NewPool()
	t.Cleanup(pool.Close)

	const (
		prefix = "a@icloud.com"
		proxy  = "socks5://127.0.0.1:1080"
	)
	var dials int
	pc := &pooledConn{
		key:      prefix + "\x00" + proxy,
		password: "code",
		newClient: func() *Client {
			dials++
			return NewClient(prefix, "code").WithProxy(proxy)
		},
	}
	pool.mu.Lock()
	pool.items[pc.key] = pc
	pool.mu.Unlock()

	pc.mu.Lock() // 模拟一个在途读持有连接
	done := make(chan error, 1)
	go func() {
		done <- pool.Do(prefix, "code", proxy, func(*Client) error { return nil })
	}()
	time.Sleep(50 * time.Millisecond) // 让 Do 排到 pc.mu 上
	pool.Close()
	pc.mu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, ErrPoolClosed) {
			t.Fatalf("排队的 Do 应返回 ErrPoolClosed, 实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("排队的 Do 未返回")
	}
	if dials != 0 {
		t.Fatalf("关闭后不得再拨号, 实际拨号 %d 次", dials)
	}
	if got := pool.Len(); got != 0 {
		t.Fatalf("旧连接应已移除, 池中剩余 %d", got)
	}
}

// TestEnsureDoesNotConnectRetired 验证 retired 池项不会再发起 Connect。
func TestEnsureDoesNotConnectRetired(t *testing.T) {
	var dials int
	pc := &pooledConn{
		newClient: func() *Client {
			dials++
			return NewClient("a@icloud.com", "code")
		},
	}
	pc.retired.Store(true)
	if err := pc.ensure(time.Minute, time.Minute); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("retired 池项 ensure 应返回 ErrPoolClosed, 实际 %v", err)
	}
	if dials != 0 {
		t.Fatalf("retired 池项不得拨号, 实际 %d", dials)
	}
}

func TestPoolCloseIsBarrier(t *testing.T) {
	pool := NewPool()
	var dials int
	restore := OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		dials++
		return nil, errors.New("不应拨号")
	})
	t.Cleanup(restore)
	pool.Close()

	if err := pool.Do("a@icloud.com", "code", "", func(*Client) error { return nil }); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Close 后 Do 应返回 ErrPoolClosed, 实际 %v", err)
	}
	if dials != 0 {
		t.Fatalf("Close 后不得再拨号, 实际 %d", dials)
	}
}

// TestPoolSeparatesExternalMailboxConnectionsByProxy 验证外部收件邮箱路径
// (DoWithServer)同样按代理隔离: 该路径有独立的 proxy 参数, 漏传会让
// 外部邮箱连接复用旧出口。
func TestPoolSeparatesExternalMailboxConnectionsByProxy(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})

	var (
		mu     sync.Mutex
		dialed []string
	)
	restore := OverrideDialForTestWithProxy(func(addr, proxy string) (*client.Client, error) {
		mu.Lock()
		dialed = append(dialed, proxy)
		mu.Unlock()
		return mailtest.DialInsecure(net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
	})
	t.Cleanup(restore)

	pool := NewPool()
	t.Cleanup(pool.Close)

	const (
		proxyA = "socks5://127.0.0.1:1080"
		proxyB = "socks5://127.0.0.1:1081"
	)
	noop := func(*Client) error { return nil }
	for _, proxy := range []string{proxyA, proxyA, proxyB} {
		if err := pool.DoWithServer("ext:163:993:u@163.com", "u@163.com", "auth-code", srv.Host, srv.Port, proxy, noop); err != nil {
			t.Fatalf("pool.DoWithServer(proxy=%s): %v", proxy, err)
		}
	}

	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()
	if len(got) != 2 || got[0] != proxyA || got[1] != proxyB {
		t.Fatalf("外部邮箱换代理应新建连接, 期望 [%s %s], 实际 %v", proxyA, proxyB, got)
	}
}
