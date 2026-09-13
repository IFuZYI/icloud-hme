package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoTaskLogReturnsPersistenceError(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "logs.json")
	m := newAutoTaskManager(filepath.Join(dir, "tasks.json"), &fakeBackend{})
	m.logFile = logFile
	if err := m.log("task_1", "info", "persisted"); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}

	parentFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.logFile = filepath.Join(parentFile, "logs.json")

	if err := m.log("task_1", "info", "should not persist"); err == nil {
		t.Fatal("log should return the persistence error")
	}
	logs := m.listLogs()
	if len(logs) != 1 || logs[0].Message != "persisted" {
		t.Fatalf("failed persistence should not change in-memory logs: %#v", logs)
	}
	after, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(persisted) {
		t.Fatalf("failed persistence changed the existing log file: before=%q after=%q", persisted, after)
	}
}
