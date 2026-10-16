package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/apikey"
)

// ====================================================================
// 秘钥账号范围: 资源归属校验(任务/日志)
// ====================================================================
//
// 契约:
//   - 任务类路由(/api/alias-tasks*、/api/alias-task-logs*)的目标账号
//     不在秘钥范围内 → 403 ACCOUNT_NOT_ALLOWED, 且不得到达 handler;
//   - 列表接口只返回范围内的任务/日志(不泄露范围外账号数据);
//   - 单项操作用 :id 解析任务真实归属账号后判定(不能只信 body);
//   - 会话请求完全不受影响(管理员看全部)。

// seedTask 造一个属于指定账号的任务。
func seedTask(t *testing.T, s *Server, accountID, name string) string {
	t.Helper()
	tk, err := s.task.create(aliasTaskInput{
		AccountID:   accountID,
		Mode:        "auto",
		DailyLimit:  5,
		TargetCount: 5,
	})
	_ = name
	if err != nil {
		t.Fatalf("seedTask(%s): %v", accountID, err)
	}
	return tk.ID
}

// 列表: 范围受限秘钥只看到范围内账号的任务。
func TestAPIKeyTaskListFiltersByAccountScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	seedTask(t, s, "acc_1", "范围内任务")
	seedTask(t, s, "acc_2", "范围外任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/alias-tasks", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("有权限的列表请求应 200, got %d %s", code, raw)
	}
	if strings.Contains(raw, `"acc_2"`) {
		t.Fatalf("列表泄露范围外账号的任务: %s", raw)
	}
	if !strings.Contains(raw, `"acc_1"`) {
		t.Fatalf("列表应包含范围内任务: %s", raw)
	}
}

// 列表: 空范围(全部账号)秘钥看到全部任务。
func TestAPIKeyTaskListEmptyScopeSeesAll(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	seedTask(t, s, "acc_1", "任务甲")
	seedTask(t, s, "acc_2", "任务乙")
	key := createTestKey(t, s, "tasks.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/alias-tasks", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("空范围应 200, got %d %s", code, raw)
	}
	if !strings.Contains(raw, `"acc_1"`) || !strings.Contains(raw, `"acc_2"`) {
		t.Fatalf("空范围应看到全部任务: %s", raw)
	}
}

// 单项: 范围外任务 ID 的 DELETE 应 403, 且任务仍存在。
func TestAPIKeyTaskDeleteDeniesOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "范围外任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	req := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID, key.Key, "")
	code, raw, _ := do(t, req)
	// 范围外与不存在统一 404(防存在性探测), 见 N4 测试。
	if code != 404 || !strings.Contains(raw, "TASK_NOT_FOUND") {
		t.Fatalf("范围外任务应 404 TASK_NOT_FOUND, got %d %s", code, raw)
	}
	if _, ok := s.task.get(taskID); !ok {
		t.Fatal("越权删除不应生效")
	}
}

// 单项: 范围外任务的 PATCH 应 403(即使 body 省略 account_id)。
func TestAPIKeyTaskPatchDeniesOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "范围外任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"daily_limit":9}`
	req := keyReq(t, serverURL(ts.URL), "PATCH", "/api/alias-tasks/"+taskID, key.Key, body)
	code, raw, _ := do(t, req)
	// 范围外与不存在统一 404(防存在性探测)。
	if code != 404 || !strings.Contains(raw, "TASK_NOT_FOUND") {
		t.Fatalf("范围外任务 PATCH 应 404, got %d %s", code, raw)
	}
	got, _ := s.task.get(taskID)
	if got.DailyLimit == 9 {
		t.Fatal("越权修改不应生效")
	}
}

// 单项: 范围内任务的 DELETE 放行(不误伤)。
func TestAPIKeyTaskDeleteAllowsInScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_1", "范围内任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	req := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID, key.Key, "")
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 {
		t.Fatalf("范围内任务删除应放行, got %d %s", code, raw)
	}
	if _, ok := s.task.get(taskID); ok {
		t.Fatal("范围内任务应被删除")
	}
}

// 日志: 范围受限秘钥只看到范围内任务的日志。
func TestAPIKeyTaskLogsFilterByAccountScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	inID := seedTask(t, s, "acc_1", "范围内任务")
	outID := seedTask(t, s, "acc_2", "范围外任务")
	if err := s.task.log(inID, "info", "范围内日志"); err != nil {
		t.Fatal(err)
	}
	if err := s.task.log(outID, "info", "范围外日志"); err != nil {
		t.Fatal(err)
	}
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/alias-task-logs", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("应 200, got %d %s", code, raw)
	}
	// 范围外日志消息不得出现。
	if strings.Contains(raw, "范围外日志") {
		t.Fatalf("日志泄露范围外账号: %s", raw)
	}
	if !strings.Contains(raw, "范围内日志") {
		t.Fatalf("日志应包含范围内任务日志: %s", raw)
	}
}

// 清理: 范围受限秘钥不得全局清理(仅能清理范围内任务日志)。
func TestAPIKeyLogsCleanupScopedToAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	inID := seedTask(t, s, "acc_1", "范围内任务")
	outID := seedTask(t, s, "acc_2", "范围外任务")
	_ = s.task.log(inID, "info", "范围内旧日志")
	_ = s.task.log(outID, "info", "范围外旧日志")

	// 把 manager 的"现在"推到 3 天后: 使两条日志(真实时间戳)都早于 cutoff,
	// 否则 older_than_days=1 下无条目过期, 测试形同虚设(变异存活)。
	base := time.Now()
	s.task.now = func() time.Time { return base.AddDate(0, 0, 3) }

	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"older_than_days":1}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-task-logs/cleanup", key.Key, body)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("应 200, got %d %s", code, raw)
	}
	// 范围内日志必须被清理。
	logs := s.task.listLogs()
	inFound, outFound := false, false
	for _, l := range logs {
		if l.TaskID == inID && l.Message == "范围内旧日志" {
			inFound = true
		}
		if l.TaskID == outID && l.Message == "范围外旧日志" {
			outFound = true
		}
	}
	if inFound {
		t.Fatal("范围内过期日志应被清理")
	}
	if !outFound {
		t.Fatalf("范围外日志被越权清理(应保留): %+v", logs)
	}
}

// 会话请求不受范围限制(管理员看全部)。
func TestSessionTaskListUnaffected(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	seedTask(t, s, "acc_1", "任务甲")
	seedTask(t, s, "acc_2", "任务乙")
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest("GET", ts.URL+"/api/alias-tasks", nil)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code != 200 || !strings.Contains(raw, `"acc_1"`) || !strings.Contains(raw, `"acc_2"`) {
		t.Fatalf("会话应看到全部任务, got %d %s", code, raw)
	}
}

var _ = apikey.Key{}

// ====================================================================
// N3: GET /api/accounts 按秘钥范围过滤
// ====================================================================
//
// 范围受限的秘钥不应看到范围外账号的摘要(名称/邮箱等)。

func TestAPIKeyAccountsListFiltersByScope(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{
		{ID: "acc_1", Name: "范围内账号", RealEmail: "in@x.com"},
		{ID: "acc_2", Name: "范围外账号", RealEmail: "out@x.com"},
	}}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "accounts.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/accounts", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("应 200, got %d %s", code, raw)
	}
	if strings.Contains(raw, "范围外账号") || strings.Contains(raw, "out@x.com") {
		t.Fatalf("账号列表泄露范围外账号: %s", raw)
	}
	if !strings.Contains(raw, "范围内账号") {
		t.Fatalf("应包含范围内账号: %s", raw)
	}
}

// 会话/空范围秘钥看到全部账号。
func TestAPIKeyAccountsListEmptyScopeSeesAll(t *testing.T) {
	f := &fakeBackend{accounts: []account.Summary{
		{ID: "acc_1", Name: "甲"}, {ID: "acc_2", Name: "乙"},
	}}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "accounts.list")

	req := keyReq(t, serverURL(ts.URL), "GET", "/api/accounts", key.Key, "")
	code, raw, _ := do(t, req)
	if code != 200 || !strings.Contains(raw, `"acc_1"`) || !strings.Contains(raw, `"acc_2"`) {
		t.Fatalf("空范围应看到全部账号, got %d %s", code, raw)
	}
}

// ====================================================================
// N4: 不泄露任务存在性(范围外与不存在同响应)
// ====================================================================
//
// 范围受限秘钥对「范围外任务」与「不存在的任务」应得到一致的响应, 否则
// 可枚举探测其它账号的任务是否存在。两者统一为 404(不区分)。

func TestAPIKeyTaskOutOfScopeIndistinguishableFromMissing(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "范围外任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	// 范围外任务
	req1 := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID, key.Key, "")
	c1, raw1, _ := do(t, req1)
	// 不存在的任务
	req2 := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/task_does_not_exist", key.Key, "")
	c2, raw2, _ := do(t, req2)

	if c1 != c2 {
		t.Fatalf("范围外(%d)与不存在(%d)状态码应一致(防存在性探测)", c1, c2)
	}
	if c1 != 404 {
		t.Fatalf("应统一为 404, got %d %s", c1, raw1)
	}
	// 错误码也应一致。
	if !strings.Contains(raw1, "TASK_NOT_FOUND") {
		t.Fatalf("范围外响应应伪装为不存在: %s", raw1)
	}
	_ = raw2
	// 任务必须仍在(未被越权删除)。
	if _, ok := s.task.get(taskID); !ok {
		t.Fatal("越权删除不应生效")
	}
}

// ====================================================================
// H1: 任务路由必须以 :id 真实归属为准, 忽略请求声明的 account_id
// ====================================================================
//
// 漏洞: 若先取 query/body 的 account_id(客户端自述), 攻击者传范围内
// account_id + 他账号的 task_id 即可越权(实测 DELETE 他账号任务 -> 200)。
// 正确: 任务路由无条件用 :id 解析真实归属再做范围判定。

// 带范围内 account_id 的 query, 对他账号任务 DELETE 必须被拒。
func TestH1TaskDeleteIgnoresDeclaredAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "他账号任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	// query 声明范围内账号, 但 task_id 属于 acc_2。
	req := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID+"?account_id=acc_1", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 200 {
		t.Fatalf("越权删除成功(H1): %d %s", code, raw)
	}
	if _, ok := s.task.get(taskID); !ok {
		t.Fatal("他账号任务被越权删除")
	}
}

// 带范围内 account_id 的 body, 对他账号任务 PATCH 必须被拒。
func TestH1TaskPatchIgnoresDeclaredAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "他账号任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"daily_limit":7,"target_count":7,"account_id":"acc_1"}`
	req := keyReq(t, serverURL(ts.URL), "PATCH", "/api/alias-tasks/"+taskID, key.Key, body)
	code, raw, _ := do(t, req)
	if code == 200 {
		t.Fatalf("越权修改成功(H1): %d %s", code, raw)
	}
	got, _ := s.task.get(taskID)
	if got.AccountID != "acc_2" {
		t.Fatalf("任务归属被篡改: %s", got.AccountID)
	}
}

// 带范围内 account_id 的 body, 对他账号任务 toggle 必须被拒。
func TestH1TaskToggleIgnoresDeclaredAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "他账号任务")
	before, _ := s.task.get(taskID)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks/"+taskID+"/toggle?account_id=acc_1", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 200 {
		t.Fatalf("越权 toggle 成功(H1): %d %s", code, raw)
	}
	after, _ := s.task.get(taskID)
	if after.Enabled != before.Enabled {
		t.Fatal("他账号任务状态被越权翻转")
	}
}

// 范围内任务即便 query 声明了别的账号, 也应按真实归属放行(不误伤)。
func TestH1InScopeTaskUnaffectedByDeclaredAccountID(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_1", "范围内任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	// 声明一个范围外账号也应按真实归属(acc_1)放行。
	req := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID+"?account_id=acc_9", key.Key, "")
	code, raw, _ := do(t, req)
	if code == 401 || code == 403 || code == 404 {
		t.Fatalf("范围内任务应按真实归属放行, got %d %s", code, raw)
	}
	if _, ok := s.task.get(taskID); ok {
		t.Fatal("范围内任务应被删除")
	}
}

// ====================================================================
// L1: 范围受限秘钥不得创建新账号(超出其账号范围语义)
// ====================================================================
//
// 范围受限秘钥只能操作其白名单内的账号; POST /api/accounts 创建的新账号
// 不在任何范围内, 属范围外行为, 应拒绝。

func TestScopedKeyCannotCreateAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "accounts.write")

	body := `{"name":"新账号","icloud_email":"n@x.com"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/accounts", key.Key, body)
	code, raw, _ := do(t, req)
	if code != 403 {
		t.Fatalf("范围受限秘钥不应能创建账号, got %d %s", code, raw)
	}
}

// 空范围(全部账号)秘钥可创建账号。
func TestUnscopedKeyCanCreateAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKey(t, s, "accounts.write")

	body := `{"name":"新账号","icloud_email":"n@x.com"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/accounts", key.Key, body)
	code, raw, _ := do(t, req)
	if code == 403 || code == 401 {
		t.Fatalf("空范围秘钥应能创建账号, got %d %s", code, raw)
	}
}

// ====================================================================
// H1b: 范围受限秘钥不得把任务改到范围外账号
// ====================================================================
//
// 中间件按任务真实归属放行后, PATCH body 的 account_id 仍可能把任务
// 迁移到别账号。范围受限秘钥不得改变任务归属到白名单之外。

func TestScopedKeyCannotReassignTaskToOutOfScopeAccount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_1", "范围内任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_2","daily_limit":7,"target_count":7,"mode":"auto"}`
	req := keyReq(t, serverURL(ts.URL), "PATCH", "/api/alias-tasks/"+taskID, key.Key, body)
	code, raw, _ := do(t, req)
	if code == 200 {
		got, _ := s.task.get(taskID)
		if got.AccountID != "acc_1" {
			t.Fatalf("范围受限秘钥把任务改到了范围外账号: %s (code=%d %s)", got.AccountID, code, raw)
		}
	}
}

// ====================================================================
// Q1: 范围校验必须覆盖请求中「所有」账号来源(query 与 body 不一致时)
// ====================================================================
//
// 漏洞: accountScopeTarget 优先 query, handler 只读 body -> 传
// ?account_id=acc_1(范围内) + body account_id=acc_2(范围外) 越权。
// 修复: 校验请求声明的每个 account_id 来源, 任一范围外即拒。

// create 任务: query 范围内 + body 范围外 -> 拒绝。
func TestQ1CreateTaskRejectsBodyOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_2","mode":"auto","target_count":5,"daily_limit":5}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks?account_id=acc_1", key.Key, body)
	code, raw, _ := do(t, req)
	if code == 201 || code == 200 {
		t.Fatalf("Q1 越权: query 范围内 + body 范围外 仍创建成功: %d %s", code, raw)
	}
	// 确认未创建到范围外账号。
	for _, tk := range s.task.list() {
		if tk.AccountID == "acc_2" {
			t.Fatalf("任务被创建到范围外账号 acc_2: %+v", tk)
		}
	}
}

// batch 别名: query 范围内 + body 范围外 -> 拒绝。
func TestQ1BatchAliasRejectsBodyOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.write")

	body := `{"account_id":"acc_2","action":"delete","aliases":[{"anonymous_id":"a1","email":"x@y.com"}]}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/aliases/batch?account_id=acc_1", key.Key, body)
	code, raw, _ := do(t, req)
	if code == 200 {
		t.Fatalf("Q1 越权: batch 仍对范围外账号执行: %d %s", code, raw)
	}
}

// create 别名(/create): query 范围内 + body 范围外 -> 拒绝。
func TestQ1CreateAliasRejectsBodyOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "aliases.create")

	body := `{"account_id":"acc_2","label":"x"}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/create?account_id=acc_1", key.Key, body)
	code, raw, _ := do(t, req)
	if code == 200 || code == 201 {
		t.Fatalf("Q1 越权: /create 仍对范围外账号执行: %d %s", code, raw)
	}
}

// 反向: query 范围外 + body 范围内 -> 也拒绝(两来源都必须合法)。
func TestQ1RejectsQueryOutOfScope(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	body := `{"account_id":"acc_1","mode":"auto","target_count":5,"daily_limit":5}`
	req := keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks?account_id=acc_9", key.Key, body)
	code, raw, _ := do(t, req)
	if code == 201 || code == 200 {
		t.Fatalf("query 范围外应拒绝: %d %s", code, raw)
	}
}

// ====================================================================
// M1: 范围外与不存在的任务响应「完全一致」(状态码 + 错误码 + 消息)
// ====================================================================
//
// 中间件伪造的 404 消息必须与 handler 抛出的完全一致, 否则消息文本
// 构成存在性 oracle。

func TestScopedTask404MessageMatchesHandler(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskID := seedTask(t, s, "acc_2", "他账号任务")
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	// 他账号任务(中间件伪造 404)
	reqA := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/"+taskID, key.Key, "")
	_, rawA, _ := do(t, reqA)

	// 不存在任务(handler 真 404)
	reqB := keyReq(t, serverURL(ts.URL), "DELETE", "/api/alias-tasks/task_missing_xyz", key.Key, "")
	_, rawB, _ := do(t, reqB)

	if rawA != rawB {
		t.Fatalf("范围外与不存在的响应体必须完全一致\n他账号: %s\n不存在: %s", rawA, rawB)
	}
}
