package server

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

func (m *autoTaskManager) loadLogs() {
	raw, err := os.ReadFile(m.logFile)
	if err == nil {
		_ = json.Unmarshal(raw, &m.logs)
	}
}

func (m *autoTaskManager) logLocked(taskID, level, format string, args ...any) error {
	entry := AliasTaskLog{
		ID: "log_" + uuid.New().String()[:8], TaskID: taskID,
		Time: time.Now().Format(time.RFC3339), Level: level,
		Message: fmt.Sprintf(format, args...),
	}
	result := append(m.logs[:len(m.logs):len(m.logs)], entry)
	if err := m.writeLogs(m.logFile, result); err != nil {
		return err
	}
	m.logs = result
	return nil
}

func (m *autoTaskManager) log(taskID, level, format string, args ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logLocked(taskID, level, format, args...)
}

func (m *autoTaskManager) listLogs() []AliasTaskLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AliasTaskLog, len(m.logs))
	for i := range m.logs {
		out[len(m.logs)-1-i] = m.logs[i]
	}
	return out
}
