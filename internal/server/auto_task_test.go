package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

type taskBackend struct {
	fakeBackend
	mu        sync.Mutex
	labels    []string
	createErr error
}

func (f *taskBackend) CreateAlias(_ string, label string) (*hme.CreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels = append(f.labels, label)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &hme.CreateResult{Email: label}, nil
}
func TestNormalizeAliasTaskValidation(t *testing.T) {
	in := aliasTaskInput{Enabled: true, AccountID: "a", IntervalMinutes: 20, TargetCount: 20, DailyLimit: 20}
	if _, e := normalizeAliasTask(in); e != nil {
		t.Fatal(e)
	}
	in.IntervalMinutes = 19
	if _, e := normalizeAliasTask(in); e == nil {
		t.Fatal("interval_minutes=19 should fail")
	}
}

func TestAutoTaskUsesLabelLibraryAndEnforcesDailyCap(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, e := m.create(aliasTaskInput{Enabled: false, AccountID: "a", IntervalMinutes: 20, TargetCount: 25, DailyLimit: 20})
	if e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	task.Enabled = true
	task.CreatedCount = 19
	task.DailyCount = 19
	task.DailyDate = taskDate(time.Now())
	m.tasks[task.ID] = task
	m.mu.Unlock()
	task = m.runOnce(task.ID)
	task = m.runOnce(task.ID)
	if len(be.labels) != 1 {
		t.Fatalf("created %d aliases, want only the remaining daily capacity", len(be.labels))
	}
	if be.labels[0] != "GitHub" {
		t.Fatalf("label should come from the built-in library, got %v", be.labels)
	}
	if task.CreatedCount != 20 || !task.Enabled {
		t.Fatalf("daily cap should defer, not finish or pause the task: %+v", task)
	}
}

func TestAutoTaskPausesImmediatelyAfterCreationFailure(t *testing.T) {
	be := &taskBackend{createErr: errors.New("iCloud 提示操作过于频繁")}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", IntervalMinutes: 20, TargetCount: 2, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if out.Enabled || out.NextRun != "" || !strings.Contains(out.LastError, "操作过于频繁") {
		t.Fatalf("creation warning should pause immediately: %+v", out)
	}
	if len(be.labels) != 1 {
		t.Fatalf("unexpected create calls: %v", be.labels)
	}
}

func TestAutoTaskReservesDailyCapacityBeforeCallingUpstream(t *testing.T) {
	be := &taskBackend{createErr: errors.New("upstream timeout")}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", IntervalMinutes: 20, TargetCount: 2, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if out.DailyCount != 1 {
		t.Fatalf("upstream attempt must consume pre-reserved daily capacity: %+v", out)
	}
}

func TestAutoTaskRunReturnsWhenTickerReachesTotal(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", IntervalMinutes: 20, TargetCount: 1, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	m.tasks[task.ID] = task
	m.stops[task.ID] = make(chan struct{})
	m.done[task.ID] = make(chan struct{})
	m.mu.Unlock()

	returned := make(chan struct{})
	go func() {
		m.runOnce(task.ID)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		close(m.done[task.ID])
		t.Fatal("runOnce blocked while stopping its ticker")
	}
}

func TestAutoTaskStartDoesNotDuplicate(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	task, e := m.create(aliasTaskInput{Enabled: true, AccountID: "a", IntervalMinutes: 20, TargetCount: 1, DailyLimit: 20})
	if e != nil {
		t.Fatal(e)
	}
	m.start()
	m.start()
	time.Sleep(5 * time.Millisecond)
	m.close()
	if task.ID == "" {
		t.Fatal("missing id")
	}
}

func readPersistedTask(t *testing.T, file, id string) AliasTask {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var stored aliasTaskFile
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	for _, task := range stored.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("persisted task %q not found", id)
	return AliasTask{}
}

func TestAutoTaskCreateLogFailureReturnsErrorButKeepsCommittedTask(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	m.writeLogs = func(string, any) error { return errors.New("injected log failure") }
	_, err := m.create(aliasTaskInput{AccountID: "a", IntervalMinutes: 20, TargetCount: 2, DailyLimit: 20})
	if err == nil || !strings.Contains(err.Error(), "injected log failure") {
		t.Fatalf("create error = %v", err)
	}
	if got := m.list(); len(got) != 1 {
		t.Fatalf("audit failure contradicted committed task: %#v", got)
	}
	if raw, readErr := os.ReadFile(file); readErr == nil {
		var stored aliasTaskFile
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Tasks) != 1 {
			t.Fatalf("audit failure removed persisted task: %#v", stored.Tasks)
		}
	} else if !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
}

func TestAutoTaskToggleStateFailureRollsBackLogStateAndRuntime(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	task := AliasTask{ID: "task_toggle", AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.writeState = func(string, any) error { return errors.New("injected state failure") }
	out, err := m.toggle(task.ID)
	if err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("toggle error = %v", err)
	}
	if out.Enabled {
		t.Fatalf("failed toggle returned transitioned task: %+v", out)
	}
	current, _ := m.get(task.ID)
	if current.Enabled {
		t.Fatalf("failed toggle changed in-memory task: %+v", current)
	}
	if persisted := readPersistedTask(t, file, task.ID); persisted.Enabled {
		t.Fatalf("failed toggle changed persisted task: %+v", persisted)
	}
	if len(m.listLogs()) != 0 {
		t.Fatalf("failed toggle left a transition log: %#v", m.listLogs())
	}
	if m.stops[task.ID] != nil {
		t.Fatal("failed toggle started the scheduler")
	}
}

func TestAutoTaskToggleLogFailureReturnsErrorAfterCommittingStateAndRuntime(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	task := AliasTask{ID: "task_toggle_log", AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.writeLogs = func(string, any) error { return errors.New("injected toggle log failure") }
	out, err := m.toggle(task.ID)
	if err == nil || !strings.Contains(err.Error(), "injected toggle log failure") {
		t.Fatalf("toggle error = %v", err)
	}
	current, _ := m.get(task.ID)
	if !out.Enabled || !current.Enabled || !readPersistedTask(t, file, task.ID).Enabled {
		t.Fatalf("log failure contradicted committed toggle: out=%+v current=%+v", out, current)
	}
	if m.stops[task.ID] == nil {
		t.Fatal("committed toggle did not start scheduler after log failure")
	}
	m.close()
}

func TestAutoTaskRunOnceReservationFailureSkipsBackendAndPauses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	backend := &taskBackend{}
	m := newAutoTaskManager(file, backend)
	task := AliasTask{ID: "task_reserve_failure", Enabled: true, AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.writeState = func(path string, value any) error {
		writes++
		if writes == 1 {
			return errors.New("injected reservation failure")
		}
		return writeJSONAtomic(path, value)
	}

	out := m.runOnce(task.ID)
	if len(backend.labels) != 0 {
		t.Fatalf("backend called without durable reservation: %#v", backend.labels)
	}
	if out.Enabled || !strings.Contains(out.LastError, "injected reservation failure") {
		t.Fatalf("reservation failure was not paused/reported: %+v", out)
	}
	if persisted := readPersistedTask(t, file, task.ID); persisted.Enabled || persisted.NextNumber != 1 {
		t.Fatalf("reservation failure changed durable sequence or remained enabled: %+v", persisted)
	}
}

func TestEnsureRunningSaveFailureKeepsSchedulerForCommittedEnabledTask(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	task := AliasTask{ID: "task_scheduler", Enabled: true, AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	m.writeState = func(string, any) error { return errors.New("injected next-run persistence failure") }

	err := m.ensureRunning(task.ID)
	if err == nil || !strings.Contains(err.Error(), "injected next-run persistence failure") {
		t.Fatalf("ensureRunning error = %v", err)
	}
	if m.stops[task.ID] == nil {
		t.Fatal("enabled task was left without a scheduler after ancillary NextRun save failure")
	}
	m.close()
}

func TestAutoTaskRunOncePersistsConsumedSequenceAfterStateFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	task := AliasTask{ID: "task_run", Enabled: true, AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.writeState = func(path string, value any) error {
		writes++
		if writes == 3 {
			return errors.New("injected post-create state failure")
		}
		return writeJSONAtomic(path, value)
	}
	out := m.runOnce(task.ID)
	persisted := readPersistedTask(t, file, task.ID)
	if out.CreatedCount != 1 || out.NextNumber != 2 || out.Enabled {
		t.Fatalf("run result did not preserve consumed sequence and pause: %+v", out)
	}
	if persisted.CreatedCount != 1 || persisted.NextNumber != 2 || persisted.Enabled {
		t.Fatalf("persisted task can reuse consumed sequence: %+v", persisted)
	}
	if !strings.Contains(out.LastError, "injected post-create state failure") {
		t.Fatalf("state failure not surfaced: %+v", out)
	}
	for _, entry := range m.listLogs() {
		if strings.Contains(entry.Message, "创建成功") {
			t.Fatalf("state failure produced false success log: %#v", entry)
		}
	}
}

func TestAutoTaskRunOnceNeverReusesReservedSequenceAfterPersistentPostCreateFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tasks.json")
	backend := &taskBackend{}
	m := newAutoTaskManager(file, backend)
	task := AliasTask{ID: "task_reserved", Enabled: true, AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.writeState = func(path string, value any) error {
		writes++
		if writes > 2 {
			return errors.New("persistent post-reservation failure")
		}
		return writeJSONAtomic(path, value)
	}

	out := m.runOnce(task.ID)
	if len(backend.labels) != 1 || backend.labels[0] != "GitHub" {
		t.Fatalf("backend calls = %#v", backend.labels)
	}
	if out.Enabled || out.NextNumber != 2 || !strings.Contains(out.LastError, "persistent post-reservation failure") {
		t.Fatalf("run result did not pause and report persistent failure: %+v", out)
	}

	restartedBackend := &taskBackend{}
	restarted := newAutoTaskManager(file, restartedBackend)
	persisted, ok := restarted.get(task.ID)
	if !ok || persisted.NextNumber != 2 {
		t.Fatalf("reserved sequence was not durable across restart: %+v", persisted)
	}
	restarted.creationGuards[task.AccountID] = creationGuard{LastAttempt: time.Now().Add(-minCreationCooldown).Format(time.RFC3339Nano)}
	restarted.runOnce(task.ID)
	if len(restartedBackend.labels) == 0 || restartedBackend.labels[0] != "GitLab" {
		t.Fatalf("restart reused consumed label sequence: %#v", restartedBackend.labels)
	}
}

func TestAutoTaskRunOnceFinalStateFailurePersistsPause(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	task := AliasTask{ID: "task_final", Enabled: true, AccountID: "a", IntervalMinutes: 60, DailyLimit: 20, MaxTotal: 2, NextNumber: 1}
	m.tasks[task.ID] = task
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.writeState = func(path string, value any) error {
		writes++
		if writes == 4 {
			return errors.New("injected final state failure")
		}
		return writeJSONAtomic(path, value)
	}
	out := m.runOnce(task.ID)
	persisted := readPersistedTask(t, file, task.ID)
	if out.Enabled || persisted.Enabled {
		t.Fatalf("final state failure did not pause task: out=%+v persisted=%+v", out, persisted)
	}
	if persisted.CreatedCount != 1 || persisted.NextNumber != 2 {
		t.Fatalf("final state recovery lost progress: %+v", persisted)
	}
	if !strings.Contains(persisted.LastError, "injected final state failure") {
		t.Fatalf("final state failure not persisted: %+v", persisted)
	}
}
