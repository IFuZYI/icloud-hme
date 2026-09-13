package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func aliasTaskRequest(t *testing.T, tsURL, session, csrf, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, tsURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	status, response, _ := do(t, req)
	return status, response
}

func TestAliasTaskHandlersClassifyDomainAndPersistenceErrors(t *testing.T) {
	s, ts := newTestServer(&fakeBackend{})
	defer ts.Close()
	s.task.file = filepath.Join(t.TempDir(), "tasks.json")
	s.task.logFile = filepath.Join(filepath.Dir(s.task.file), "logs.json")
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	valid := `{"account_id":"acc_1","interval_minutes":60,"batch_count":1,"max_total":2,"label_prefix":"x"}`

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-tasks", `{"account_id":""}`)
	if status != http.StatusBadRequest || !strings.Contains(body, `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("validation error = %d: %s", status, body)
	}
	status, body = aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-tasks/missing/toggle", `{}`)
	if status != http.StatusNotFound || !strings.Contains(body, `"code":"TASK_NOT_FOUND"`) {
		t.Fatalf("not-found error = %d: %s", status, body)
	}

	s.task.writeState = func(string, any) error { return errors.New("injected task persistence failure") }
	status, body = aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-tasks", valid)
	if status != http.StatusInternalServerError || !strings.Contains(body, `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("state persistence error = %d: %s", status, body)
	}
}

func TestToggleAliasTaskHandlerReturnsInternalErrorForAuditPersistenceFailure(t *testing.T) {
	s, ts := newTestServer(&fakeBackend{})
	defer ts.Close()
	s.task.file = filepath.Join(t.TempDir(), "tasks.json")
	s.task.logFile = filepath.Join(filepath.Dir(s.task.file), "logs.json")
	task := AliasTask{ID: "task_handler", AccountID: "acc_1", IntervalMinutes: 60, BatchCount: 1, MaxTotal: 2, LabelPrefix: "x", NextNumber: 1}
	s.task.tasks[task.ID] = task
	if err := s.task.saveLocked(); err != nil {
		t.Fatal(err)
	}
	s.task.writeLogs = func(string, any) error { return errors.New("injected audit persistence failure") }
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/alias-tasks/"+task.ID+"/toggle", `{}`)
	if status != http.StatusInternalServerError || !strings.Contains(body, `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("audit persistence error = %d: %s", status, body)
	}
	current, _ := s.task.get(task.ID)
	if !current.Enabled {
		t.Fatalf("durably committed toggle was rolled back after audit failure: %+v", current)
	}
	s.task.close()
}
