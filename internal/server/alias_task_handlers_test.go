package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/hme"
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
	valid := `{"account_id":"acc_1","mode":"scheduled","interval_minutes":20,"batch_count":1,"target_count":2}`

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
	task := AliasTask{ID: "task_handler", AccountID: "acc_1", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
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

func TestCreateAliasRejectsManualCreationWhenAccountDailyLimitIsReached(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com", Label: "GitHub"}}
	s, ts := newTestServer(f)
	defer ts.Close()
	today := time.Now().Format("2006-01-02")
	s.task.tasks["task_daily_cap"] = AliasTask{
		ID: "task_daily_cap", AccountID: "acc_1", Enabled: true, Mode: taskModeScheduled, IntervalMinutes: 60, BatchCount: 1,
		DailyLimit: maxTaskDailyLimit, MaxTotal: 100, DailyCount: maxTaskDailyLimit, DailyDate: today,
	}
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"GitHub"}`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("manual creation at daily cap = %d: %s", status, body)
	}
	var response struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "CREATION_LIMIT_REACHED" {
		t.Fatalf("error code = %q, want CREATION_LIMIT_REACHED", response.Code)
	}
}

func TestAliasLabelLibraryEndpointAndManualValidation(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com", Label: "GitHub"}}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/alias-labels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	status, body, _ := do(t, req)
	if status != http.StatusOK || !strings.Contains(body, "GitHub") {
		t.Fatalf("label library = %d: %s", status, body)
	}

	status, body = aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"任意自定义标签"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("custom manual label = %d: %s", status, body)
	}
}

func TestCreateAliasRejectsRequestsDuringAccountCooldown(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com", Label: "GitHub"}}
	s, ts := newTestServer(f)
	defer ts.Close()
	s.task.creationGuards["acc_1"] = creationGuard{LastAttempt: time.Now().Add(-19 * time.Minute).Format(time.RFC3339Nano)}
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"GitHub"}`)
	if status != http.StatusTooManyRequests || !strings.Contains(body, `"code":"CREATION_COOLDOWN"`) {
		t.Fatalf("manual creation during cooldown = %d: %s", status, body)
	}
}
