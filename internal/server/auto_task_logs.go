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
//
// 2000 条: 自动任务 500/日 + 批量/清理摘要几十条 → 约 3-4 周保留期;
// 配合「成功项聚合、失败才逐条」的写入策略, 重写成本可接受。
const maxTaskLogEntries = 2000

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
	return m.logLockedWithCategory("", taskID, level, format, args...)
}

// logLockedWithCategory 带来源类别的日志写入(调用方持锁)。
func (m *autoTaskManager) logLockedWithCategory(category, taskID, level, format string, args ...any) error {
	entry := AliasTaskLog{
		ID: "log_" + uuid.New().String()[:8], TaskID: taskID,
		Time: time.Now().Format(time.RFC3339), Level: level,
		Message:  fmt.Sprintf(format, args...),
		Category: category,
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

// logWithCategory 写带来源类别的日志(供批量操作与清理任务标注来源)。
func (m *autoTaskManager) logWithCategory(category, taskID, level, format string, args ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logLockedWithCategory(category, taskID, level, format, args...)
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
// 返回 (删除数, 剩余数, error)。会话路径不受范围限制。
func (m *autoTaskManager) cleanupLogs(olderThanDays int) (int, int, error) {
	return m.cleanupLogsScoped(olderThanDays, nil, false)
}

// cleanupLogsScoped 按账号白名单清理日志。
//
//	restricted=false → 清理全部账号(会话/空范围秘钥), remaining 为全局日志数;
//	restricted=true  → 只清理归属账号在白名单内的日志, 范围外账号的日志一律
//	                   保留(防止受限秘钥越权抹除范围外账号的活动痕迹), 且
//	                   remaining **只统计范围内的日志**——否则响应里的全局
//	                   剩余数会构成范围外计数的侧信道(审计 A3)。
//
// 归属判定经 TaskID → 任务 AccountID; 归属未知(任务已删)的日志保守保留,
// 也不计入受限秘钥的 remaining。
func (m *autoTaskManager) cleanupLogsScoped(olderThanDays int, scope []string, restricted bool) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().AddDate(0, 0, -olderThanDays)
	inScope := func(entry AliasTaskLog) bool {
		if !restricted {
			return true
		}
		tk, ok := m.tasks[entry.TaskID]
		return ok && containsString(scope, tk.AccountID)
	}
	kept := make([]AliasTaskLog, 0, len(m.logs))
	deleted := 0
	remaining := 0
	for _, entry := range m.logs {
		if !inScope(entry) {
			kept = append(kept, entry) // 范围外/归属未知: 保留, 且不计数
			continue
		}
		ts, err := time.Parse(time.RFC3339, entry.Time)
		if err != nil {
			// 时间非法: 保守保留
			kept = append(kept, entry)
			remaining++
			continue
		}
		if ts.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, entry)
		remaining++
	}
	if deleted == 0 {
		return 0, remaining, nil
	}
	if err := m.writeLogs(m.logFile, kept); err != nil {
		return 0, remaining, err
	}
	m.logs = kept
	return deleted, remaining, nil
}
