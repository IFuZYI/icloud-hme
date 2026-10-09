package server

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
)

// maxTaskLogEntries 限制审计日志在内存与磁盘上的最大条目数。日志仅供排查，
// 无需无限保留；封顶可避免长期运行时内存持续增长，以及每写一条就重写整份
// 越来越大的 JSON 文件。
const maxTaskLogEntries = 500

func (m *autoTaskManager) loadLogs() {
	raw, err := os.ReadFile(m.logFile)
	if err == nil {
		_ = json.Unmarshal(raw, &m.logs)
	}
	if len(m.logs) > maxTaskLogEntries {
		m.logs = append([]AliasTaskLog(nil), m.logs[len(m.logs)-maxTaskLogEntries:]...)
	}
}

func (m *autoTaskManager) logLocked(taskID, level, format string, args ...any) error {
	entry := AliasTaskLog{
		ID: "log_" + uuid.New().String()[:8], TaskID: taskID,
		Time: time.Now().Format(time.RFC3339), Level: level,
		Message: fmt.Sprintf(format, args...),
	}
	result := append(m.logs[:len(m.logs):len(m.logs)], entry)
	if len(result) > maxTaskLogEntries {
		result = result[len(result)-maxTaskLogEntries:]
	}
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

// cleanupLogs 删除严格早于「now - olderThanDays 天」的日志。
//
// 语义与边界:
//   - 恰好等于 cutoff 的条目保留(「1 天前」= 保留最近 24 小时);
//   - 时间无法解析的条目保守保留(无法证明过期, 不误删);
//   - 没有可清理条目时不重写文件;
//   - 落盘失败时内存不变并返回错误(与 logLocked 一致: 先写盘成功才更新内存)。
//
// 返回 (删除数, 剩余数, error)。
func (m *autoTaskManager) cleanupLogs(olderThanDays int) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().AddDate(0, 0, -olderThanDays)
	kept := make([]AliasTaskLog, 0, len(m.logs))
	deleted := 0
	for _, entry := range m.logs {
		ts, err := time.Parse(time.RFC3339, entry.Time)
		if err != nil {
			// 时间非法: 保守保留
			kept = append(kept, entry)
			continue
		}
		if ts.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, entry)
	}
	if deleted == 0 {
		return 0, len(m.logs), nil
	}
	if err := m.writeLogs(m.logFile, kept); err != nil {
		return 0, len(m.logs), err
	}
	m.logs = kept
	return deleted, len(kept), nil
}
