package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"icloud-hme/internal/apikey"
)

// ====================================================================
// API 秘钥鉴权: Bearer/X-API-Key 替代会话 Cookie, 按权限放行
// ====================================================================
//
// 契约:
//  1. 带 X-API-Key 或 Authorization: Bearer 的请求走秘钥鉴权, 无需 CSRF;
//  2. 秘钥对每个接口独立授权(权限 ID 见 permissionCatalog);
//  3. 无效/停用秘钥 401 INVALID_API_KEY; 权限不足 403 PERMISSION_DENIED;
//  4. 秘钥管理接口(/api/keys、/api/permissions)仅限管理员会话
//     (秘钥不得管理秘钥或给自己提权);
//  5. 会话鉴权行为完全不变(仍强制 CSRF)。
//
// 断言口径: 有权限的请求「不出现 401/403」即视为通过鉴权
// (下游 handler 可能因参数/业务返回其它状态码, 与鉴权无关)。

// createTestKey 直接在 Server 的秘钥管理器里建一个秘钥(绕过 HTTP, 聚焦鉴权面)。
func createTestKey(t *testing.T, s *Server, perms ...string) apikey.Key {
	t.Helper()
	rec, err := s.keys.Create(apikey.CreateInput{Name: "测试", Permissions: perms})
	if err != nil {
		t.Fatalf("创建测试秘钥: %v", err)
	}
	return rec
}

// keyReq 构造带 X-API-Key 的请求。
func keyReq(t *testing.T, ts serverURL, method, path, key, body string) *http.Request {
	t.Helper()
	req, _ := http.NewRequest(method, string(ts)+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-API-Key", key)
	return req
}

type serverURL string

// 秘钥可代替会话: 有权限的 GET 请求成功。
func TestAPIKeyAllowsWithPermission(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "inbox.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/inbox?account_id=acc_1", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 {
		t.Fatalf("有权限的请求不应被拒绝: %d %s", code, raw)
	}
}

// 秘钥缺少所需权限: 403 PERMISSION_DENIED。
func TestAPIKeyDeniesWithoutPermission(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "inbox.list") // 只有 inbox.list

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/accounts", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 403 {
		t.Fatalf("无权限应 403, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "PERMISSION_DENIED") {
		t.Fatalf("错误码应为 PERMISSION_DENIED: %s", raw)
	}
}

// 空权限秘钥: 所有受保护接口一律 403(失败关闭)。
func TestAPIKeyEmptyPermissionsDeniesAll(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s) // 无权限

	for _, route := range protectedRouteList {
		path := "/api" + strings.NewReplacer(":id", "acc_1", ":message_id", "1").Replace(route.Path)
		req := keyReq(t, serverURL(ts.URL), route.Method, path, key.Key, "{}")
		code, raw, _ := do(t, req)
		if code != 403 {
			t.Fatalf("%s %s: 空权限秘钥应 403, got %d %s", route.Method, path, code, raw)
		}
	}
}

// 无效秘钥与停用秘钥: 401 INVALID_API_KEY(不泄露秘钥是否存在)。
func TestAPIKeyInvalidAndDisabledRejected(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)

	// 未知秘钥
	req := keyReq(t, serverURL(ts.URL), "GET", "/api/accounts", "sk-not-a-real-key-000000", "")
	code, raw, _ := do(t, req)
	if code != 401 || !strings.Contains(raw, "INVALID_API_KEY") {
		t.Fatalf("未知秘钥应 401 INVALID_API_KEY, got %d %s", code, raw)
	}

	// 停用秘钥
	key := createTestKey(t, s, "accounts.list")
	disabled := false
	if _, err := s.keys.Update(key.ID, apikey.UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	req = keyReq(t, serverURL(ts.URL), "GET", "/api/accounts", key.Key, "")
	code, raw, _ = do(t, req)
	if code != 401 || !strings.Contains(raw, "INVALID_API_KEY") {
		t.Fatalf("停用秘钥应 401 INVALID_API_KEY, got %d %s", code, raw)
	}
}

// Authorization: Bearer 形式同样可用(标准脚本写法)。
func TestAPIKeyBearerHeaderAccepted(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "accounts.list")

	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 {
		t.Fatalf("Bearer 形式应通过鉴权: %d %s", code, raw)
	}
}

// Authorization 的 scheme 大小写不敏感(RFC 7235: auth-scheme 大小写不敏感):
// 各类 HTTP 客户端/脚本可能发出 "bearer"/"BEARER" 前缀, 必须同样接受。
func TestAPIKeyBearerSchemeCaseInsensitive(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "accounts.list")

	for _, scheme := range []string{"bearer ", "BEARER ", "BeArEr "} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
		req.Header.Set("Authorization", scheme+key.Key)
		code, raw, _ := do(t, req)
		if code == 401 || code == 403 {
			t.Fatalf("scheme %q 应通过鉴权: %d %s", scheme, code, raw)
		}
	}
}

// 秘钥鉴权跳过 CSRF: 非 GET 请求不带 X-CSRF-Token 也应成功。
func TestAPIKeySkipsCSRF(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "inbox.previews")

	req := keyReq(t, serverURL(ts.URL), "POST", "/api/inbox/previews?account_id=acc_1", key.Key, `{"ids":["1"]}`)
	code, raw, _ := do(t, req)
	if code == 403 && strings.Contains(raw, "CSRF") {
		t.Fatalf("秘钥请求不应触发 CSRF 校验: %d %s", code, raw)
	}
	if code == 401 {
		t.Fatalf("秘钥请求不应 401: %s", raw)
	}
}

// 会话鉴权保持原样: 非 GET 无 CSRF 仍被拒(秘钥不改变会话路径的安全性)。
func TestSessionStillRequiresCSRF(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	cookie, _ := login(t, ts, "admin-pass-2026-strong")

	req := authedReq(t, ts, "POST", "/api/inbox/previews?account_id=acc_1", `{"ids":["1"]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	// 故意不带 CSRF
	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "CSRF_INVALID") {
		t.Fatalf("会话请求缺 CSRF 应 403 CSRF_INVALID, got %d %s", code, raw)
	}
}

// 秘钥管理接口仅限会话: 秘钥访问 /api/keys 与 /api/permissions 一律 403。
func TestKeyManagementSessionOnly(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	// 给足权限(理论上也没有 keys 权限可给): 仍然拒绝。
	key := createTestKey(t, s, "accounts.list", "inbox.list", "system.reload")

	cases := []struct {
		method, path, body string
	}{
		{"GET", "/api/keys", ""},
		{"POST", "/api/keys", `{"name":"x"}`},
		{"GET", "/api/permissions", ""},
		{"DELETE", "/api/keys/key_1", ""},
	}
	for _, tc := range cases {
		req := keyReq(t, serverURL(ts.URL), tc.method, tc.path, key.Key, tc.body)
		code, raw, _ := do(t, req)
		if code != 403 {
			t.Fatalf("%s %s: 秘钥访问应 403, got %d %s", tc.method, tc.path, code, raw)
		}
	}
}

// 每个受保护路由: 全权限秘钥通过鉴权(无 401/403), 空权限秘钥 403。
// 同时验证鉴权发生在 handler 之前(参数错误也不影响 403 判定)。
func TestAllProtectedRoutesEnforcePermissions(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)

	allPerms := make([]string, 0, len(permissionCatalog))
	for _, p := range permissionCatalog {
		allPerms = append(allPerms, p.ID)
	}
	fullKey := createTestKey(t, s, allPerms...)
	emptyKey := createTestKey(t, s)

	for _, route := range protectedRouteList {
		path := "/api" + strings.NewReplacer(":id", "acc_1", ":message_id", "1").Replace(route.Path)

		// 全权限: 不出现 401/403。
		req := keyReq(t, serverURL(ts.URL), route.Method, path, fullKey.Key, "{}")
		code, raw, _ := do(t, req)
		if code == 401 || code == 403 {
			t.Fatalf("全权限秘钥 %s %s 被拒绝: %d %s", route.Method, path, code, raw)
		}

		// 空权限: 403。
		req = keyReq(t, serverURL(ts.URL), route.Method, path, emptyKey.Key, "{}")
		code, raw, _ = do(t, req)
		if code != 403 {
			t.Fatalf("空权限秘钥 %s %s 应 403, got %d %s", route.Method, path, code, raw)
		}
	}
}

// 路由权限表与 gin 实际注册的路由完全一致(双向):
// 新增路由忘记声明权限、或删除路由留下死条目, 都会在这里失败。
func TestRoutePermissionTableComplete(t *testing.T) {
	f := &fakeBackend{}
	s, _ := newTestServer(t, f)

	registered := map[string]bool{}
	for _, r := range s.r.Routes() {
		if !strings.HasPrefix(r.Path, "/api/") {
			continue
		}
		registered[r.Method+" "+r.Path] = true
	}

	expected := map[string]bool{}
	// 公开路由
	for _, r := range []string{"POST /api/auth/login", "GET /api/auth/session"} {
		expected[r] = true
	}
	// 仅会话路由
	for _, r := range sessionOnlyRoutes {
		expected[r] = true
	}
	// 权限路由
	for _, route := range protectedRouteList {
		expected[route.Method+" /api"+route.Path] = true
	}

	for r := range registered {
		if !expected[r] {
			t.Errorf("路由 %s 未在权限表中声明(新增路由必须声明权限或列入 sessionOnlyRoutes)", r)
		}
	}
	for r := range expected {
		if !registered[r] {
			t.Errorf("权限表声明了 %s, 但 gin 未注册该路由(死条目)", r)
		}
	}
}

// 权限目录自身完整性: ID 唯一、分组有效、全部路由权限均在目录中。
func TestPermissionCatalogIntegrity(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range permissionCatalog {
		if p.ID == "" || p.Label == "" || p.Group == "" {
			t.Fatalf("权限条目字段缺失: %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("权限 ID 重复: %s", p.ID)
		}
		seen[p.ID] = true
	}
	validGroups := map[string]bool{}
	for _, g := range permissionGroups {
		validGroups[g.ID] = true
	}
	for _, p := range permissionCatalog {
		if !validGroups[p.Group] {
			t.Fatalf("权限 %s 的分组 %s 不在分组表中", p.ID, p.Group)
		}
	}
	for _, route := range protectedRouteList {
		if !seen[route.Permission] {
			t.Fatalf("路由 %s %s 引用了目录外的权限 %s", route.Method, route.Path, route.Permission)
		}
	}
}

// JSON 序列化: 权限目录含 id/label/group 字段(前端契约)。
func TestPermissionCatalogJSONShape(t *testing.T) {
	raw, err := json.Marshal(permissionCatalog[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"id"`, `"label"`, `"group"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("序列化缺少 %s: %s", field, raw)
		}
	}
}

// ====================================================================
// 账号使用范围: 秘钥限定可访问账号
// ====================================================================
//
// 契约:
//  6. 秘钥可设账号范围(--account-ids); 空范围 = 全部账号;
//  7. 请求的目标账号不在范围内 → 403 ACCOUNT_NOT_ALLOWED(失败关闭);
//  8. 范围校验早于业务逻辑: 越权账号的请求不得到达 handler(不泄露账号是否存在);
//  9. 从 query/body/path 三处提取目标账号 ID。

// createTestKeyScoped 建一个带账号范围的秘钥。
func createTestKeyScoped(t *testing.T, s *Server, accountIDs []string, perms ...string) apikey.Key {
	t.Helper()
	rec, err := s.keys.Create(apikey.CreateInput{
		Name:        "范围测试",
		Permissions: perms,
		AccountIDs:  accountIDs,
	})
	if err != nil {
		t.Fatalf("创建范围秘钥: %v", err)
	}
	return rec
}

// 白名单内账号放行(query account_id)。
func TestAPIKeyAccountScopeAllowsListedAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/inbox?account_id=acc_1", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 {
		t.Fatalf("白名单内账号应放行: %d %s", code, raw)
	}
}

// 白名单外账号拒绝(403 ACCOUNT_NOT_ALLOWED), 且不得到达 handler。
func TestAPIKeyAccountScopeDeniesUnlistedAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "inbox.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/inbox?account_id=acc_2", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 403 {
		t.Fatalf("范围外账号应 403, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("错误码应为 ACCOUNT_NOT_ALLOWED: %s", raw)
	}
	if f.listInboxQuery.AccountID != "" {
		t.Fatalf("越权请求不应到达 handler, listInboxQuery=%+v", f.listInboxQuery)
	}
}

// 空范围 = 全部账号放行(向后兼容旧秘钥)。
func TestAPIKeyEmptyAccountScopeAllowsAny(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "inbox.list") // 无范围

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/inbox?account_id=acc_any", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 {
		t.Fatalf("空范围应放行任意账号: %d %s", code, raw)
	}
}

// body account_id 也被范围校验(POST /create)。
func TestAPIKeyAccountScopeChecksBodyAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.create")

	body := `{"account_id":"acc_9","label":"x"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/create", key.Key, body)
	code, raw, _ := do(t, req)
	if code != 403 {
		t.Fatalf("body 中范围外账号应 403, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("错误码应为 ACCOUNT_NOT_ALLOWED: %s", raw)
	}
}

// 路径 :id(账号路由)也纳入范围校验(PATCH /accounts/:id)。
func TestAPIKeyAccountScopeChecksPathAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "accounts.write")

	req := keyReq(t, serverURL(ts.URL), "PATCH", "/api/accounts/acc_2", key.Key, `{"label":"y"}`)
	code, raw, _ := do(t, req)
	if code != 403 {
		t.Fatalf("路径中范围外账号应 403, got %d %s", code, raw)
	}
	if !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("错误码应为 ACCOUNT_NOT_ALLOWED: %s", raw)
	}
}

// ====================================================================
// 空秘钥头: 存在但为空 → 视为使用秘钥(失败关闭), 不回退会话
// ====================================================================
//
// 契约: X-API-Key 头「存在但为空」时不得静默降级为会话调用。
// 理由: 调用方意图用秘钥(带了头), 空值应报错而非悄悄走 Cookie 路径——
// 否则「脚本带空秘钥 + 浏览器恰好有会话」会通过, 削弱失败关闭语义。

// 空 X-API-Key 头 + 有效会话 → 401(不回退会话)。
func TestAPIKeyEmptyHeaderDoesNotFallbackToSession(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	_ = s
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.Header.Set("X-API-Key", "") // 存在但为空
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code != 401 {
		t.Fatalf("空秘钥头应 401(不回退会话), got %d %s", code, raw)
	}
	if !strings.Contains(raw, "INVALID_API_KEY") {
		t.Fatalf("错误码应为 INVALID_API_KEY: %s", raw)
	}
}

// 纯空格 X-API-Key 头同样不回退。
func TestAPIKeyWhitespaceHeaderDoesNotFallback(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.Header.Set("X-API-Key", "   ")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, _, _ := do(t, req)
	if code != 401 {
		t.Fatalf("空格秘钥头应 401, got %d", code)
	}
}

// 空 Authorization: Bearer 头同样不回退。
func TestAPIKeyEmptyBearerDoesNotFallback(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer ")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, _, _ := do(t, req)
	if code != 401 {
		t.Fatalf("空 Bearer 应 401, got %d", code)
	}
}

// 无秘钥头时仍正常回退会话(不误伤浏览器调用)。
func TestNoKeyHeaderStillUsesSession(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("GET", ts.URL+"/api/accounts", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code == 401 {
		t.Fatalf("无秘钥头应正常回退会话, got %d %s", code, raw)
	}
}

// ====================================================================
// N1: Content-Type 不得成为账号范围校验的旁路
// ====================================================================
//
// 契约: 无论 Content-Type 是什么, 只要请求体能被解析出 account_id,
// 就必须做账号范围校验。gin 的 ShouldBindJSON 忽略 Content-Type 照常
// 绑定 JSON, 若中间件的提取依赖 Content-Type, 攻击者可用 text/plain
// 携带 JSON body 绕过范围校验(实测 403->200 越权)。

// text/plain + JSON body 不得绕过范围校验。
func TestAPIKeyScopeIgnoresContentTypeForBodyAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.write")

	body := `{"account_id":"acc_2","action":"delete","aliases":[{"anonymous_id":"a1","email":"x@y.com"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/aliases/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain") // 伪装类型
	req.Header.Set("X-API-Key", key.Key)

	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("text/plain 不得绕过范围校验, got %d %s", code, raw)
	}
}

// 无 Content-Type 头 + JSON body 同样不得绕过。
func TestAPIKeyScopeIgnoresMissingContentType(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.write")

	body := `{"account_id":"acc_2","action":"delete","aliases":[{"anonymous_id":"a1","email":"x@y.com"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/aliases/batch", strings.NewReader(body))
	req.Header.Set("X-API-Key", key.Key) // 不设 Content-Type

	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("缺 Content-Type 不得绕过范围校验, got %d %s", code, raw)
	}
}
