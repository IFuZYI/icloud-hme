package mail

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ====================================================================
// 成功状态码兼容性: Apple 对成功响应不一定返回恰好 200
//
// 回归背景(生产实测): GET /appleauth/auth 返回 HTTP 201 导致 hme 登录
// 误报失败; mail 侧同样对接 Apple(setup validate / mccgateway), 严格
// ==200 判定是同一类缺陷, 这里钉住 201 也必须成功。
// ====================================================================

// newTestWebClient 构造指向测试服务器的 WebClient。
func newTestWebClient(t *testing.T, srvURL string) *WebClient {
	t.Helper()
	c, err := NewWebClient(map[string]string{"X-APPLE-WEBAUTH-TOKEN": "t"}, "12345", "icloud.com")
	if err != nil {
		t.Fatalf("NewWebClient: %v", err)
	}
	c.setupBaseURL = srvURL
	return c
}

// validate 返回 201 时必须成功解析出 mccgateway URL。
func TestResolveMccGatewayAccepts201(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/setup/ws/1/validate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // 201: Apple 实测成功形态
		_, _ = w.Write([]byte(`{"webservices":{"mccgateway":{"url":"https://p217-mccgateway.icloud.com"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestWebClient(t, srv.URL)
	if err := c.resolveMccGateway(); err != nil {
		t.Fatalf("201 是成功状态, resolveMccGateway 不应报错: %v", err)
	}
	if c.mccGatewayURL != "https://p217-mccgateway.icloud.com" {
		t.Fatalf("mccGatewayURL = %q", c.mccGatewayURL)
	}
}

// 邮件搜索返回 201 时必须成功返回邮件列表。
func TestSearchAccepts201(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mailws2/v1/thread/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"totalThreadsReturned":1,"threadList":[{"threadId":"t1","subject":"验证码","senders":["no_reply@email.apple.com"],"preview":"code 123456","timestamp":1700000000000}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestWebClient(t, srv.URL)
	// 直接注入 mccgateway(绕过 validate: resolveMccGateway 会去掉端口号,
	// 随机端口测试服务器经它中转不可达; validate 的 201 已由上一个测试覆盖)。
	c.mccGatewayURL = srv.URL
	msgs, err := c.ListInbox(10)
	if err != nil {
		t.Fatalf("201 是成功状态, ListInbox 不应报错: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "验证码" {
		t.Fatalf("msgs = %+v", msgs)
	}
}

// 非 2xx(如 500)仍必须报错——兼容 2xx 不能放宽失败判定。
func TestResolveMccGatewayRejects500(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/setup/ws/1/validate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestWebClient(t, srv.URL)
	if err := c.resolveMccGateway(); err == nil {
		t.Fatal("500 必须报错")
	}
}

// TestWebClientUsesAccountProxy 验证 Web API 邮件读取也走账号代理:
// IMAP 失败后的 Web 回退若直连, 同样会暴露真实出口 IP, 破坏
// 「同一账号单一出口」的风控前提。
func TestWebClientUsesAccountProxy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/mailws2/v1/thread/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalThreadsReturned":1,"threadList":[{"threadId":"t1","subject":"验证码","senders":["no_reply@email.apple.com"],"preview":"code 123456","timestamp":1700000000000}]}`))
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	target := strings.TrimPrefix(backend.URL, "http://")
	proxy := newFakeHTTPConnectProxy(t, target)

	c, err := NewWebClientWithProxy(map[string]string{"X-APPLE-WEBAUTH-TOKEN": "t"}, "12345", "icloud.com", proxy.URL())
	if err != nil {
		t.Fatalf("NewWebClientWithProxy: %v", err)
	}
	c.mccGatewayURL = backend.URL
	msgs, err := c.ListInbox(10)
	if err != nil {
		t.Fatalf("经代理读取 Web 邮件应成功: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("应返回 1 封邮件, 实际 %d", len(msgs))
	}

	got := proxy.Connects()
	if len(got) != 1 || got[0] != target {
		t.Fatalf("代理应收到 1 次 CONNECT %s, 实际 %v", target, got)
	}
}

// TestNewWebClientWithProxyRejectsInvalidProxy 验证非法代理地址在构造期
// 返回错误, 而不是留下 httpc=nil 的客户端在首次请求时 panic。
func TestNewWebClientWithProxyRejectsInvalidProxy(t *testing.T) {
	if _, err := NewWebClientWithProxy(map[string]string{}, "12345", "icloud.com", "http://[::1"); err == nil {
		t.Fatal("非法代理地址应返回错误")
	}
}

// TestNewWebClientWithProxyInvalidProxyDoesNotLeakCredentials 验证构造错误
// 不回显代理 URL 中的 user:pass(tls-client 原始错误会包含完整 URL)。
func TestNewWebClientWithProxyInvalidProxyDoesNotLeakCredentials(t *testing.T) {
	_, err := NewWebClientWithProxy(map[string]string{}, "12345", "icloud.com", "http://user:secret@[::1")
	if err == nil {
		t.Fatal("非法代理地址应返回错误")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("错误信息泄露代理凭据: %v", err)
	}
}

// TestNewWebClientWithProxyNormalizesPortlessSOCKS5 验证 socks5 省略端口时
// 补 1080: tls-client 只给 http/https 补默认端口, 不归一化会让 Web 回退
// 以 "missing port in address" 失败。
func TestNewWebClientWithProxyNormalizesPortlessSOCKS5(t *testing.T) {
	c, err := NewWebClientWithProxy(map[string]string{}, "12345", "icloud.com", "socks5://proxy.example.com")
	if err != nil {
		t.Fatalf("NewWebClientWithProxy: %v", err)
	}
	if got := c.Proxy(); got != "socks5://proxy.example.com:1080" {
		t.Fatalf("portless socks5 应归一化为 :1080, got %q", got)
	}
}
