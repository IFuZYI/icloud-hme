package mail

import (
	"net/http"
	"net/http/httptest"
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
	c := NewWebClient(map[string]string{"X-APPLE-WEBAUTH-TOKEN": "t"}, "12345", "icloud.com")
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
