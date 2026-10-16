package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"icloud-hme/internal/apikey"
)

// ====================================================================
// 秘钥管理 HTTP 接口(仅管理员会话)
// ====================================================================
//
//	GET    /api/keys           列出全部秘钥
//	POST   /api/keys           创建(可指定权限; key 留空自动生成)
//	PATCH  /api/keys/:id       更新名称/权限/启用状态
//	DELETE /api/keys/:id       删除
//	GET    /api/permissions    权限目录(供前端渲染分组勾选)
//
// 全部接口仅限会话 + CSRF(秘钥不可访问, 见 apikey_auth_test.go)。

// sessionReq 构造带会话与 CSRF 的请求(管理接口专用)。
func sessionReq(t *testing.T, ts serverURL, session, csrf, method, path, body string) *http.Request {
	t.Helper()
	req, _ := http.NewRequest(method, string(ts)+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	return req
}

// 创建 → 列表 → 更新 → 删除 的完整生命周期。
func TestKeysCRUDLifecycle(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	// 创建(自动生成秘钥)
	req := sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys",
		`{"name":"脚本A","permissions":["inbox.list","inbox.previews"]}`)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, raw)
	}
	var created struct {
		Data apikey.Key `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Data.Key, "sk-") || len(created.Data.Key) != 3+24 {
		t.Fatalf("自动秘钥格式错误: %q", created.Data.Key)
	}
	if created.Data.Name != "脚本A" || len(created.Data.Permissions) != 2 {
		t.Fatalf("创建结果不符: %+v", created.Data)
	}

	// 列表包含
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "GET", "/api/keys", "")
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("列表失败: %d %s", code, raw)
	}
	var list struct {
		Data struct {
			Keys []apikey.Key `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Keys) != 1 || list.Data.Keys[0].ID != created.Data.ID {
		t.Fatalf("列表不符: %+v", list.Data.Keys)
	}

	// 更新: 名称 + 权限 + 停用
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "PATCH", "/api/keys/"+created.Data.ID,
		`{"name":"脚本B","permissions":["inbox.list"],"enabled":false}`)
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("更新失败: %d %s", code, raw)
	}
	var updated struct {
		Data apikey.Key `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Data.Name != "脚本B" || updated.Data.Enabled || len(updated.Data.Permissions) != 1 {
		t.Fatalf("更新结果不符: %+v", updated.Data)
	}

	// 删除
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "DELETE", "/api/keys/"+created.Data.ID, "")
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("删除失败: %d %s", code, raw)
	}

	req = sessionReq(t, serverURL(ts.URL), session, csrf, "GET", "/api/keys", "")
	_, raw, _ = do(t, req)
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Keys) != 0 {
		t.Fatalf("删除后列表应为空: %+v", list.Data.Keys)
	}
}

// 创建支持手动秘钥; 重复与非法值分别 409/400。
func TestKeysCreateCustomAndValidation(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req := sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys",
		`{"name":"手动","key":"my-custom-key-2026"}`)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("手动秘钥创建失败: %d %s", code, raw)
	}
	var created struct {
		Data apikey.Key `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &created)
	if created.Data.Key != "my-custom-key-2026" {
		t.Fatalf("手动秘钥应原样保存: %q", created.Data.Key)
	}

	// 重复 → 409 KEY_EXISTS
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys",
		`{"name":"重复","key":"my-custom-key-2026"}`)
	code, raw, _ = do(t, req)
	if code != 409 || !strings.Contains(raw, "KEY_EXISTS") {
		t.Fatalf("重复应 409 KEY_EXISTS, got %d %s", code, raw)
	}

	// 非法(过短) → 400
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys",
		`{"name":"短","key":"abc"}`)
	code, raw, _ = do(t, req)
	if code != 400 {
		t.Fatalf("非法秘钥应 400, got %d %s", code, raw)
	}
}

// 创建时未知权限 ID 必须拒绝(防止笔误产生永不生效的权限)。
func TestKeysCreateRejectsUnknownPermission(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req := sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys",
		`{"name":"坏权限","permissions":["inbox.list","not.a.real.permission"]}`)
	code, raw, _ := do(t, req)
	if code != 400 || !strings.Contains(raw, "VALIDATION_ERROR") {
		t.Fatalf("未知权限应 400 VALIDATION_ERROR, got %d %s", code, raw)
	}
}

// 更新不存在/未知权限的校验。
func TestKeysUpdateValidation(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req := sessionReq(t, serverURL(ts.URL), session, csrf, "PATCH", "/api/keys/key_nope",
		`{"name":"x"}`)
	code, raw, _ := do(t, req)
	if code != 404 || !strings.Contains(raw, "KEY_NOT_FOUND") {
		t.Fatalf("不存在应 404 KEY_NOT_FOUND, got %d %s", code, raw)
	}

	// 先建一个再测未知权限
	req = sessionReq(t, serverURL(ts.URL), session, csrf, "POST", "/api/keys", `{"name":"x"}`)
	_, raw, _ = do(t, req)
	var created struct {
		Data apikey.Key `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &created)

	req = sessionReq(t, serverURL(ts.URL), session, csrf, "PATCH", "/api/keys/"+created.Data.ID,
		`{"permissions":["nope.nope"]}`)
	code, raw, _ = do(t, req)
	if code != 400 {
		t.Fatalf("未知权限应 400, got %d %s", code, raw)
	}
}

// 更新为空名称必须拒绝(与创建路径一致: 名称必填, 不允许被清空)。
func TestKeysUpdateRejectsEmptyName(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	key := createTestKey(t, s, "accounts.list")

	for _, body := range []string{`{"name":""}`, `{"name":"   "}`} {
		req := sessionReq(t, serverURL(ts.URL), session, csrf, "PATCH", "/api/keys/"+key.ID, body)
		code, raw, _ := do(t, req)
		if code != 400 || !strings.Contains(raw, "VALIDATION_ERROR") {
			t.Fatalf("空名称(%s)应 400 VALIDATION_ERROR, got %d %s", body, code, raw)
		}
	}
	// 名称未被清空。
	got, ok := s.keys.Get(key.ID)
	if !ok || got.Name != "测试" {
		t.Fatalf("名称应保持原值, got %q", got.Name)
	}
}

// 权限目录接口返回分组与条目。
func TestPermissionsCatalogEndpoint(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req := sessionReq(t, serverURL(ts.URL), session, csrf, "GET", "/api/permissions", "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("权限目录失败: %d %s", code, raw)
	}
	var out struct {
		Data struct {
			Groups []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"groups"`
			Permissions []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
				Group string `json:"group"`
			} `json:"permissions"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Groups) == 0 || len(out.Data.Permissions) == 0 {
		t.Fatalf("目录为空: %+v", out.Data)
	}
	// 目录里每个权限都有中文标签(前端直接展示)。
	for _, p := range out.Data.Permissions {
		if p.Label == "" {
			t.Fatalf("权限 %s 缺 label", p.ID)
		}
	}
}

// 秘钥接口未登录: 401(与会话路径一致)。
func TestKeysRequireSession(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)

	req, _ := http.NewRequest("GET", ts.URL+"/api/keys", nil)
	code, raw, _ := do(t, req)
	if code != 401 || !strings.Contains(raw, "AUTH_REQUIRED") {
		t.Fatalf("未登录应 401 AUTH_REQUIRED, got %d %s", code, raw)
	}
}

// 会话 + 缺 CSRF 的写操作: 403(管理接口不豁免 CSRF)。
func TestKeysWriteRequiresCSRF(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("POST", ts.URL+"/api/keys", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "CSRF_INVALID") {
		t.Fatalf("缺 CSRF 应 403, got %d %s", code, raw)
	}
}
