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

func TestBatchDeleteAliasesWaitsAndWritesLogs(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(f)
	defer ts.Close()
	s.task.logFile = filepath.Join(t.TempDir(), "alias_task_logs.json")
	s.task.logs = nil
	s.task.batchDelay = 15 * time.Millisecond
	sess, csrf := login(t, ts, "admin-pass-2026-strong")

	req := authedReq(t, ts, http.MethodPost, "/api/aliases/batch", `{"account_id":"acc_1","action":"delete","aliases":[{"anonymous_id":"anon_a","email":"a@icloud.com"},{"anonymous_id":"anon_b","email":"b@icloud.com"}]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("批量删除期望 200,得到 %d: %s", status, body)
	}
	if len(f.aliasDeleteIDs) != 2 || f.aliasDeleteIDs[0] != "anon_a" || f.aliasDeleteIDs[1] != "anon_b" {
		t.Fatalf("删除调用顺序错误: %#v", f.aliasDeleteIDs)
	}
	if elapsed := f.aliasCallTimes[1].Sub(f.aliasCallTimes[0]); elapsed < s.task.batchDelay {
		t.Fatalf("两次删除间隔 %v,小于要求 %v", elapsed, s.task.batchDelay)
	}
	logs := s.task.listLogs()
	if len(logs) != 4 {
		t.Fatalf("期望开始、两条结果、完成共 4 条日志,得到 %d: %#v", len(logs), logs)
	}
	joined := ""
	for _, entry := range logs {
		joined += entry.Message + "\n"
	}
	for _, expected := range []string{"批量删除开始", "a@icloud.com 删除成功", "b@icloud.com 删除成功", "批量删除完成"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("日志缺少 %q: %s", expected, joined)
		}
	}
	var out struct {
		Data struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Succeeded != 2 || out.Data.Failed != 0 {
		t.Fatalf("批量结果错误: %s", body)
	}
}

func TestBatchAliasActionValidatesInput(t *testing.T) {
	f := &fakeBackend{}
	_, ts := newTestServer(f)
	defer ts.Close()
	sess, csrf := login(t, ts, "admin-pass-2026-strong")

	req := authedReq(t, ts, http.MethodPost, "/api/aliases/batch", `{"account_id":"acc_1","action":"unknown","aliases":[]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, _, _ := do(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("非法批量操作期望 400,得到 %d", status)
	}
}

func TestBatchAliasActionTreatsFalseResultAsItemFailure(t *testing.T) {
	operationSucceeded := false
	f := &fakeBackend{aliasActResult: &operationSucceeded}
	s, ts := newTestServer(f)
	defer ts.Close()
	s.task.logFile = filepath.Join(t.TempDir(), "alias_task_logs.json")
	sess, csrf := login(t, ts, "admin-pass-2026-strong")

	req := authedReq(t, ts, http.MethodPost, "/api/aliases/batch", `{"account_id":"acc_1","action":"deactivate","aliases":[{"anonymous_id":"anon_a","email":"a@icloud.com"}]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("批量停用期望 200,得到 %d: %s", status, body)
	}
	var out struct {
		Data struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
			Results   []struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Succeeded != 0 || out.Data.Failed != 1 || len(out.Data.Results) != 1 || out.Data.Results[0].Success || out.Data.Results[0].Error == "" {
		t.Fatalf("false 结果应计为单项失败: %s", body)
	}
	for _, entry := range s.task.listLogs() {
		if strings.Contains(entry.Message, "a@icloud.com 停用成功") {
			t.Fatalf("false 结果不应记录成功日志: %#v", entry)
		}
	}
}

func TestBatchAliasActionReturnsErrorWhenLogPersistenceFails(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(f)
	defer ts.Close()
	dir := t.TempDir()
	parentFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.task.logFile = filepath.Join(parentFile, "logs.json")
	sess, csrf := login(t, ts, "admin-pass-2026-strong")

	req := authedReq(t, ts, http.MethodPost, "/api/aliases/batch", `{"account_id":"acc_1","action":"delete","aliases":[{"anonymous_id":"anon_a","email":"a@icloud.com"}]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusInternalServerError || !strings.Contains(body, `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("日志持久化失败应返回 500 INTERNAL_ERROR,得到 %d: %s", status, body)
	}
	if len(f.aliasDeleteIDs) != 0 {
		t.Fatalf("开始日志未持久化时不应执行批量操作: %#v", f.aliasDeleteIDs)
	}
}

func TestBatchAliasActionReturnsOutcomesWhenPostOperationLogFails(t *testing.T) {
	f := &fakeBackend{}
	s, ts := newTestServer(f)
	defer ts.Close()
	s.task.logFile = filepath.Join(t.TempDir(), "alias_task_logs.json")
	s.task.logs = nil
	s.task.batchDelay = 0
	writes := 0
	s.task.writeLogs = func(path string, value any) error {
		writes++
		if writes > 1 {
			return errors.New("injected post-operation log failure")
		}
		return writeJSONAtomic(path, value)
	}
	sess, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, http.MethodPost, "/api/aliases/batch", `{"account_id":"acc_1","action":"delete","aliases":[{"anonymous_id":"anon_a","email":"a@icloud.com"},{"anonymous_id":"anon_b","email":"b@icloud.com"}]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: sess})
	req.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, req)
	if status != http.StatusOK {
		t.Fatalf("post-operation log failure should preserve normal response, got %d: %s", status, body)
	}
	var out struct {
		Data struct {
			Succeeded    int                `json:"succeeded"`
			Failed       int                `json:"failed"`
			Results      []batchAliasResult `json:"results"`
			LoggingError string             `json:"logging_error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Succeeded != 2 || out.Data.Failed != 0 || len(out.Data.Results) != 2 {
		t.Fatalf("outcomes were not returned: %s", body)
	}
	if !strings.Contains(out.Data.LoggingError, "injected post-operation log failure") {
		t.Fatalf("logging warning missing: %s", body)
	}
	if len(f.aliasDeleteIDs) != 2 {
		t.Fatalf("operations missing or duplicated: %#v", f.aliasDeleteIDs)
	}
}
