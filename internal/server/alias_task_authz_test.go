package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/apikey"
	"icloud-hme/internal/hme"
)

// ====================================================================
// 自动别名任务的「延迟权限」与只读/范围计数审计修复
// ====================================================================
//
// 契约:
//  1. 任务记录创建者秘钥(created_by_key_id); 会话创建时为空;
//  2. 每次创建别名前、任务武装前(ensureRunning)与服务重启自动续跑前
//     (start)都重校创建者秘钥: 删除/停用 → key_revoked; 收回 tasks.write
//     → key_permission_revoked; 账号移出范围 → key_scope_revoked;
//     不通过则停用任务(Enabled=false + PauseReason + warn 日志), 不再调用
//     CreateAlias;
//  3. GET /api/alias-tasks(list) 纯读: 不改 tasks.json 的内容与 mtime,
//     也不改动内存中的任务状态(展示口径的刷新只在返回值上做);
//  4. 日志清理的 remaining 只统计当前秘钥范围内的日志(会话为全局)。

// countingBackend 记录 CreateAlias 的实际调用次数(断言「不得再创建」)。
type countingBackend struct {
	fakeBackend
	mu      sync.Mutex
	creates []string
}

func (b *countingBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.creates = append(b.creates, accountID+"|"+label)
	return &hme.CreateResult{Email: label}, nil
}

func (b *countingBackend) createCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.creates)
}

// newCountingServer 用可计数的 backend 构造真实路由的测试 Server。
func newCountingServer(t *testing.T) (*Server, *httptest.Server, *countingBackend) {
	t.Helper()
	be := &countingBackend{}
	dir := t.TempDir()
	s := newWithBackend(be, Config{
		AdminPassword: "admin-pass-2026-strong",
		SessionTTL:    time.Hour,
		AutoTaskFile:  filepath.Join(dir, "alias_task.json"),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		s.task.close()
	})
	return s, ts, be
}

// createTaskBody 造一个「下一轮必然创建 1 个」的定时任务请求体。
// 定时任务无作息窗口/首日折算, 便于确定性地断言 CreateAlias 是否被调用。
const createTaskBody = `{"account_id":"acc_1","mode":"scheduled","interval_minutes":60,` +
	`"batch_count":1,"target_count":5,"daily_limit":50}`

func decodeTask(t *testing.T, raw string) AliasTask {
	t.Helper()
	var out struct {
		Data AliasTask `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("解析任务响应失败: %v (%s)", err, raw)
	}
	return out.Data
}

func hasWarnLog(t *testing.T, m *autoTaskManager, taskID, wantReason string) bool {
	t.Helper()
	for _, l := range m.listLogs() {
		if l.TaskID != taskID || l.Level != "warn" {
			continue
		}
		if l.Category != "auto_task" {
			t.Fatalf("warn 日志 category = %q, want auto_task", l.Category)
		}
		if !strings.Contains(l.Message, wantReason) {
			t.Fatalf("warn 日志未包含暂停原因 %q: %s", wantReason, l.Message)
		}
		return true
	}
	return false
}

// a) 创建者秘钥被删除: runOnce 不得再调用 CreateAlias, 任务停用并标记
// PauseReason=key_revoked, 同时写一条 warn 日志。
func TestAliasTaskAuthzKeyDeletedPausesTask(t *testing.T) {
	s, ts, be := newCountingServer(t)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	code, raw, _ := do(t, keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, createTaskBody))
	if code != http.StatusCreated {
		t.Fatalf("创建任务应 201, got %d %s", code, raw)
	}
	task := decodeTask(t, raw)
	if task.CreatedByKeyID != key.ID {
		t.Fatalf("任务未记录创建者秘钥: got %q, want %q", task.CreatedByKeyID, key.ID)
	}

	// 吊销创建者秘钥(删除)。
	if err := s.keys.Delete(key.ID); err != nil {
		t.Fatalf("删除秘钥: %v", err)
	}
	before := be.createCount()
	out := s.task.runOnce(task.ID)
	if got := be.createCount(); got != before {
		t.Fatalf("秘钥已删除仍调用 CreateAlias: 调用次数 %d -> %d (%v)", before, got, be.creates)
	}
	if out.Enabled {
		t.Fatalf("秘钥已删除, 任务必须停用: %+v", out)
	}
	if out.PauseReason != "key_revoked" {
		t.Fatalf("PauseReason = %q, want key_revoked", out.PauseReason)
	}
	if !hasWarnLog(t, s.task, task.ID, "key_revoked") {
		t.Fatal("停用任务时必须写一条 warn 日志(category auto_task)")
	}
}

// b) 秘钥账号范围缩小到不含该账号: 同样停用, 原因为 key_scope_revoked。
func TestAliasTaskAuthzKeyScopeShrunkPausesTask(t *testing.T) {
	s, ts, be := newCountingServer(t)
	key := createTestKeyScoped(t, s, []string{"acc_1", "acc_2"}, "tasks.write")

	code, raw, _ := do(t, keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, createTaskBody))
	if code != http.StatusCreated {
		t.Fatalf("创建任务应 201, got %d %s", code, raw)
	}
	task := decodeTask(t, raw)

	// 缩范围: 移除 acc_1。
	narrowed := []string{"acc_2"}
	if _, err := s.keys.Update(key.ID, apikey.UpdateInput{AccountIDs: &narrowed}); err != nil {
		t.Fatalf("缩小秘钥范围: %v", err)
	}
	before := be.createCount()
	out := s.task.runOnce(task.ID)
	if got := be.createCount(); got != before {
		t.Fatalf("账号已移出秘钥范围仍调用 CreateAlias: %d -> %d", before, got)
	}
	if out.Enabled || out.PauseReason != "key_scope_revoked" {
		t.Fatalf("缩范围后任务应停用且 reason=key_scope_revoked, got %+v", out)
	}
	if !hasWarnLog(t, s.task, task.ID, "key_scope_revoked") {
		t.Fatal("缺少 key_scope_revoked 的 warn 日志")
	}
}

// b2) 秘钥被收回 tasks.write: key_permission_revoked。
func TestAliasTaskAuthzKeyPermissionRevokedPausesTask(t *testing.T) {
	s, ts, be := newCountingServer(t)
	key := createTestKeyScoped(t, s, []string{"acc_1"}, "tasks.write")

	code, raw, _ := do(t, keyReq(t, serverURL(ts.URL), "POST", "/api/alias-tasks", key.Key, createTaskBody))
	if code != http.StatusCreated {
		t.Fatalf("创建任务应 201, got %d %s", code, raw)
	}
	task := decodeTask(t, raw)

	perms := []string{"inbox.list"}
	if _, err := s.keys.Update(key.ID, apikey.UpdateInput{Permissions: &perms}); err != nil {
		t.Fatalf("收回权限: %v", err)
	}
	before := be.createCount()
	out := s.task.runOnce(task.ID)
	if got := be.createCount(); got != before {
		t.Fatalf("权限已收回仍调用 CreateAlias: %d -> %d", before, got)
	}
	if out.Enabled || out.PauseReason != "key_permission_revoked" {
		t.Fatalf("收权后任务应停用且 reason=key_permission_revoked, got %+v", out)
	}
}

// c) 正向对照: 会话创建的任务(created_by_key_id 为空)不受秘钥校验影响,
// runOnce 仍正常创建别名。
func TestAliasTaskAuthzSessionTaskStillCreates(t *testing.T) {
	s, ts, be := newCountingServer(t)
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, "POST", "/api/alias-tasks", createTaskBody)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ := do(t, req)
	if code != http.StatusCreated {
		t.Fatalf("会话创建任务应 201, got %d %s", code, raw)
	}
	task := decodeTask(t, raw)
	if task.CreatedByKeyID != "" {
		t.Fatalf("会话创建的任务不得带创建者秘钥: %q", task.CreatedByKeyID)
	}

	before := be.createCount()
	out := s.task.runOnce(task.ID)
	if got := be.createCount(); got != before+1 {
		t.Fatalf("会话任务应正常创建 1 个别名, 调用次数 %d -> %d", before, got)
	}
	if !out.Enabled {
		t.Fatalf("会话任务不应被停用: %+v", out)
	}
	if out.PauseReason != "" {
		t.Fatalf("会话任务 PauseReason 应为空, got %q", out.PauseReason)
	}
}

// d) list() 纯读: 构造 daily_date 已过期的任务, 调用 list() 后 tasks.json
// 的内容与 mtime 都不变, 且内存状态不被推进(只有返回值做展示刷新)。
func TestAliasTaskAuthzListIsReadOnly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	// 无 created_at → 触发加载期 backfill; list() 若落盘会改写文件。
	legacy := `{"tasks":[{"id":"task_out","account_id":"acc_B","mode":"scheduled",` +
		`"interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,` +
		`"next_number":1,"enabled":false,"daily_count":7,"daily_date":"2020-01-01"}]}`
	if err := os.WriteFile(file, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()
	out := m.list()
	if len(out) != 1 {
		t.Fatalf("list 应返回 1 个任务, got %d", len(out))
	}
	// 展示口径仍与调度一致: 过期周期显示归零。
	if out[0].DailyCount != 0 || out[0].DailyDate == "2020-01-01" {
		t.Fatalf("列表展示应刷新过期周期: count=%d date=%q", out[0].DailyCount, out[0].DailyDate)
	}
	// 纯读: 内存状态不得被推进。
	stored, _ := m.get("task_out")
	if stored.DailyCount != 7 || stored.DailyDate != "2020-01-01" {
		t.Fatalf("list() 改动了内存任务状态: count=%d date=%q", stored.DailyCount, stored.DailyDate)
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("list() 改写了 tasks.json:\nbefore=%s\nafter=%s", before, after)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(past) {
		t.Fatalf("list() 改了 tasks.json 的 mtime: got %v, want %v", st.ModTime(), past)
	}
}

// d2) 执行路径必须接手展示读路径移出的状态推进: runOnce 后过期周期被重置
// 并落盘(证明 list 变纯读没有丢掉迁移能力)。
func TestAliasTaskAuthzRunOncePersistsCycleReset(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	legacy := `{"tasks":[{"id":"task_out","account_id":"acc_B","mode":"scheduled",` +
		`"interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,` +
		`"next_number":1,"enabled":true,"daily_count":7,"daily_date":"2020-01-01"}]}`
	if err := os.WriteFile(file, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()
	m.runOnce("task_out")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var f aliasTaskFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tasks) != 1 || f.Tasks[0].DailyDate == "2020-01-01" {
		t.Fatalf("runOnce 未把周期重置落盘: %+v", f.Tasks)
	}
	if f.Tasks[0].CreatedCount < 1 {
		t.Fatalf("runOnce 应正常创建并落盘: %+v", f.Tasks)
	}
}

// e) cleanup 的 remaining 只统计当前秘钥范围内的日志(审计 A3 侧信道)。
func TestAliasTaskAuthzCleanupRemainingScoped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()
	taskA, err := m.create(aliasTaskInput{AccountID: "acc_A", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, TargetCount: 5})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := m.create(aliasTaskInput{AccountID: "acc_B", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, TargetCount: 5})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -2).Format(time.RFC3339)
	recent := now.Format(time.RFC3339)
	m.mu.Lock()
	m.logs = []AliasTaskLog{
		{ID: "log_a_old", TaskID: taskA.ID, Time: old, Level: "info", Message: "a-old", Category: "auto_task"},
		{ID: "log_a_new", TaskID: taskA.ID, Time: recent, Level: "info", Message: "a-new", Category: "auto_task"},
		{ID: "log_b_old", TaskID: taskB.ID, Time: old, Level: "info", Message: "b-old", Category: "auto_task"},
	}
	m.mu.Unlock()

	deleted, remaining, err := m.cleanupLogsScoped(1, []string{"acc_A"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || remaining != 1 {
		t.Fatalf("范围内清理 deleted/remaining = %d/%d, want 1/1(全局剩余数会泄露范围外计数)", deleted, remaining)
	}
	// 范围外日志必须保留(不得被受限秘钥抹除)。
	keptB := false
	for _, l := range m.listLogs() {
		if l.ID == "log_b_old" {
			keptB = true
		}
	}
	if !keptB {
		t.Fatal("范围外账号的日志被删除了")
	}
	// 会话路径(全局)仍返回全局剩余数。
	deleted, remaining, err = m.cleanupLogs(1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || remaining != 1 {
		t.Fatalf("全局清理 deleted/remaining = %d/%d, want 1/1", deleted, remaining)
	}
}

// e2) HTTP 层: 受限秘钥清理日志时 remaining 只含范围内日志。
func TestAliasTaskAuthzCleanupHandlerRemainingScoped(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	taskA := seedTask(t, s, "acc_A", "A")
	taskB := seedTask(t, s, "acc_B", "B")
	now := time.Now()
	old := now.AddDate(0, 0, -2).Format(time.RFC3339)
	s.task.mu.Lock()
	s.task.logs = []AliasTaskLog{
		{ID: "log_a_old", TaskID: taskA, Time: old, Level: "info", Message: "a-old", Category: "auto_task"},
		{ID: "log_b_old", TaskID: taskB, Time: old, Level: "info", Message: "b-old", Category: "auto_task"},
	}
	s.task.mu.Unlock()

	key := createTestKeyScoped(t, s, []string{"acc_A"}, "tasks.write")
	code, raw, _ := do(t, keyReq(t, serverURL(ts.URL), "POST", "/api/alias-task-logs/cleanup", key.Key,
		`{"older_than_days":1}`))
	if code != http.StatusOK {
		t.Fatalf("清理应 200, got %d %s", code, raw)
	}
	var out struct {
		Data struct {
			Deleted   int `json:"deleted"`
			Remaining int `json:"remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Deleted != 1 || out.Data.Remaining != 0 {
		t.Fatalf("受限秘钥清理 deleted/remaining = %d/%d, want 1/0(不得泄露范围外日志数)",
			out.Data.Deleted, out.Data.Remaining)
	}
}

// 重启武装前校验: start() 不得把创建者秘钥已失效的任务自动续跑。
func TestAliasTaskAuthzStartDoesNotArmRevokedKeyTask(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tasks.json")
	legacy := `{"tasks":[{"id":"task_gone","account_id":"acc_1","mode":"scheduled",` +
		`"interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,` +
		`"next_number":1,"enabled":true,"created_by_key_id":"key_deleted"}]}`
	if err := os.WriteFile(file, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	be := &taskBackend{}
	m := newAutoTaskManager(file, be)
	defer m.close()
	m.start()

	out, ok := m.get("task_gone")
	if !ok {
		t.Fatal("任务丢失")
	}
	if out.Enabled || out.PauseReason != "key_revoked" {
		t.Fatalf("重启后不得武装秘钥已失效的任务: enabled=%v reason=%q", out.Enabled, out.PauseReason)
	}
	m.mu.Lock()
	_, armed := m.stops["task_gone"]
	m.mu.Unlock()
	if armed {
		t.Fatal("秘钥已失效的任务仍被武装(调度循环已启动)")
	}
	if !hasWarnLog(t, m, "task_gone", "key_revoked") {
		t.Fatal("重启停用任务时缺少 warn 日志")
	}
}
