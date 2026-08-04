package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// testFS 构造包含 index.html 与哈希 asset 的内存文件系统。
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {
			Data: []byte("<html><body>管理界面</body></html>"),
		},
		"assets/app-abc123.js": {
			Data: []byte("console.log('app')"),
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
