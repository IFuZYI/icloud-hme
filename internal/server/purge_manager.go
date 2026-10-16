// Package server - 邮件清理任务管理器。
//
// 一次清理 = 一条 PurgeTask(无论多少封邮件), 异步执行:
//   - 按账号并发: 同账号同时最多 1 个任务, 不同账号可并行;
//   - 分块执行(每块 chunkSize 封), 块间节流 + 抖动, 每小时块数封顶;
//   - 断点续传: 记录 (folder, uid 偏移), 暂停/失败后可继续;
//   - 持久化 purge_tasks.json(原子写, 保留最近 50 条), 重启后运行中任务
//     标记 interrupted(可 resume)。
package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
)

// ====================================================================
// 参数(可注入, 测试覆盖)
// ====================================================================

var (
	// purgeChunkSize 是每块的邮件数。
	purgeChunkSize = 500
	// purgeChunkDelay 是块间基础间隔; 实际间隔再加 0~purgeChunkJitter 的抖动。
	purgeChunkDelay = 3 * time.Second
	// purgeChunkJitter 是块间抖动上限(随机 0~该值), 避免固定节奏特征。
	purgeChunkJitter = 2 * time.Second
	// purgeHourlyChunkCap 是滚动 1 小时内最多执行的块数; 达到后转 throttled。
	purgeHourlyChunkCap = 20
	// purgeKeepTasks 是保留的任务记录条数。
	purgeKeepTasks = 50
	// purgeFolders 是清理覆盖的文件夹(与读信口径一致)。
	purgeFolders = []string{"INBOX", "Junk", "垃圾邮件"}
)

// ====================================================================
// 数据模型
// ====================================================================

// PurgeTask 是一次邮件清理任务的完整状态。
type PurgeTask struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name,omitempty"`
	RangeStart  string `json:"range_start"`
	RangeEnd    string `json:"range_end"`
	// Status: running|throttled|paused|done|failed|cancelled|interrupted
	Status string `json:"status"`
	// Total 是预览确定的待删总数; Processed 是已处理数。
	Total     int `json:"total"`
	Processed int `json:"processed"`
	Deleted   int `json:"deleted"`
	Failed    int `json:"failed"`
	// CurrentMessage 是当前块的描述(如 "INBOX · 500 封")。
	CurrentMessage string `json:"current_message,omitempty"`
	// 断点: 当前处理到的文件夹与块序号(块级断点)。
	FolderCursor string `json:"folder_cursor,omitempty"`
	ChunkCursor  int    `json:"chunk_cursor,omitempty"`
	Chunks       int    `json:"chunks,omitempty"`
	// HourlyChunks 记录最近 1 小时内的块时刻(节流窗口判定)。
	HourlyChunks []string `json:"hourly_chunks,omitempty"`
	StartedAt    string   `json:"started_at"`
	FinishedAt   string   `json:"finished_at,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// IsActive 判断任务是否仍在进行(占用账号锁)。
func (t PurgeTask) IsActive() bool {
	switch t.Status {
	case "running", "throttled", "paused":
		return true
	}
	return false
}

// ====================================================================
// 管理器
// ====================================================================

// purgeManager 管理清理任务: 状态机 + 按账号互斥 + 异步执行。
type purgeManager struct {
	mu     sync.Mutex
	tasks  map[string]*PurgeTask
	order  []string // 创建顺序(用于保留策略)
	file   string
	active map[string]string // accountID → 活跃任务 ID
	// now 可注入(测试确定化)。
	now func() time.Time
	// sleep 可注入(测试跳过块间等待)。
	sleep func(time.Duration)
	// withClient 提供账号的 IMAP 客户端(生产 = account.Manager.WithMailClient)。
	// 为 nil 时执行器报错(测试须注入)。
	withClient func(accountID string, fn func(*mail.Client) error) error
	// logFn 写审计日志(可空)。
	logFn func(taskID, level, format string, args ...any)
	// stopped 标记管理器已关闭(测试清理)。
	stopped bool
}

// newPurgeManager 建管理器; file 为空 = 纯内存。
func newPurgeManager(file string, withClient func(string, func(*mail.Client) error) error) (*purgeManager, error) {
	m := &purgeManager{
		tasks:      map[string]*PurgeTask{},
		file:       file,
		active:     map[string]string{},
		now:        time.Now,
		sleep:      time.Sleep,
		withClient: withClient,
	}
	if file != "" {
		if err := m.load(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// load 读取持久化任务; 运行中的任务标记 interrupted(重启中断)。
func (m *purgeManager) load() error {
	raw, err := os.ReadFile(m.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var wrapper struct {
		Tasks []*PurgeTask `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("解析清理任务文件: %w", err)
	}
	for _, t := range wrapper.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		// 运行中的任务在重启后无法继续执行(进程内的 runner 已消失):
		// 标记 interrupted, 用户可显式 resume。
		if t.Status == "running" || t.Status == "throttled" {
			t.Status = "interrupted"
		}
		m.tasks[t.ID] = t
		m.order = append(m.order, t.ID)
	}
	return nil
}

// saveLocked 原子写盘(调用方持锁)。纯内存模式为空操作。
func (m *purgeManager) saveLocked() error {
	if m.file == "" {
		return nil
	}
	tasks := make([]*PurgeTask, 0, len(m.order))
	for _, id := range m.order {
		if t, ok := m.tasks[id]; ok {
			tasks = append(tasks, t)
		}
	}
	return writeJSONAtomic(m.file, map[string]any{"tasks": tasks})
}

// create 创建任务。同账号已有活跃任务时返回错误。
func (m *purgeManager) create(accountID, accountName string, start, end time.Time) (PurgeTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.active[accountID]; busy {
		return PurgeTask{}, fmt.Errorf("该账号已有清理任务在运行")
	}
	// 清理任务不重复: 已存在活跃任务的账号直接拒绝(即使 active 表因重启为空,
	// 也检查任务表——双保险)。
	for _, id := range m.order {
		t := m.tasks[id]
		if t.AccountID == accountID && t.IsActive() {
			return PurgeTask{}, fmt.Errorf("该账号已有清理任务在运行")
		}
	}
	tk := &PurgeTask{
		ID:          "purge_" + randomHexToken(8),
		AccountID:   accountID,
		AccountName: accountName,
		RangeStart:  start.Format(time.RFC3339),
		RangeEnd:    end.Format(time.RFC3339),
		Status:      "running",
		StartedAt:   m.now().Format(time.RFC3339),
	}
	m.tasks[tk.ID] = tk
	m.order = append(m.order, tk.ID)
	m.active[accountID] = tk.ID
	m.trimLocked()
	if err := m.saveLocked(); err != nil {
		delete(m.tasks, tk.ID)
		m.order = m.order[:len(m.order)-1]
		delete(m.active, accountID)
		return PurgeTask{}, err
	}
	return *tk, nil
}

// trimLocked 保留最近 purgeKeepTasks 条(活跃任务永不裁剪)。
func (m *purgeManager) trimLocked() {
	if len(m.order) <= purgeKeepTasks {
		return
	}
	keep := make([]string, 0, purgeKeepTasks)
	// 从新到旧保留; 活跃任务无条件保留。
	for i := len(m.order) - 1; i >= 0 && len(keep) < purgeKeepTasks; i-- {
		id := m.order[i]
		if t, ok := m.tasks[id]; ok {
			keep = append(keep, id)
			_ = t
		}
	}
	// 反转为旧→新顺序。
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	kept := map[string]bool{}
	for _, id := range keep {
		kept[id] = true
	}
	for id := range m.tasks {
		if !kept[id] {
			delete(m.tasks, id)
		}
	}
	m.order = keep
}

// get 取单个任务。
func (m *purgeManager) get(id string) (PurgeTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return PurgeTask{}, false
	}
	return *t, true
}

// list 返回全部任务(新→旧)。
func (m *purgeManager) list() []PurgeTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PurgeTask, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if t, ok := m.tasks[m.order[i]]; ok {
			out = append(out, *t)
		}
	}
	return out
}

// update 应用变更(持锁快照 + 写盘)。
func (m *purgeManager) update(id string, fn func(*PurgeTask)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	fn(t)
	return m.saveLocked()
}

// finish 结束任务并释放账号锁。
func (m *purgeManager) finish(id, status, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return
	}
	t.Status = status
	if errMsg != "" {
		t.Error = errMsg
	}
	if status == "done" || status == "failed" || status == "cancelled" {
		t.FinishedAt = m.now().Format(time.RFC3339)
		delete(m.active, t.AccountID)
	}
	_ = m.saveLocked()
}

// pause 暂停任务(保留账号锁, 可 resume)。
func (m *purgeManager) pause(id string) error {
	return m.update(id, func(t *PurgeTask) {
		if t.Status == "running" || t.Status == "throttled" {
			t.Status = "paused"
		}
	})
}

// resume 继续暂停/中断的任务。
func (m *purgeManager) resume(id string) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	switch t.Status {
	case "paused", "interrupted":
		t.Status = "running"
		m.active[t.AccountID] = id
	default:
		m.mu.Unlock()
		return fmt.Errorf("任务状态 %s 不可继续", t.Status)
	}
	err := m.saveLocked()
	m.mu.Unlock()
	return err
}

// cancel 取消任务并释放账号锁。
func (m *purgeManager) cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("任务不存在")
	}
	if t.Status == "done" || t.Status == "failed" || t.Status == "cancelled" {
		return nil
	}
	t.Status = "cancelled"
	t.FinishedAt = m.now().Format(time.RFC3339)
	delete(m.active, t.AccountID)
	return m.saveLocked()
}

// setTotalFromPreview 用一次 SEARCH 统计待删总数, 写入 Total(进度条分母)。
// 失败时不阻塞任务(Total 保持 0, 前端显示不确定进度)。
func (m *purgeManager) setTotalFromPreview(taskID string, start, end time.Time) {
	if m.withClient == nil {
		return
	}
	tk, ok := m.get(taskID)
	if !ok {
		return
	}
	total := 0
	err := m.withClient(tk.AccountID, func(c *mail.Client) error {
		for _, folder := range purgeFolders {
			uids, missing, e := c.SearchRange(folder, mail.DateRange{Start: start, End: end})
			if e != nil {
				return e
			}
			if missing {
				continue
			}
			total += len(uids)
		}
		return nil
	})
	if err != nil {
		return
	}
	_ = m.update(taskID, func(t *PurgeTask) { t.Total = total })
}

// randomHexToken 生成 n 字节的随机 hex(任务 ID 用)。
func randomHexToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())[:n*2]
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

// accountNames 解析账号显示名(供列表展示)。
func (m *purgeManager) accountNames(accounts []account.Summary) map[string]string {
	names := map[string]string{}
	for _, a := range accounts {
		names[a.ID] = a.Name
	}
	return names
}

// sortTasksByTime 供测试与列表稳定排序(新→旧)。
func sortTasksByTime(tasks []PurgeTask) {
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].StartedAt > tasks[j].StartedAt
	})
}
