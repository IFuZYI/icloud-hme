package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const maxTaskTotal = 999

type AliasTask struct {
	ID              string `json:"id"`
	Enabled         bool   `json:"enabled"`
	AccountID       string `json:"account_id"`
	IntervalMinutes int    `json:"interval_minutes"`
	BatchCount      int    `json:"batch_count"`
	MaxTotal        int    `json:"max_total"`
	CreatedCount    int    `json:"created_count"`
	LabelPrefix     string `json:"label_prefix"`
	NextNumber      int    `json:"next_number"`
	LastRun         string `json:"last_run,omitempty"`
	LastSuccess     int    `json:"last_success"`
	LastError       string `json:"last_error,omitempty"`
	NextRun         string `json:"next_run,omitempty"`
}

type aliasTaskInput struct {
	Enabled         bool   `json:"enabled"`
	AccountID       string `json:"account_id"`
	IntervalMinutes int    `json:"interval_minutes"`
	BatchCount      int    `json:"batch_count"`
	MaxTotal        int    `json:"max_total"`
	LabelPrefix     string `json:"label_prefix"`
}
type aliasTaskFile struct {
	Tasks []AliasTask `json:"tasks"`
}

type AliasTaskLog struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type autoTaskManager struct {
	mu       sync.Mutex
	tasks    map[string]AliasTask
	file     string
	backend  Backend
	stops    map[string]chan struct{}
	done     map[string]chan struct{}
	creating map[string]bool
	logs     []AliasTaskLog
	logFile  string
}

func normalizeAliasTask(in aliasTaskInput) (AliasTask, error) {
	if strings.TrimSpace(in.AccountID) == "" {
		return AliasTask{}, fmt.Errorf("account_id 必填")
	}
	if in.IntervalMinutes < 1 || in.IntervalMinutes > 10080 {
		return AliasTask{}, fmt.Errorf("interval_minutes 必须为 1-10080")
	}
	if in.BatchCount < 1 || in.BatchCount > maxTaskTotal {
		return AliasTask{}, fmt.Errorf("batch_count 必须为 1-%d", maxTaskTotal)
	}
	if in.MaxTotal < 1 || in.MaxTotal > maxTaskTotal {
		return AliasTask{}, fmt.Errorf("max_total 必须为 1-%d", maxTaskTotal)
	}
	prefix := strings.TrimSpace(in.LabelPrefix)
	if prefix == "" {
		return AliasTask{}, fmt.Errorf("label_prefix 必填")
	}
	if len([]rune(prefix)) > 180 {
		return AliasTask{}, fmt.Errorf("label_prefix 不能超过 180 个字符")
	}
	return AliasTask{ID: "task_" + uuid.New().String()[:8], Enabled: in.Enabled, AccountID: strings.TrimSpace(in.AccountID), IntervalMinutes: in.IntervalMinutes, BatchCount: in.BatchCount, MaxTotal: in.MaxTotal, LabelPrefix: prefix, NextNumber: 1}, nil
}
func newAutoTaskManager(file string, backend Backend) *autoTaskManager {
	m := &autoTaskManager{file: file, logFile: filepath.Join(filepath.Dir(file), "alias_task_logs.json"), backend: backend, tasks: map[string]AliasTask{}, stops: map[string]chan struct{}{}, done: map[string]chan struct{}{}, creating: map[string]bool{}}
	m.load()
	m.loadLogs()
	return m
}
func (m *autoTaskManager) load() {
	raw, e := os.ReadFile(m.file)
	if e != nil {
		return
	}
	var f aliasTaskFile
	if json.Unmarshal(raw, &f) == nil {
		for _, t := range f.Tasks {
			if t.ID == "" {
				t.ID = "task_" + uuid.New().String()[:8]
			}
			if t.NextNumber < 1 {
				t.NextNumber = 1
			}
			m.tasks[t.ID] = t
		}
	}
}
func (m *autoTaskManager) saveLocked() error {
	return writeJSONAtomic(m.file, aliasTaskFile{Tasks: m.taskListLocked()})
}
func (m *autoTaskManager) taskListLocked() []AliasTask {
	out := make([]AliasTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	return out
}
func (m *autoTaskManager) list() []AliasTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskListLocked()
}
func (m *autoTaskManager) get(id string) (AliasTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	return t, ok
}
func (m *autoTaskManager) create(in aliasTaskInput) (AliasTask, error) {
	t, e := normalizeAliasTask(in)
	if e != nil {
		return t, e
	}
	t.Enabled = true
	t.NextRun = time.Now().Add(10 * time.Second).Format(time.RFC3339)
	m.mu.Lock()
	e = m.saveAddLocked(t)
	if e == nil {
		m.logLocked(t.ID, "info", "创建任务，等待 10 秒后开始")
	}
	m.mu.Unlock()
	if e == nil {
		m.scheduleStart(t.ID)
	}
	return t, e
}

func (m *autoTaskManager) scheduleStart(id string) {
	go func() {
		<-time.After(10 * time.Second)
		m.runOnce(id)
		m.ensureRunning(id)
	}()
}
func (m *autoTaskManager) saveAddLocked(t AliasTask) error { m.tasks[t.ID] = t; return m.saveLocked() }
func (m *autoTaskManager) update(id string, in aliasTaskInput) (AliasTask, error) {
	n, e := normalizeAliasTask(in)
	if e != nil {
		return n, e
	}
	m.stop(id)
	m.mu.Lock()
	old, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return AliasTask{}, fmt.Errorf("任务不存在")
	}
	n.ID = id
	n.Enabled = true
	n.NextNumber = old.NextNumber
	n.CreatedCount = old.CreatedCount
	n.LastRun = old.LastRun
	n.LastSuccess = old.LastSuccess
	n.LastError = old.LastError
	n.NextRun = old.NextRun
	e = m.saveAddLocked(n)
	m.mu.Unlock()
	if e == nil && n.Enabled && n.CreatedCount < n.MaxTotal {
		m.ensureRunning(id)
	}
	return n, e
}
func (m *autoTaskManager) remove(id string) error {
	m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[id]; !ok {
		return fmt.Errorf("任务不存在")
	}
	delete(m.tasks, id)
	return m.saveLocked()
}
func (m *autoTaskManager) toggle(id string) (AliasTask, error) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return t, fmt.Errorf("任务不存在")
	}
	t.Enabled = !t.Enabled
	if !t.Enabled {
		// 暂停:清空下次执行时刻,避免界面残留暂停前的旧时刻。
		t.NextRun = ""
	}
	m.tasks[id] = t
	if t.Enabled {
		m.logLocked(id, "info", "任务已启用")
	} else {
		m.logLocked(id, "info", "任务已暂停")
	}
	e := m.saveLocked()
	m.mu.Unlock()
	if t.Enabled && t.CreatedCount < t.MaxTotal {
		m.ensureRunning(id)
	} else {
		m.stop(id)
	}
	return t, e
}
func (m *autoTaskManager) stop(id string) {
	m.mu.Lock()
	ch, ok := m.stops[id]
	done := m.done[id]
	if ok {
		delete(m.stops, id)
	}
	m.mu.Unlock()
	if ok {
		close(ch)
		<-done
	}
}
func (m *autoTaskManager) close() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.stops))
	for id := range m.stops {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.stop(id)
	}
}
func (m *autoTaskManager) ensureRunning(id string) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal || m.stops[id] != nil {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	m.stops[id] = stop
	m.done[id] = done
	interval := time.Duration(t.IntervalMinutes) * time.Minute
	// 从「启用时刻 + 间隔」推算下次执行,而不是沿用暂停前的绝对时刻。
	t.NextRun = time.Now().Add(interval).Format(time.RFC3339)
	m.tasks[id] = t
	_ = m.saveLocked()
	m.mu.Unlock()
	go func() {
		defer close(done)
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				m.runOnce(id)
			case <-stop:
				m.mu.Lock()
				delete(m.stops, id)
				m.mu.Unlock()
				return
			}
		}
	}()
}
func (m *autoTaskManager) runOnce(id string) AliasTask {
	m.mu.Lock()
	if m.creating[id] {
		t := m.tasks[id]
		m.mu.Unlock()
		return t
	}
	t, ok := m.tasks[id]
	if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal {
		m.mu.Unlock()
		return t
	}
	m.creating[id] = true
	remaining := t.MaxTotal - t.CreatedCount
	count := t.BatchCount
	if count > remaining {
		count = remaining
	}
	m.mu.Unlock()

	success := 0
	lastErr := ""
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		label := fmt.Sprintf("%s%03d", t.LabelPrefix, t.NextNumber+i)
		_, err := m.backend.CreateAlias(t.AccountID, label)
		m.mu.Lock()
		current := m.tasks[id]
		if err != nil {
			lastErr = err.Error()
			m.logLocked(id, "error", "%s 创建失败：%v", label, err)
		} else {
			success++
			current.CreatedCount++
			current.NextNumber++
			m.tasks[id] = current
			m.logLocked(id, "info", "%s 创建成功（进度 %d/%d）", label, current.CreatedCount, current.MaxTotal)
		}
		_ = m.saveLocked()
		m.mu.Unlock()
		if err != nil {
			break
		}
	}

	m.mu.Lock()
	t = m.tasks[id]
	t.LastRun = time.Now().Format(time.RFC3339)
	t.LastSuccess = success
	t.LastError = lastErr
	if lastErr != "" {
		for _, acc := range m.backend.ListAccounts() {
			if acc.ID == t.AccountID && acc.Status != "active" {
				t.Enabled = false
				m.logLocked(id, "error", "账号状态异常（%s），任务已暂停", acc.Status)
			}
		}
	}
	// 仅当任务仍启用且未达上限时,按「本轮结束 + 间隔」推算下次执行;否则清空。
	if t.Enabled && t.CreatedCount < t.MaxTotal {
		t.NextRun = time.Now().Add(time.Duration(t.IntervalMinutes) * time.Minute).Format(time.RFC3339)
	} else {
		t.NextRun = ""
	}
	m.tasks[id] = t
	delete(m.creating, id)
	_ = m.saveLocked()
	m.mu.Unlock()
	if t.CreatedCount >= t.MaxTotal || !t.Enabled {
		m.requestStop(id)
	}
	return t
}

func (m *autoTaskManager) requestStop(id string) {
	m.mu.Lock()
	ch, ok := m.stops[id]
	if ok {
		delete(m.stops, id)
	}
	m.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (m *autoTaskManager) start() {
	m.mu.Lock()
	ids := make([]string, 0)
	for id, t := range m.tasks {
		if t.Enabled && t.CreatedCount < t.MaxTotal {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.ensureRunning(id)
	}
}
