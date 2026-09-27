package hme

import (
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequestDoesNotDuplicateCookies 回归测试:业务请求(含多次请求、服务端 Set-Cookie 刷新)
// 只能发送一份 Cookie。
//
// 历史 bug:NewClient 既手动设置 Cookie 头,又给底层 tls-client 配了 cookie jar。
// fhttp 在 Jar 非空时会把 jar 里的 cookie 逐个 AddCookie 追加到同一个 Cookie 头后面,
// 而 jar 又会被每次响应的 Set-Cookie 填充。于是第 2 次起请求的 Cookie 头 = 手动一份 + jar 一份,
// 真实账号(22 个 cookie / 4480B)翻倍后触发 Apple 边缘的
// "400 Request Header Or Cookie Too Large"。
func TestRequestDoesNotDuplicateCookies(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		gotCookie = r.Header.Get("Cookie")
		// 每次响应都下发 Set-Cookie,模拟 iCloud 刷新 token —— 会污染 jar。
		stdhttp.SetCookie(w, &stdhttp.Cookie{Name: "A", Value: "1"})
		stdhttp.SetCookie(w, &stdhttp.Cookie{Name: "B", Value: "2"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, err := NewClient(map[string]string{"A": "1", "B": "2"}, "icloud.com", "", false)
	if err != nil {
		t.Fatal(err)
	}

	// 连发多次,第 2 次起才会暴露"响应 Set-Cookie 回灌 jar → 下次翻倍"的问题。
	for i := 0; i < 3; i++ {
		if _, err := c.request("GET", srv.URL, nil, 0, 1); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if n := strings.Count(gotCookie, "A="); n != 1 {
			t.Fatalf("请求 %d: cookie A 出现 %d 次(期望 1),Cookie 头 = %q", i, n, gotCookie)
		}
		if n := strings.Count(gotCookie, "B="); n != 1 {
			t.Fatalf("请求 %d: cookie B 出现 %d 次(期望 1),Cookie 头 = %q", i, n, gotCookie)
		}
	}
}
