package server

import (
	"fmt"
	"math/rand"
	"time"

	"icloud-hme/internal/mail"
)

// ====================================================================
// 清理任务执行器: 分块节流删除
// ====================================================================
//
// 执行模型:
//   - 任务创建后由 run(taskID) 异步驱动(goroutine);
//   - 每个文件夹: 先 SEARCH 取全量 UID(一次往返), 再逐块删除;
//   - 每块 = 1×SELECT + 1×STORE + 1×EXPUNGE; 块间 sleep(purgeChunkDelay + 抖动);
//   - 滚动 1 小时块数超过 purgeHourlyChunkCap → throttled, 睡到窗口滚动后继续;
//   - 每块完成后更新任务计数(前端轮询可见), 并失效账号读缓存;
//   - 失败: 单块重试 1 次, 仍失败 → paused(记录错误), resume 从块断点继续;
//   - cancel 检查点: 每块开始前检查任务状态, cancelled 即停止。
//
// 断点续传实现: 恢复时重新 SEARCH——已删 UID 不再返回, 天然续传;
// ChunkCursor 仅作展示(记录进度用), 不参与跳过逻辑。

// run 异步执行任务(单飞: 同一任务同时只允许 1 个 runner)。
//
// 双 runner 会让同账号的 IMAP 命令数近乎翻倍, 击穿分块节流的风控设计,
// 因此持锁检查 + 置位 running 标记, 已在跑则直接返回; goroutine 退出时清位。
func (m *purgeManager) run(taskID string) {
	if m.withClient == nil {
		m.finish(taskID, "failed", "清理执行器未配置 IMAP 客户端")
		return
	}
	m.mu.Lock()
	if m.running[taskID] {
		m.mu.Unlock()
		return
	}
	m.running[taskID] = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, taskID)
			m.mu.Unlock()
		}()
		m.runSync(taskID)
	}()
}

// runSync 同步执行(测试直接调用; 生产由 run 起 goroutine)。
// 注意: 不经单飞守卫——测试需要直接驱动执行体; 生产路径一律走 run。
func (m *purgeManager) runSync(taskID string) {
	tk, ok := m.get(taskID)
	if !ok {
		return
	}
	start, _ := time.Parse(time.RFC3339, tk.RangeStart)
	end, _ := time.Parse(time.RFC3339, tk.RangeEnd)
	r := mail.DateRange{Start: start, End: end}

	// 从断点继续: folder 游标 + 块游标。
	folders := purgeFolders
	startFolder := 0
	if tk.FolderCursor != "" {
		for i, f := range folders {
			if f == tk.FolderCursor {
				startFolder = i
				break
			}
		}
	}

	total := tk.Total
	processed := tk.Processed
	deleted := tk.Deleted
	failed := tk.Failed
	chunkIdx := tk.ChunkCursor

	for fi := startFolder; fi < len(folders); fi++ {
		folder := folders[fi]
		// 每文件夹: 取全量 UID(一次 SEARCH; 带操作预算防 IMAP 卡死)。
		var uids []uint32
		var missing bool
		err := withIMAPTimeout(purgeOpTimeout, func() error {
			return m.withClient(tk.AccountID, func(c *mail.Client) error {
				var e error
				uids, missing, e = c.SearchRange(folder, r)
				return e
			})
		})
		if err != nil {
			m.pauseWithError(taskID, fmt.Sprintf("搜索 %s 失败: %v", folder, err))
			return
		}
		if missing {
			m.progress(taskID, folder, 0, processed, deleted, failed, 0, fmt.Sprintf("%s: 文件夹不存在, 跳过", folder))
			continue
		}

		// 本文件夹的块计划。
		chunkSize := purgeChunkSize
		if chunkSize <= 0 {
			chunkSize = mail.DefaultPurgeChunkSize
		}

		for start := 0; start < len(uids); start += chunkSize {
			endIdx := start + chunkSize
			if endIdx > len(uids) {
				endIdx = len(uids)
			}
			chunk := uids[start:endIdx]

			// cancel 检查点。
			if cur, ok := m.get(taskID); !ok || cur.Status == "cancelled" || cur.Status == "paused" {
				return
			}

			// 节流窗口: 滚动 1 小时块数封顶。
			if wait := m.throttleWait(taskID); wait > 0 {
				m.setStatus(taskID, "throttled", fmt.Sprintf("风控节流中, 约 %s 后继续", wait.Round(time.Second)))
				m.sleep(wait)
				// 等待期间用户可能暂停/取消: 两者都不得被覆盖为 running,
				// 否则「暂停」被吞、删除继续(风控节流失效)。
				// 只有仍处于 throttled/running 时才恢复 running。
				cur, ok := m.get(taskID)
				if !ok || (cur.Status != "throttled" && cur.Status != "running") {
					return
				}
				m.setStatus(taskID, "running", "")
			}

			// 执行一块(失败重试 1 次; 每次调用带操作预算防 IMAP 卡死)。
			var chunkErr error
			for attempt := 0; attempt < 2; attempt++ {
				chunkErr = withIMAPTimeout(purgeOpTimeout, func() error {
					return m.withClient(tk.AccountID, func(c *mail.Client) error {
						return c.DeleteUIDChunk(folder, chunk)
					})
				})
				if chunkErr == nil {
					break
				}
				if attempt == 0 {
					m.sleep(2 * time.Second) // 重试前短暂退避
				}
			}
			m.recordChunk(taskID)
			if chunkErr != nil {
				// 重试后仍失败: 计入失败数(前端展示「失败 N」), 再暂停。
				failed += len(chunk)
				m.progress(taskID, folder, chunkIdx, processed, deleted, failed, 0,
					fmt.Sprintf("%s: 删除失败 %d 封", folder, len(chunk)))
				m.pauseWithError(taskID, fmt.Sprintf("删除 %s 块失败: %v", folder, chunkErr))
				return
			}

			processed += len(chunk)
			deleted += len(chunk)
			m.progress(taskID, folder, chunkIdx, processed, deleted, failed, len(chunk),
				fmt.Sprintf("%s: 删除 %d 封", folder, len(chunk)))
			// 删除后读缓存立即失效(否则 30s 收件箱/5min 摘要缓存仍展示已删邮件)。
			if m.invalidateReads != nil {
				m.invalidateReads(tk.AccountID)
			}

			// 块间节流(最后一块不睡)。
			if endIdx < len(uids) {
				delay := purgeChunkDelay
				if purgeChunkJitter > 0 {
					delay += time.Duration(rand.Int63n(int64(purgeChunkJitter)))
				}
				m.sleep(delay)
			}
			chunkIdx = 0 // 断点游标仅作展示; 续传靠重新 SEARCH
		}
		chunkIdx = 0
	}

	// 全部完成。
	tkNow, _ := m.get(taskID)
	_ = tkNow
	m.finish(taskID, "done", "")
	m.log(taskID, "info", "清理完成: 删除 %d 封(共处理 %d)", deleted, processed)
	_ = total
}

// throttleWait 计算节流等待时长; 0 = 不需等待。
//
// 滚动窗口: 最近 1 小时内执行的块数 ≥ purgeHourlyChunkCap 时, 等到最早
// 一块滑出窗口。
func (m *purgeManager) throttleWait(taskID string) time.Duration {
	if purgeHourlyChunkCap <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return 0
	}
	now := m.now()
	cutoff := now.Add(-time.Hour)
	var recent []time.Time
	for _, s := range t.HourlyChunks {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			continue
		}
		if ts.After(cutoff) {
			recent = append(recent, ts)
		}
	}
	if len(recent) < purgeHourlyChunkCap {
		return 0
	}
	// 等到最早一块滑出窗口(加 1s 余量)。
	earliest := recent[0]
	for _, ts := range recent[1:] {
		if ts.Before(earliest) {
			earliest = ts
		}
	}
	return earliest.Add(time.Hour).Sub(now) + time.Second
}

// recordChunk 记录一块的执行时刻(节流窗口用)。
func (m *purgeManager) recordChunk(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return
	}
	now := m.now()
	// 只保留最近 2 小时的时刻(窗口只需 1 小时)。
	cutoff := now.Add(-2 * time.Hour)
	kept := make([]string, 0, len(t.HourlyChunks)+1)
	for _, s := range t.HourlyChunks {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err == nil && ts.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, now.Format(time.RFC3339Nano))
	t.HourlyChunks = kept
	t.Chunks++
	_ = m.saveLocked()
}

// progress 更新进度(每块完成时)。
func (m *purgeManager) progress(taskID, folder string, chunkCursor, processed, deleted, failed, _ int, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return
	}
	t.Processed = processed
	t.Deleted = deleted
	t.Failed = failed
	t.FolderCursor = folder
	t.ChunkCursor = chunkCursor
	if msg != "" {
		t.CurrentMessage = msg
	}
	_ = m.saveLocked()
}

// setStatus 更新状态与提示信息。
func (m *purgeManager) setStatus(taskID, status, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return
	}
	t.Status = status
	if msg != "" {
		t.CurrentMessage = msg
	}
	_ = m.saveLocked()
}

// pauseWithError 失败暂停(记录错误, 保留账号锁待 resume)。
func (m *purgeManager) pauseWithError(taskID, errMsg string) {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return
	}
	t.Status = "paused"
	t.Error = errMsg
	_ = m.saveLocked()
	m.mu.Unlock()
	m.log(taskID, "error", "%s", errMsg)
}

// log 写审计日志(可空实现)。
func (m *purgeManager) log(taskID, level, format string, args ...any) {
	if m.logFn == nil {
		return
	}
	m.logFn(taskID, level, format, args...)
}
