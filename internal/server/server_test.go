package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"testing/fstest"

	"icloud-hme/internal/webui"
)

// testBundleJS 是超过 gzip 压缩阈值的测试资源(模拟真实 bundle)。
var testBundleJS = strings.Repeat("console.log('app');\n", 120)

// TestSPAAPI404JSON 验证 /api/not-found 返回 JSON 404 而非 HTML。
func TestSPAAPI404JSON(t *testing.T) {
	// 替换 webui 为内存 FS,避免依赖构建产物。
	// 注意: webui handler 在 Server 构造期捕获 webuiFS(见 server.go register),
	// 因此必须先替换、再构造(LE-4 修复后测试替换点的正确用法)。
	prevFS := webuiFS
	webuiFS = fstest.MapFS{
		"index.html": {Data: []byte("<html>管理界面</html>")},
	}
	defer func() { webuiFS = prevFS }()

	f := &fakeBackend{}
	s := newWithBackend(f, Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
	})

	req := httptest.NewRequest("GET", "/api/not-found", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 404,得到 %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), `{"success":false`) {
		t.Fatalf("API 404 应返回 JSON: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("API 404 不应返回 HTML: %s", rec.Body.String())
	}
}

// TestSPAIndexServed 验证根路径由 webui 服务(返回 index 而非 404)。
func TestSPAIndexServed(t *testing.T) {
	// 同 TestSPAAPI404JSON: 先替换 webuiFS, 再构造 Server。
	prevFS := webuiFS
	webuiFS = fstest.MapFS{
		"index.html": {Data: []byte("<html>管理界面</html>")},
	}
	defer func() { webuiFS = prevFS }()

	f := &fakeBackend{}
	s := newWithBackend(f, Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
	})

	req := httptest.NewRequest("GET", "/accounts?account_id=acc_test", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("SPA 路径期望 200,得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "管理界面") {
		t.Fatalf("SPA 路径应返回 index 内容: %s", rec.Body.String())
	}
}

// TestNoRouteReusesSingleWebuiHandler (LE-4) 验证 NoRoute 在构造期只创建一次
// webui handler 并跨请求复用——修复前闭包体内每请求调用 webui.Handler,
// 每请求新建空 gzip 缓存, 压缩结果从不复用(每请求重压整个 bundle)。
//
// 观测点(两层):
//  1. newWebuiHandler 构造计数 = 1(与请求数无关);
//  2. webui 包累计实际压缩次数: 两次 GET 同一可压缩资源只新增 1 次。
func TestNoRouteReusesSingleWebuiHandler(t *testing.T) {
	// 替换 webuiFS(先于构造)与构造钩子(计数)。
	prevFS := webuiFS
	webuiFS = fstest.MapFS{
		"index.html":           {Data: []byte("<html>管理界面</html>")},
		"assets/app-abc123.js": {Data: []byte(testBundleJS)},
	}
	defer func() { webuiFS = prevFS }()

	prevHook := newWebuiHandler
	var built atomic.Int64
	newWebuiHandler = func(fsys fs.FS) http.Handler {
		built.Add(1)
		return prevHook(fsys)
	}
	defer func() { newWebuiHandler = prevHook }()

	f := &fakeBackend{}
	s := newWithBackend(f, Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
	})
	if got := built.Load(); got != 1 {
		t.Fatalf("构造期应只创建 1 次 webui handler, 实际 %d 次", got)
	}

	before := webui.CompressedCallsForTest()
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	rec1 := get()
	after1 := webui.CompressedCallsForTest()
	if rec1.Code != http.StatusOK || rec1.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("第一次请求应 200 + gzip, 得到 %d/%q", rec1.Code, rec1.Header().Get("Content-Encoding"))
	}
	if after1-before != 1 {
		t.Fatalf("第一次请求应压缩 1 次, 实际 %d", after1-before)
	}

	rec2 := get()
	after2 := webui.CompressedCallsForTest()
	if rec2.Code != http.StatusOK || rec2.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("第二次请求应 200 + gzip, 得到 %d/%q", rec2.Code, rec2.Header().Get("Content-Encoding"))
	}
	if after2-after1 != 0 {
		t.Fatalf("第二次请求应命中构造期 handler 的压缩缓存(0 次新压缩), 实际 %d 次——"+
			"说明 NoRoute 每请求重建了 handler(LE-4 缺陷)", after2-after1)
	}
	if built.Load() != 1 {
		t.Fatalf("请求期不应再构造 webui handler, 构造计数 = %d", built.Load())
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatal("两次请求的 gzip 体应一致")
	}
}
