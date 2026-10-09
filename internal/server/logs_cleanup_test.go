package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCleanupLogsRemovesOldEntriesAndPersists 验证按时间清理:
// 删除严格早于 cutoff(now - N 天)的日志, 内存与磁盘同步, 较新日志保留。
func TestCleanupLogsRemovesOldEntriesAndPersists(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.logs = []AliasTaskLog{
		{ID: "l_old", TaskID: "task_1", Time: now.AddDate(0, 0, -2).Format(time.RFC3339), Level: "info", Message: "两天前的日志"},
		{ID: "l_mid", TaskID: "task_1", Time: now.Add(-12 * time.Hour).Format(time.RFC3339), Level: "info", Message: "十二小时前的日志"},
		{ID: "l_new", TaskID: "task_1", Time: now.Add(-30 * time.Minute).Format(time.RFC3339), Level: "error", Message: "半小时前的日志"},
	}
	if err := m.writeLogs(m.logFile, m.logs); err != nil {
		t.Fatal(err)
	}

	deleted, remaining, err := m.cleanupLogs(1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || remaining != 2 {
		t.Fatalf("deleted/remaining = %d/%d, 期望 1/2", deleted, remaining)
	}
	if got := m.listLogs(); len(got) != 2 {
		t.Fatalf("内存应剩 2 条, got %d", len(got))
	}
	raw, err := os.ReadFile(m.logFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "两天前的日志") {
		t.Fatalf("磁盘文件仍含已清理日志: %s", raw)
	}
	if !strings.Contains(string(raw), "十二小时前的日志") || !strings.Contains(string(raw), "半小时前的日志") {
		t.Fatalf("磁盘文件缺少应保留的日志: %s", raw)
	}
}

// TestCleanupLogsBoundaryKeepsExactCutoffTime 验证边界: 恰好等于 cutoff 时刻的
// 条目保留, 只删除严格更早的(「1 天前」= 保留最近 24 小时)。
func TestCleanupLogsBoundaryKeepsExactCutoffTime(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	cutoff := now.AddDate(0, 0, -1)
	m.logs = []AliasTaskLog{
		{ID: "l_before", TaskID: "t", Time: cutoff.Add(-time.Second).Format(time.RFC3339), Level: "info", Message: "早一秒"},
		{ID: "l_exact", TaskID: "t", Time: cutoff.Format(time.RFC3339), Level: "info", Message: "恰好 cutoff"},
	}

	deleted, remaining, err := m.cleanupLogs(1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || remaining != 1 {
		t.Fatalf("deleted/remaining = %d/%d, 期望 1/1", deleted, remaining)
	}
	if got := m.listLogs(); got[0].ID != "l_exact" {
		t.Fatalf("应保留恰好 cutoff 的条目, got %+v", got)
	}
}

// TestCleanupLogsKeepsUnparseableTimeEntries 验证时间无法解析的条目保守保留:
// 无法证明其过期, 清理不应误删。
func TestCleanupLogsKeepsUnparseableTimeEntries(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.logs = []AliasTaskLog{
		{ID: "l_bad", TaskID: "t", Time: "not-a-time", Level: "info", Message: "时间非法"},
		{ID: "l_old", TaskID: "t", Time: now.AddDate(0, 0, -30).Format(time.RFC3339), Level: "info", Message: "三十天前"},
	}

	deleted, remaining, err := m.cleanupLogs(7)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || remaining != 1 {
		t.Fatalf("deleted/remaining = %d/%d, 期望 1/1(非法时间保留)", deleted, remaining)
	}
	if got := m.listLogs(); got[0].ID != "l_bad" {
		t.Fatalf("应保留时间非法的条目, got %+v", got)
	}
}

// TestCleanupLogsNoChangeDoesNotRewriteFile 验证没有可清理条目时不重写文件
// (避免无谓的磁盘写与 mtime 变化)。
func TestCleanupLogsNoChangeDoesNotRewriteFile(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	writes := 0
	m.writeLogs = func(string, any) error { writes++; return nil }
	m.logs = []AliasTaskLog{
		{ID: "l_new", TaskID: "t", Time: now.Add(-time.Hour).Format(time.RFC3339), Level: "info", Message: "一小时前"},
	}

	deleted, remaining, err := m.cleanupLogs(30)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || remaining != 1 {
		t.Fatalf("deleted/remaining = %d/%d, 期望 0/1", deleted, remaining)
	}
	if writes != 0 {
		t.Fatalf("无可清理条目不应重写文件, writes=%d", writes)
	}
}

// TestCleanupLogsPersistenceErrorKeepsMemory 验证落盘失败时内存不变、返回错误
// (与 logLocked 的失败语义一致: 先写盘成功才更新内存)。
func TestCleanupLogsPersistenceErrorKeepsMemory(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.writeLogs = func(string, any) error { return errors.New("disk full") }
	m.logs = []AliasTaskLog{
		{ID: "l_old", TaskID: "t", Time: now.AddDate(0, 0, -30).Format(time.RFC3339), Level: "info", Message: "三十天前"},
	}

	if _, _, err := m.cleanupLogs(1); err == nil {
		t.Fatal("落盘失败应返回错误")
	}
	if len(m.logs) != 1 {
		t.Fatalf("落盘失败不应改变内存, got %d 条", len(m.logs))
	}
}

// TestCleanupLogsEndpoint 验证 HTTP 层: POST /api/alias-task-logs/cleanup
// 删除指定天数前的日志, 返回 deleted/remaining, 列表随之更新。
func TestCleanupLogsEndpoint(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	// 预置: 一条 10 天前 + 一条 2 小时前
	s.task.mu.Lock()
	s.task.logs = []AliasTaskLog{
		{ID: "l_old", TaskID: "task_1", Time: time.Now().AddDate(0, 0, -10).Format(time.RFC3339), Level: "info", Message: "十天前"},
		{ID: "l_new", TaskID: "task_1", Time: time.Now().Add(-2 * time.Hour).Format(time.RFC3339), Level: "info", Message: "两小时前"},
	}
	s.task.mu.Unlock()

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-task-logs/cleanup", `{"older_than_days":7}`)
	if status != http.StatusOK {
		t.Fatalf("cleanup = %d: %s", status, body)
	}
	var out struct {
		Data struct {
			Deleted   int `json:"deleted"`
			Remaining int `json:"remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Deleted != 1 || out.Data.Remaining != 1 {
		t.Fatalf("deleted/remaining = %d/%d, 期望 1/1: %s", out.Data.Deleted, out.Data.Remaining, body)
	}

	// 列表随之更新: 只剩 2 小时前那条
	status, body = aliasTaskRequest(t, ts.URL, session, csrf, http.MethodGet, "/api/alias-task-logs", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d: %s", status, body)
	}
	var listOut struct {
		Data []AliasTaskLog `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &listOut); err != nil {
		t.Fatal(err)
	}
	if len(listOut.Data) != 1 || listOut.Data[0].ID != "l_new" {
		t.Fatalf("清理后列表 = %+v, 期望仅 l_new", listOut.Data)
	}
}

// TestCleanupLogsEndpointValidation 验证参数校验: older_than_days 必填且为
// 1-3650 的整数, 非法输入直接 400。
func TestCleanupLogsEndpointValidation(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	cases := []struct {
		name string
		body string
	}{
		{"缺字段", `{}`},
		{"零", `{"older_than_days":0}`},
		{"负数", `{"older_than_days":-3}`},
		{"超上限", `{"older_than_days":3651}`},
		{"非整数", `{"older_than_days":"7"}`},
		{"非法 JSON", `not-json`},
	}
	for _, c := range cases {
		status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-task-logs/cleanup", c.body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, 期望 400: %s", c.name, status, body)
		}
	}
}

// TestCleanupLogsEndpointRequiresCSRF 验证写接口的 CSRF 保护未被绕过。
func TestCleanupLogsEndpointRequiresCSRF(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, _ := login(t, ts, "admin-pass-2026-strong")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/alias-task-logs/cleanup", strings.NewReader(`{"older_than_days":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	status, _, _ := do(t, req)
	if status != http.StatusForbidden {
		t.Fatalf("无 CSRF 的清理请求 = %d, 期望 403", status)
	}
}
