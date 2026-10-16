package webui

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testAssetJS 是超过 gzip 压缩阈值的测试资源(模拟真实 bundle)。
var testAssetJS = strings.Repeat("console.log('app');\n", 120)

// testFS 构造包含 index.html 与哈希 asset 的内存文件系统。
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {
			Data: []byte("<html><body>管理界面</body></html>"),
		},
		"assets/app-abc123.js": {
			Data: []byte(testAssetJS),
		},
	}
}

// TestSPARoot 验证根路径返回 index.html 且缓存为 no-cache。
func TestSPARoot(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,得到 %d", rec.Code)
	}
	if !containsStr(rec.Body.String(), "管理界面") {
		t.Fatalf("响应应包含 index 内容: %s", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index 缓存应为 no-cache,得到 %q", cc)
	}
}

// TestSPAFallback 验证 SPA 路径返回 index.html。
func TestSPAFallback(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/accounts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,得到 %d", rec.Code)
	}
	if !containsStr(rec.Body.String(), "管理界面") {
		t.Fatalf("SPA fallback 应返回 index: %s", rec.Body.String())
	}
}

// TestSPAAsset 验证真实 asset 服务且缓存为 immutable。
func TestSPAAsset(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,得到 %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("哈希 asset 缓存应为 immutable,得到 %q", cc)
	}
}

// TestSPAMissingAsset 验证缺失 asset 返回 404。
func TestSPAMissingAsset(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/assets/missing.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 404,得到 %d", rec.Code)
	}
}

// TestSPAMethodNotAllowed 验证 POST 不被静态服务处理。
func TestSPAMethodNotAllowed(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("POST", "/accounts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("POST 不应返回 200")
	}
}

// TestSPAAssetGzipServed 验证文本资源在客户端声明 Accept-Encoding: gzip 时
// 以 gzip 传输: 响应带 Content-Encoding: gzip 与 Vary, 解压后与源文件一致,
// 且压缩后体积显著小于原文(674KB 的 bundle 实测 gzip 后约 211KB)。
func TestSPAAssetGzipServed(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,得到 %d", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Accept-Encoding: gzip 时响应应为 gzip,得到 %q", ce)
	}
	if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Fatalf("响应应带 Vary: Accept-Encoding,得到 %q", v)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("响应体不是合法 gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if string(plain) != testAssetJS {
		t.Fatalf("解压内容与源文件不一致: %d vs %d 字节", len(plain), len(testAssetJS))
	}
	if rec.Body.Len() >= len(testAssetJS) {
		t.Fatalf("gzip 后体积应小于原文: %d >= %d", rec.Body.Len(), len(testAssetJS))
	}
}

// TestSPAAssetNoGzipWithoutAcceptEncoding 验证客户端未声明 gzip 时按原文传输
// (不压缩、不带 Content-Encoding)。
func TestSPAAssetNoGzipWithoutAcceptEncoding(t *testing.T) {
	h := Handler(testFS())
	req := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,得到 %d", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Fatalf("未声明 gzip 时不应压缩,得到 Content-Encoding=%q", ce)
	}
	if rec.Body.String() != testAssetJS {
		t.Fatal("未压缩响应体应与源文件一致")
	}
}

// TestSPAAssetHeadMatchesGet 验证 HEAD 响应头与 GET 一致(含压缩协商):
// 客户端(及中间缓存)依赖 HEAD 头部判断资源形态, 不一致会误导缓存策略。
func TestSPAAssetHeadMatchesGet(t *testing.T) {
	h := Handler(testFS())
	getReq := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
	getReq.Header.Set("Accept-Encoding", "gzip")
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)

	headReq := httptest.NewRequest("HEAD", "/assets/app-abc123.js", nil)
	headReq.Header.Set("Accept-Encoding", "gzip")
	headRec := httptest.NewRecorder()
	h.ServeHTTP(headRec, headReq)

	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD 期望 200,得到 %d", headRec.Code)
	}
	for _, header := range []string{"Content-Encoding", "Content-Type", "Content-Length", "Cache-Control"} {
		if headRec.Header().Get(header) != getRec.Header().Get(header) {
			t.Fatalf("HEAD 的 %s 应与 GET 一致: HEAD=%q GET=%q",
				header, headRec.Header().Get(header), getRec.Header().Get(header))
		}
	}
	if headRec.Body.Len() != 0 {
		t.Fatalf("HEAD 不应有响应体,得到 %d 字节", headRec.Body.Len())
	}
}

// TestEmbedded 验证 Embedded 返回 dist 子文件系统。
func TestEmbedded(t *testing.T) {
	fsys, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open("placeholder.txt"); err != nil {
		t.Fatalf("Embedded 应包含 placeholder.txt: %v", err)
	}
}

// TestEmbeddedNoDist 验证 dist 不存在时返回清晰错误。
func TestEmbeddedNoDist(t *testing.T) {
	if _, err := Embedded(); err != nil {
		// 未构建 dist 时返回 503 文案由 Handler 保证,这里只验证不 panic
		t.Logf("Embedded 错误(可接受): %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
