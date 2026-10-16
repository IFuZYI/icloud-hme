package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ====================================================================
// 日志策略优化: category 字段 + 写入频率 + 保留上限
// ====================================================================
//
// 契约:
//   - AliasTaskLog 增 category 字段(auto_task/batch_alias/purge);
//     旧数据无该字段时按 auto_task 处理(向后兼容);
//   - 保留上限 2000 条(原 500——加清理任务后 500 会过快冲掉历史);
//   - 批量操作不再逐项写日志(开始 1 条 + 失败逐条 + 结束 1 条),
//     写盘次数从 O(N) 降到 O(失败数 + 2)。

// 日志条目带 category; 未指定时为空(读侧按 auto_task 处理)。
func TestLogCategoryField(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "alias_task.json"), &fakeBackend{})
	if err := m.logWithCategory("auto_task", "t1", "info", "自动任务日志"); err != nil {
		t.Fatal(err)
	}
	if err := m.logWithCategory("batch_alias", "t2", "info", "批量日志"); err != nil {
		t.Fatal(err)
	}
	logs := m.listLogs()
	if len(logs) != 2 {
		t.Fatalf("日志数 = %d, want 2", len(logs))
	}
	// 新→旧。
	if logs[0].Category != "batch_alias" || logs[1].Category != "auto_task" {
		t.Fatalf("category 字段异常: %+v", logs)
	}
}

// 旧格式日志(无 category)仍能加载, 视作 auto_task。
func TestLogCategoryBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "alias_task_logs.json")
	old := `[{"id":"log_old","task_id":"t","time":"2026-01-01T00:00:00Z","level":"info","message":"旧格式"}]`
	if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAutoTaskManager(filepath.Join(dir, "alias_task.json"), &fakeBackend{})
	m.loadLogs()
	logs := m.listLogs()
	if len(logs) != 1 {
		t.Fatalf("旧格式应能加载: %d", len(logs))
	}
	if logs[0].Category != "" {
		t.Fatalf("旧条目 category 应为空: %q", logs[0].Category)
	}
}

// 保留上限 2000。
func TestLogKeepLimit2000(t *testing.T) {
	if maxTaskLogEntries != 2000 {
		t.Fatalf("maxTaskLogEntries = %d, want 2000", maxTaskLogEntries)
	}
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "alias_task.json"), &fakeBackend{})
	for i := 0; i < 2100; i++ {
		if err := m.log("t", "info", "第 %d 条", i); err != nil {
			t.Fatal(err)
		}
	}
	logs := m.listLogs()
	if len(logs) != 2000 {
		t.Fatalf("日志数 = %d, want 2000", len(logs))
	}
	// 最新一条应在。
	if !strings.Contains(logs[0].Message, "2099") {
		t.Fatalf("最新条目异常: %s", logs[0].Message)
	}
}

// 写入频率: 全成功的批量操作只写 2 次(开始+结束), 失败逐条增加。
func TestBatchAliasLogWriteCount(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	_ = session

	// 3 个全成功的批量停用: 开始 1 + 结束 1 = 2 条日志。
	body := `{"account_id":"acc_1","action":"deactivate","aliases":[
		{"anonymous_id":"a1","email":"a1@x.com"},
		{"anonymous_id":"a2","email":"a2@x.com"},
		{"anonymous_id":"a3","email":"a3@x.com"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/aliases/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("批量应成功: %d %s", code, raw)
	}
	logs := s.task.listLogs()
	// 3 项全成功 → 只有 2 条(开始+完成), 不再逐项。
	if len(logs) != 2 {
		t.Fatalf("日志数 = %d, want 2 (开始+结束, 成功项聚合)", len(logs))
	}
	// 全部带 batch_alias 类别。
	for _, l := range logs {
		if l.Category != "batch_alias" {
			t.Fatalf("category = %q, want batch_alias", l.Category)
		}
	}
}
