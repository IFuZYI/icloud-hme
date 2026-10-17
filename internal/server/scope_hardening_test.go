package server

import (
	"net/http"
	"strings"
	"testing"

	"icloud-hme/internal/hme"
)

// ====================================================================
// 安全加固回归: /reload 全局动作 + 别名归属校验
// ====================================================================

// 范围受限秘钥不得触发全局配置重载(审计 A1)。
//
// /reload 重载全部账号配置, 与「仅限白名单账号」的授权语义冲突; 且 Reload
// 会把已从磁盘删除、但内存仍残留的账号复活。
func TestReloadRejectsScopedKey(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "system.reload")

	req := keyReq(t, serverURL(ts.URL), "POST", "/api/reload", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 403 || !strings.Contains(raw, "ACCOUNT_NOT_ALLOWED") {
		t.Fatalf("受限秘钥不得重载全局配置, got %d %s", code, raw)
	}
	if f.reloadCount != 0 {
		t.Fatalf("被拒的请求不得触达后端 Reload, reloadCount=%d", f.reloadCount)
	}
}

// 未限定账号范围的秘钥(等价于全部账号)仍可重载——这是既有契约。
func TestReloadAllowsUnscopedKey(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, nil, "system.reload")

	req := keyReq(t, serverURL(ts.URL), "POST", "/api/reload", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("不受限秘钥应可重载, got %d %s", code, raw)
	}
	if f.reloadCount != 1 {
		t.Fatalf("Reload 应被调用一次, got %d", f.reloadCount)
	}
}

// 范围受限秘钥不得用「范围内的 account_id + 范围外的别名 ID」操作别名
// (审计 B2/A4)。范围外与不存在统一 404。
func TestAliasActionRejectsForeignAnonymousID(t *testing.T) {
	f := &fakeBackend{aliases: []hme.Alias{
		{AnonymousID: "own_1", Email: "own@icloud.com"},
	}}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.write")

	// 范围外别名: 404, 且不得触达后端。
	req := keyReq(t, serverURL(ts.URL), "DELETE", "/api/aliases/foreign_1", key.Key, `{"account_id":"acc_1"}`)
	code, raw, _ := do(t, req)
	if code != 404 || !strings.Contains(raw, "ALIAS_NOT_FOUND") {
		t.Fatalf("范围外别名应 404, got %d %s", code, raw)
	}
	if len(f.aliasActIDs) != 0 || f.aliasDeleteID != "" {
		t.Fatalf("被拒请求不得触达后端: act=%v del=%q", f.aliasActIDs, f.aliasDeleteID)
	}

	// 自己的别名: 正常放行。
	req2 := keyReq(t, serverURL(ts.URL), "DELETE", "/api/aliases/own_1", key.Key, `{"account_id":"acc_1"}`)
	code2, raw2, _ := do(t, req2)
	if code2 != 200 {
		t.Fatalf("范围内别名应可删除, got %d %s", code2, raw2)
	}
}

// 批量别名: 混入一个范围外 ID 时整批拒绝(不做部分执行)。
func TestBatchAliasRejectsForeignAnonymousID(t *testing.T) {
	f := &fakeBackend{aliases: []hme.Alias{
		{AnonymousID: "own_1", Email: "own@icloud.com"},
	}}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.write")

	body := `{"account_id":"acc_1","action":"deactivate","aliases":[` +
		`{"anonymous_id":"own_1","email":"own@icloud.com"},` +
		`{"anonymous_id":"foreign_1","email":"foreign@icloud.com"}]}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/aliases/batch", key.Key, body)
	code, raw, _ := do(t, req)
	if code != 404 {
		t.Fatalf("混入范围外别名应整批 404, got %d %s", code, raw)
	}
	if len(f.aliasActIDs) != 0 {
		t.Fatalf("整批拒绝时不得执行任何一项, 实际执行 %v", f.aliasActIDs)
	}
}

// 会话(管理员)不受归属校验影响: 不额外调用上游别名列表。
func TestAliasActionSessionSkipsOwnershipLookup(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("DELETE", string(serverURL(ts.URL))+"/api/aliases/whatever", strings.NewReader(`{"account_id":"acc_1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("会话删除别名应放行, got %d %s", code, raw)
	}
	if f.aliasDeleteID != "whatever" {
		t.Fatalf("会话路径应直接执行删除, got %q", f.aliasDeleteID)
	}
}
