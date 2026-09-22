package server

import (
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// 创建周期下限、上限：防止短周期集中请求触发上游风控。
	minTaskIntervalMinutes = 20
	maxTaskIntervalMinutes = 24 * 60
	// 单个账号每天最多创建的别名数（人工 + 所有任务合计的硬上限）。
	maxTaskDailyLimit = 50
	maxTaskTotal      = 999
	// 无论人工或自动路径，同一账号两次创建尝试至少间隔 20 分钟。
	minCreationCooldown = 20 * time.Minute

	// 任务类型：
	//   auto      自主任务——设定每天创建数量，由系统自动把创建时刻分摊到全天；
	//   scheduled 定时任务——每隔固定分钟创建固定数量，直到达到目标总数。
	taskModeAuto      = "auto"
	taskModeScheduled = "scheduled"

	// scheduled 任务单个周期允许创建的数量上限（防止一次性爆发）。
	maxBatchPerInterval = 20

	// 自主任务的拟人活动窗口：仅在 [autoActiveStartHour, autoActiveEndHour) 内创建，
	// 避免凌晨等间隔创建暴露机器特征。窗口过窄容纳不下当日数量时会自动前移起点。
	autoActiveStartHour = 8
	autoActiveEndHour   = 24
	// autoJitterSigma 为创建间隔的乘性扰动幅度：实际间隔 = 基准 × (1 ± sigma)。
	// 取三角分布，使多数间隔落在基准附近、偶尔明显偏长或偏短，更接近真人节奏。
	autoJitterSigma = 0.4
)

// allowedDailyCounts 是自主任务可选的每日创建数量。
var allowedDailyCounts = map[int]bool{5: true, 10: true, 15: true, 20: true, 25: true, 30: true, 35: true, 40: true, 45: true, 50: true}

var (
	errAliasTaskValidation  = errors.New("alias task validation error")
	errAliasTaskNotFound    = errors.New("alias task not found")
	errAliasTaskPersistence = errors.New("alias task persistence error")
	errCreationCooldown     = errors.New("creation cooldown")
	errCreationDailyLimit   = errors.New("creation daily limit")
)

func aliasTaskValidationError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errAliasTaskValidation, fmt.Sprintf(format, args...))
}

func aliasTaskPersistenceError(err error) error {
	return fmt.Errorf("%w: %v", errAliasTaskPersistence, err)
}

type AliasTask struct {
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
	AccountID string `json:"account_id"`
	// Mode 为任务类型：auto(自主) 或 scheduled(定时)。
	Mode string `json:"mode"`
	// IntervalMinutes 仅 scheduled 任务使用：每隔多少分钟创建一批。
	IntervalMinutes int `json:"interval_minutes"`
	// BatchCount 仅 scheduled 任务使用：每个周期创建的数量。
	BatchCount int `json:"batch_count"`
	// DailyLimit 表示每个自然日最多创建数量。
	// auto 任务：即为用户设定的每天创建数量；scheduled 任务：作为每日安全上限。
	DailyLimit int `json:"daily_limit"`
	// LabelMode 为标签生成方式：library(名称库自动) / sequential(顺序) / hash(哈希)。
	LabelMode string `json:"label_mode"`
	// LabelPrefix 为手动标签前缀（sequential/hash 模式使用）。
	LabelPrefix string `json:"label_prefix,omitempty"`
	// HashLength 为哈希后缀长度（hash 模式使用）。
	HashLength int `json:"hash_length,omitempty"`
	// LabelSeed 为 library 模式的名称库抽取种子。每个任务独立随机生成，使不同
	// 任务的抽取序列彼此错开，降低跨任务撞名概率；为 0 时按任务 ID 派生（兼容旧任务）。
	LabelSeed    uint64 `json:"label_seed,omitempty"`
	MaxTotal     int    `json:"max_total"`
	CreatedCount int    `json:"created_count"`
	NextNumber   int    `json:"next_number"`
	DailyCount   int    `json:"daily_count"`
	DailyDate    string `json:"daily_date,omitempty"`
	LastRun      string `json:"last_run,omitempty"`
	LastSuccess  int    `json:"last_success"`
	LastError    string `json:"last_error,omitempty"`
	NextRun      string `json:"next_run,omitempty"`
}

type aliasTaskInput struct {
	Enabled         bool   `json:"enabled"`
	AccountID       string `json:"account_id"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"interval_minutes"`
	BatchCount      int    `json:"batch_count"`
	TargetCount     int    `json:"target_count"`
	DailyLimit      int    `json:"daily_limit"`
	LabelMode       string `json:"label_mode"`
	LabelPrefix     string `json:"label_prefix"`
	HashLength      int    `json:"hash_length"`
}
type aliasTaskFile struct {
	Tasks          []AliasTask                     `json:"tasks"`
	ManualDaily    map[string]dailyCreationCounter `json:"manual_daily,omitempty"`
	CreationGuards map[string]creationGuard        `json:"creation_guards,omitempty"`
}

type dailyCreationCounter struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// creationGuard 记录账号最近一次创建尝试。即使上游失败也保留记录，避免失败
// 后立刻重试造成更高风险。
type creationGuard struct {
	LastAttempt string `json:"last_attempt"`
}

type AliasTaskLog struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type autoTaskManager struct {
	mu             sync.Mutex
	tasks          map[string]AliasTask
	file           string
	backend        Backend
	stops          map[string]chan struct{}
	done           map[string]chan struct{}
	creating       map[string]bool
	manualDaily    map[string]dailyCreationCounter
	creationGuards map[string]creationGuard
	logs           []AliasTaskLog
	logFile        string
	batchDelay     time.Duration
	writeState     func(string, any) error
	writeLogs      func(string, any) error
	// now 与 jitter 可注入，便于测试确定化。jitter 返回以 1.0 为中心的乘性因子。
	now    func() time.Time
	jitter func() float64
}

func normalizeAliasTask(in aliasTaskInput) (AliasTask, error) {
	if strings.TrimSpace(in.AccountID) == "" {
		return AliasTask{}, aliasTaskValidationError("account_id 必填")
	}
	if in.TargetCount < 1 || in.TargetCount > maxTaskTotal {
		return AliasTask{}, aliasTaskValidationError("target_count 必须为 1-%d", maxTaskTotal)
	}

	task := AliasTask{
		ID:         "task_" + uuid.New().String()[:8],
		Enabled:    in.Enabled,
		AccountID:  strings.TrimSpace(in.AccountID),
		MaxTotal:   in.TargetCount,
		NextNumber: 1,
		// 为名称库抽取分配独立随机种子，使不同任务的序列彼此错开。
		LabelSeed: newLabelSeed(),
	}

	mode := in.Mode
	if mode == "" {
		mode = taskModeAuto
	}
	switch mode {
	case taskModeAuto:
		// 自主任务：用户设定每天创建数量（5-50，步进 5），系统自动安排时间。
		if !allowedDailyCounts[in.DailyLimit] {
			return AliasTask{}, aliasTaskValidationError("daily_limit 必须为 5-%d 之间且为 5 的倍数", maxTaskDailyLimit)
		}
		task.Mode = taskModeAuto
		task.DailyLimit = in.DailyLimit
		// 自主任务的实际执行间隔由 nextAutoDelay 在每次调度时按活动窗口+剩余预算
		// +随机扰动动态计算；此处仅存一个按每日数量分摊的基准值作为 NextRun 初值估算。
		task.IntervalMinutes = autoIntervalMinutes(in.DailyLimit)
		task.BatchCount = 1
	case taskModeScheduled:
		// 定时任务：每隔 interval 分钟创建 batch 个，直到达到目标总数。
		if in.IntervalMinutes < minTaskIntervalMinutes || in.IntervalMinutes > maxTaskIntervalMinutes {
			return AliasTask{}, aliasTaskValidationError("interval_minutes 必须为 %d-%d", minTaskIntervalMinutes, maxTaskIntervalMinutes)
		}
		if in.BatchCount < 1 || in.BatchCount > maxBatchPerInterval {
			return AliasTask{}, aliasTaskValidationError("batch_count 必须为 1-%d", maxBatchPerInterval)
		}
		task.Mode = taskModeScheduled
		task.IntervalMinutes = in.IntervalMinutes
		task.BatchCount = in.BatchCount
		task.DailyLimit = maxTaskDailyLimit
	default:
		return AliasTask{}, aliasTaskValidationError("mode 必须为 %s 或 %s", taskModeAuto, taskModeScheduled)
	}

	labelMode := in.LabelMode
	if labelMode == "" {
		labelMode = labelModeLibrary
	}
	switch labelMode {
	case labelModeLibrary:
		task.LabelMode = labelModeLibrary
	case labelModeSequential, labelModeHash:
		prefix := strings.TrimSpace(in.LabelPrefix)
		if prefix == "" {
			return AliasTask{}, aliasTaskValidationError("label_prefix 必填")
		}
		if len([]rune(prefix)) > maxLabelPrefixLen {
			return AliasTask{}, aliasTaskValidationError("label_prefix 最长 %d 字符", maxLabelPrefixLen)
		}
		task.LabelMode = labelMode
		task.LabelPrefix = prefix
		if labelMode == labelModeHash {
			hashLen := in.HashLength
			if hashLen == 0 {
				hashLen = minHashLength
			}
			if hashLen < minHashLength || hashLen > maxHashLength {
				return AliasTask{}, aliasTaskValidationError("hash_length 必须为 %d-%d", minHashLength, maxHashLength)
			}
			task.HashLength = hashLen
		}
	default:
		return AliasTask{}, aliasTaskValidationError("label_mode 必须为 %s、%s 或 %s", labelModeLibrary, labelModeSequential, labelModeHash)
	}

	return task, nil
}

// autoIntervalMinutes 为自主任务按每天创建数量把 24 小时均匀分摊出的执行间隔。
// 例如每天 10 个 → 每 144 分钟一次；同时受最小冷却窗口约束不会低于下限。
func autoIntervalMinutes(dailyCount int) int {
	if dailyCount < 1 {
		dailyCount = 1
	}
	interval := (24 * 60) / dailyCount
	if interval < minTaskIntervalMinutes {
		interval = minTaskIntervalMinutes
	}
	if interval > maxTaskIntervalMinutes {
		interval = maxTaskIntervalMinutes
	}
	return interval
}

// autoActiveWindow 返回 now 所在自然日的拟人活动窗口 [start, end)。
// 当窗口时长不足以在最小冷却间隔内容纳 remaining 个创建时，自动向前扩展起点，
// 保证当日目标仍可达成（宁可放宽作息也不违反 20 分钟冷却红线）。
func autoActiveWindow(now time.Time, remaining int) (time.Time, time.Time) {
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(autoActiveEndHour * time.Hour)
	start := time.Date(now.Year(), now.Month(), now.Day(), autoActiveStartHour, 0, 0, 0, now.Location())
	if remaining < 1 {
		return start, end
	}
	// 至少要留出 remaining 段最小冷却；不够则把起点前移（不早于 0 点）。
	needed := time.Duration(remaining) * minCreationCooldown
	if earliest := end.Add(-needed); earliest.Before(start) {
		start = earliest
		if dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()); start.Before(dayStart) {
			start = dayStart
		}
	}
	return start, end
}

// nextAutoDelay 计算自主任务下一次创建前应等待的时长。三层叠加：
//  1. 剩余预算配速：base = 窗口剩余时长 / 当日剩余数量，天然自校正，使当日总数收敛到目标；
//  2. 乘性随机扰动：base × jitter()（以 1.0 为中心的三角分布），制造不规则的真人节奏；
//  3. 昼夜窗口 + 冷却地板：窗口外顺延到窗口起点或次日；结果不低于 minCreationCooldown。
func (m *autoTaskManager) nextAutoDelay(t AliasTask, now time.Time) time.Duration {
	remaining := t.DailyLimit - t.DailyCount
	if remaining <= 0 {
		// 当日额度用尽，睡到次日窗口起点附近。
		return nextDailyRun(now).Sub(now)
	}
	start, end := autoActiveWindow(now, remaining)
	if now.Before(start) {
		return start.Sub(now)
	}
	if !now.Before(end) {
		return nextDailyRun(now).Sub(now)
	}
	base := end.Sub(now) / time.Duration(remaining)
	delay := time.Duration(float64(base) * m.jitterFactor())
	if delay < minCreationCooldown {
		delay = minCreationCooldown
	}
	return delay
}

// jitterFactor 返回本次调度的乘性扰动因子，注入缺省时退化为无扰动的 1.0。
func (m *autoTaskManager) jitterFactor() float64 {
	if m.jitter == nil {
		return 1.0
	}
	return m.jitter()
}

// nowFunc 返回可注入的时钟，缺省为 time.Now。
func (m *autoTaskManager) nowFunc() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// triangularJitter 返回区间 [1-sigma, 1+sigma]、众数为 1.0 的三角分布采样，
// 只依赖 math/rand/v2 的全局源（无需额外种子管理）。
func triangularJitter() float64 {
	// 两个均匀随机数之和的一半服从三角分布，均值 0.5、范围 [0,1]。
	u := (mrand.Float64() + mrand.Float64()) / 2
	return 1 + autoJitterSigma*(2*u-1)
}
func newAutoTaskManager(file string, backend Backend) *autoTaskManager {
	m := &autoTaskManager{file: file, logFile: filepath.Join(filepath.Dir(file), "alias_task_logs.json"), backend: backend, tasks: map[string]AliasTask{}, stops: map[string]chan struct{}{}, done: map[string]chan struct{}{}, creating: map[string]bool{}, manualDaily: map[string]dailyCreationCounter{}, creationGuards: map[string]creationGuard{}, batchDelay: 3 * time.Second, writeState: writeJSONAtomic, writeLogs: writeJSONAtomic, now: time.Now, jitter: triangularJitter}
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
		if f.ManualDaily != nil {
			m.manualDaily = f.ManualDaily
		}
		if f.CreationGuards != nil {
			m.creationGuards = f.CreationGuards
		}
		for _, t := range f.Tasks {
			if t.ID == "" {
				t.ID = "task_" + uuid.New().String()[:8]
			}
			if t.NextNumber < 1 {
				t.NextNumber = 1
			}
			// 兼容旧任务：未写入抽取种子时按 ID 派生一个稳定非零种子，
			// 使其从顺序抽取切换到与其它任务错开的随机抽取。
			if t.LabelSeed == 0 {
				t.LabelSeed = fnv64(t.ID)
			}
			// 兼容旧任务：旧记录没有 mode 字段，按定时任务解释。
			if t.Mode == "" {
				t.Mode = taskModeScheduled
			}
			if t.LabelMode == "" {
				t.LabelMode = labelModeLibrary
			}
			if t.BatchCount < 1 {
				t.BatchCount = 1
			}
			// 兼容旧任务：旧字段没有 daily_limit 时采用新的安全默认值。
			if t.DailyLimit < 1 || t.DailyLimit > maxTaskDailyLimit {
				t.DailyLimit = maxTaskDailyLimit
			}
			if t.IntervalMinutes < minTaskIntervalMinutes || t.IntervalMinutes > maxTaskIntervalMinutes {
				t.IntervalMinutes = maxTaskIntervalMinutes
			}
			m.tasks[t.ID] = t
		}
	}
}
func (m *autoTaskManager) saveLocked() error {
	return m.writeState(m.file, aliasTaskFile{Tasks: m.taskListLocked(), ManualDaily: m.manualDaily, CreationGuards: m.creationGuards})
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
	committed := e == nil
	if !committed {
		delete(m.tasks, t.ID)
		e = aliasTaskPersistenceError(e)
	} else if logErr := m.logLocked(t.ID, "info", "创建任务，等待 10 秒后开始"); logErr != nil {
		e = aliasTaskPersistenceError(logErr)
	}
	m.mu.Unlock()
	if committed {
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
func (m *autoTaskManager) saveAddLocked(t AliasTask) error {
	old, existed := m.tasks[t.ID]
	m.tasks[t.ID] = t
	if err := m.saveLocked(); err != nil {
		if existed {
			m.tasks[t.ID] = old
		} else {
			delete(m.tasks, t.ID)
		}
		return err
	}
	return nil
}
func (m *autoTaskManager) update(id string, in aliasTaskInput) (AliasTask, error) {
	n, e := normalizeAliasTask(in)
	if e != nil {
		return n, e
	}
	m.mu.Lock()
	old, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return AliasTask{}, fmt.Errorf("%w: 任务不存在", errAliasTaskNotFound)
	}
	m.mu.Unlock()
	m.stop(id)
	m.mu.Lock()
	old, ok = m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return AliasTask{}, fmt.Errorf("%w: 任务不存在", errAliasTaskNotFound)
	}
	n.ID = id
	n.Enabled = true
	n.NextNumber = old.NextNumber
	// 保留原有抽取种子，使更新任务不改变名称库抽取序列（避免与已创建标签重叠）。
	n.LabelSeed = old.LabelSeed
	n.CreatedCount = old.CreatedCount
	n.DailyCount = old.DailyCount
	n.DailyDate = old.DailyDate
	n.LastRun = old.LastRun
	n.LastSuccess = old.LastSuccess
	n.LastError = old.LastError
	n.NextRun = old.NextRun
	e = m.saveAddLocked(n)
	if e != nil {
		m.tasks[id] = old
		e = aliasTaskPersistenceError(e)
	}
	m.mu.Unlock()
	if e != nil {
		if old.Enabled && old.CreatedCount < old.MaxTotal {
			_ = m.ensureRunning(id)
		}
		return old, e
	}
	if n.Enabled && n.CreatedCount < n.MaxTotal {
		if runErr := m.ensureRunning(id); runErr != nil {
			e = aliasTaskPersistenceError(runErr)
		}
	}
	return n, e
}
func (m *autoTaskManager) remove(id string) error {
	m.mu.Lock()
	_, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: 任务不存在", errAliasTaskNotFound)
	}
	m.stop(id)
	m.mu.Lock()
	old, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: 任务不存在", errAliasTaskNotFound)
	}
	delete(m.tasks, id)
	err := m.saveLocked()
	if err != nil {
		m.tasks[id] = old
		err = aliasTaskPersistenceError(err)
	}
	m.mu.Unlock()
	if err != nil && old.Enabled && old.CreatedCount < old.MaxTotal {
		_ = m.ensureRunning(id)
	}
	return err
}
func (m *autoTaskManager) toggle(id string) (AliasTask, error) {
	m.mu.Lock()
	old, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return old, fmt.Errorf("%w: 任务不存在", errAliasTaskNotFound)
	}
	t := old
	t.Enabled = !t.Enabled
	if !t.Enabled {
		// 暂停:清空下次执行时刻,避免界面残留暂停前的旧时刻。
		t.NextRun = ""
	}
	m.tasks[id] = t
	if e := m.saveLocked(); e != nil {
		m.tasks[id] = old
		m.mu.Unlock()
		return old, aliasTaskPersistenceError(e)
	}
	message := "任务已暂停"
	if t.Enabled {
		message = "任务已启用"
	}
	logErr := m.logLocked(id, "info", "%s", message)
	m.mu.Unlock()
	var e error
	if logErr != nil {
		e = aliasTaskPersistenceError(logErr)
	}
	if t.Enabled && t.CreatedCount < t.MaxTotal {
		if runErr := m.ensureRunning(id); runErr != nil {
			e = errors.Join(e, aliasTaskPersistenceError(runErr))
		}
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
func (m *autoTaskManager) ensureRunning(id string) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal || m.stops[id] != nil {
		m.mu.Unlock()
		return nil
	}
	old := t
	stop := make(chan struct{})
	done := make(chan struct{})
	m.stops[id] = stop
	m.done[id] = done
	// 自主任务用带随机扰动的动态间隔（更接近真人节奏）；定时任务保持精确固定间隔。
	auto := t.Mode == taskModeAuto
	var firstDelay time.Duration
	if auto {
		firstDelay = m.nextAutoDelay(t, m.nowFunc())
	} else {
		firstDelay = time.Duration(t.IntervalMinutes) * time.Minute
	}
	// 从「启用时刻 + 间隔」推算下次执行,而不是沿用暂停前的绝对时刻。
	t.NextRun = m.nowFunc().Add(firstDelay).Format(time.RFC3339)
	m.tasks[id] = t
	saveErr := m.saveLocked()
	if saveErr != nil {
		m.tasks[id] = old
	}
	m.mu.Unlock()
	if auto {
		go m.runAutoLoop(id, stop, done, firstDelay)
	} else {
		go m.runScheduledLoop(id, stop, done, firstDelay)
	}
	return saveErr
}

// runScheduledLoop 以固定间隔精确触发定时任务，直到收到停止信号。
func (m *autoTaskManager) runScheduledLoop(id string, stop, done chan struct{}, interval time.Duration) {
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
}

// runAutoLoop 用自重排定时器驱动自主任务：每次创建后按 nextAutoDelay 重新计算
// 下一次的扰动间隔，而非固定周期，从而呈现不规则的拟人创建节奏。
func (m *autoTaskManager) runAutoLoop(id string, stop, done chan struct{}, firstDelay time.Duration) {
	defer close(done)
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			m.runOnce(id)
			// 重排下一次：读取最新任务状态计算扰动间隔并回写 NextRun。
			m.mu.Lock()
			t, ok := m.tasks[id]
			if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal {
				m.mu.Unlock()
				// 任务已终止；runOnce 内已 requestStop，此处等待停止信号。
				continue
			}
			next := m.nextAutoDelay(t, m.nowFunc())
			t.NextRun = m.nowFunc().Add(next).Format(time.RFC3339)
			m.tasks[id] = t
			_ = m.saveLocked()
			m.mu.Unlock()
			timer.Reset(next)
		case <-stop:
			m.mu.Lock()
			delete(m.stops, id)
			m.mu.Unlock()
			return
		}
	}
}
func (m *autoTaskManager) runOnce(id string) AliasTask {
	m.mu.Lock()
	if m.creating[id] {
		t := m.tasks[id]
		m.mu.Unlock()
		return t
	}
	t, ok := m.tasks[id]
	if ok {
		t = resetDailyCount(t, time.Now())
		m.tasks[id] = t
	}
	if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal {
		m.mu.Unlock()
		return t
	}
	remaining := t.MaxTotal - t.CreatedCount
	now := time.Now()
	today := taskDate(now)
	if m.accountDailyCountLocked(t.AccountID, today) >= maxTaskDailyLimit || t.DailyCount >= t.DailyLimit {
		t.NextRun = nextDailyRun(now).Format(time.RFC3339)
		m.tasks[id] = t
		_ = m.saveLocked()
		m.mu.Unlock()
		return t
	}
	if cooldown := m.creationCooldownRemainingLocked(t.AccountID, now); cooldown > 0 {
		t.NextRun = now.Add(cooldown).Format(time.RFC3339)
		m.tasks[id] = t
		_ = m.saveLocked()
		m.mu.Unlock()
		return t
	}
	m.creating[id] = true
	// 定时任务每个周期创建 BatchCount 个；自主任务每次 1 个。数量还需
	// 受剩余目标数与当日额度约束，逐个创建时再次校验。
	count := t.BatchCount
	if count < 1 {
		count = 1
	}
	if remaining < count {
		count = remaining
	}
	m.mu.Unlock()

	success := 0
	lastErr := ""
	for i := 0; i < count; i++ {
		// 同一周期内连续创建时留出间隔，避免瞬时高频触发上游风控。
		if i > 0 {
			time.Sleep(m.batchDelay)
		}
		m.mu.Lock()
		current, exists := m.tasks[id]
		if exists {
			current = resetDailyCount(current, time.Now())
			m.tasks[id] = current
		}
		if !exists || !current.Enabled || current.CreatedCount >= current.MaxTotal || current.DailyCount >= current.DailyLimit || m.accountDailyCountLocked(current.AccountID, taskDate(time.Now())) >= maxTaskDailyLimit {
			m.mu.Unlock()
			break
		}
		if err := m.reserveAutoCreationAttemptLocked(current.AccountID, time.Now()); err != nil {
			lastErr = "创建冷却状态保存失败：" + err.Error()
			current.Enabled = false
			current.NextRun = ""
			current.LastRun = time.Now().Format(time.RFC3339)
			current.LastSuccess = success
			current.LastError = lastErr
			m.tasks[id] = current
			_ = m.saveLocked()
			m.mu.Unlock()
			break
		}
		label := current.labelFor(current.NextNumber)
		reserved := current
		reserved.NextNumber++
		reserved.DailyCount++
		m.tasks[id] = reserved
		if reserveErr := m.saveLocked(); reserveErr != nil {
			m.tasks[id] = current
			lastErr = "任务序号预留失败：" + reserveErr.Error()
			current.Enabled = false
			current.NextRun = ""
			current.LastRun = time.Now().Format(time.RFC3339)
			current.LastSuccess = success
			current.LastError = lastErr
			m.tasks[id] = current
			if recoveryErr := m.saveLocked(); recoveryErr != nil {
				lastErr += "；暂停状态保存失败：" + recoveryErr.Error()
				current.LastError = lastErr
				m.tasks[id] = current
			}
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()

		_, createErr := m.backend.CreateAlias(current.AccountID, label)
		m.mu.Lock()
		current = m.tasks[id]
		if createErr != nil {
			lastErr = createErr.Error()
			current.Enabled = false
			current.NextRun = ""
			current.LastRun = time.Now().Format(time.RFC3339)
			current.LastSuccess = success
			current.LastError = lastErr
			m.tasks[id] = current
			_ = m.saveLocked()
			if logErr := m.logLocked(id, "error", "%s 创建失败：%v", label, createErr); logErr != nil {
				lastErr += "；日志保存失败：" + logErr.Error()
			}
			m.mu.Unlock()
			break
		}

		success++
		current.CreatedCount++
		m.tasks[id] = current
		if saveErr := m.saveLocked(); saveErr != nil {
			lastErr = "任务状态保存失败：" + saveErr.Error()
			current.Enabled = false
			current.NextRun = ""
			current.LastRun = time.Now().Format(time.RFC3339)
			current.LastSuccess = success
			current.LastError = lastErr
			m.tasks[id] = current
			if recoveryErr := m.saveLocked(); recoveryErr != nil {
				lastErr += "；暂停状态保存失败：" + recoveryErr.Error()
				current.LastError = lastErr
				m.tasks[id] = current
			}
			m.mu.Unlock()
			break
		}
		if logErr := m.logLocked(id, "info", "%s 创建成功（进度 %d/%d）", label, current.CreatedCount, current.MaxTotal); logErr != nil {
			lastErr = "日志保存失败：" + logErr.Error()
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	t = m.tasks[id]
	t.LastRun = time.Now().Format(time.RFC3339)
	t.LastSuccess = success
	t.LastError = lastErr
	pauseStatus := ""
	if lastErr != "" {
		for _, acc := range m.backend.ListAccounts() {
			if acc.ID == t.AccountID && acc.Status != "active" {
				t.Enabled = false
				pauseStatus = acc.Status
			}
		}
	}
	if lastErr != "" {
		t.Enabled = false
	}
	// 仅当任务仍启用且未达上限时,按「本轮结束 + 间隔」推算下次执行;否则清空。
	// 自主任务使用带扰动的动态间隔,定时任务使用固定间隔。
	if t.Enabled && t.CreatedCount < t.MaxTotal && t.DailyCount >= t.DailyLimit {
		t.NextRun = nextDailyRun(time.Now()).Format(time.RFC3339)
	} else if t.Enabled && t.CreatedCount < t.MaxTotal {
		if t.Mode == taskModeAuto {
			t.NextRun = m.nowFunc().Add(m.nextAutoDelay(t, m.nowFunc())).Format(time.RFC3339)
		} else {
			t.NextRun = time.Now().Add(time.Duration(t.IntervalMinutes) * time.Minute).Format(time.RFC3339)
		}
	} else {
		t.NextRun = ""
	}
	m.tasks[id] = t
	delete(m.creating, id)
	if saveErr := m.saveLocked(); saveErr != nil {
		if t.LastError != "" {
			t.LastError += "；"
		}
		t.LastError += "任务状态保存失败：" + saveErr.Error()
		t.Enabled = false
		t.NextRun = ""
		m.tasks[id] = t
		if recoveryErr := m.saveLocked(); recoveryErr != nil {
			t.LastError += "；暂停状态保存失败：" + recoveryErr.Error()
			m.tasks[id] = t
		}
	} else if pauseStatus != "" {
		if logErr := m.logLocked(id, "error", "账号状态异常（%s），任务已暂停", pauseStatus); logErr != nil {
			// The pause is already durable; do not overwrite it merely to record
			// that its explanatory log failed.
			t.LastError = "日志保存失败：" + logErr.Error()
			m.tasks[id] = t
		}
	}
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

func taskDate(now time.Time) string { return now.Format("2006-01-02") }

func resetDailyCount(task AliasTask, now time.Time) AliasTask {
	today := taskDate(now)
	if task.DailyDate != today {
		task.DailyDate = today
		task.DailyCount = 0
	}
	return task
}

// accountDailyCountLocked 返回一个账号在当前自然日内由所有任务创建的总数。
// 调用方必须已持有 m.mu，确保并发任务无法同时越过上限。
func (m *autoTaskManager) accountDailyCountLocked(accountID, day string) int {
	total := 0
	for _, task := range m.tasks {
		if task.AccountID == accountID && task.DailyDate == day {
			total += task.DailyCount
		}
	}
	if counter := m.manualDaily[accountID]; counter.Date == day {
		total += counter.Count
	}
	return total
}

func (m *autoTaskManager) resetManualDailyLocked(accountID string, now time.Time) {
	counter := m.manualDaily[accountID]
	if counter.Date != taskDate(now) {
		m.manualDaily[accountID] = dailyCreationCounter{Date: taskDate(now)}
	}
}

// creationCooldownRemainingLocked 返回同一账号再次创建前需要等待的时间。
// 调用方必须持有 m.mu。
func (m *autoTaskManager) creationCooldownRemainingLocked(accountID string, now time.Time) time.Duration {
	guard := m.creationGuards[accountID]
	last, err := time.Parse(time.RFC3339Nano, guard.LastAttempt)
	if err != nil {
		return 0
	}
	if elapsed := now.Sub(last); elapsed < minCreationCooldown {
		return minCreationCooldown - elapsed
	}
	return 0
}

// reserveAutoCreationAttemptLocked 记录自动任务的尝试时间。调用方必须持有
// m.mu；写入失败时不发起上游请求，避免重启后绕过冷却窗口。
func (m *autoTaskManager) reserveAutoCreationAttemptLocked(accountID string, now time.Time) error {
	oldGuard, hadGuard := m.creationGuards[accountID]
	m.creationGuards[accountID] = creationGuard{LastAttempt: now.Format(time.RFC3339Nano)}
	if err := m.saveLocked(); err != nil {
		if hadGuard {
			m.creationGuards[accountID] = oldGuard
		} else {
			delete(m.creationGuards, accountID)
		}
		return err
	}
	return nil
}

// reserveManualCreation 原子地预留一次人工创建额度。人工创建与自动任务
// 共享每天 20 个的硬上限，且同一账号任意两次创建尝试至少相隔 20 分钟。
func (m *autoTaskManager) reserveManualCreation(accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, task := range m.tasks {
		updated := resetDailyCount(task, now)
		if updated != task {
			m.tasks[id] = updated
		}
	}
	if m.creationCooldownRemainingLocked(accountID, now) > 0 {
		return errCreationCooldown
	}
	m.resetManualDailyLocked(accountID, now)
	if m.accountDailyCountLocked(accountID, taskDate(now)) >= maxTaskDailyLimit {
		return errCreationDailyLimit
	}
	oldGuard, hadGuard := m.creationGuards[accountID]
	m.creationGuards[accountID] = creationGuard{LastAttempt: now.Format(time.RFC3339Nano)}
	counter := m.manualDaily[accountID]
	counter.Count++
	m.manualDaily[accountID] = counter
	if err := m.saveLocked(); err != nil {
		counter.Count--
		m.manualDaily[accountID] = counter
		if hadGuard {
			m.creationGuards[accountID] = oldGuard
		} else {
			delete(m.creationGuards, accountID)
		}
		return err
	}
	return nil
}

// releaseManualCreation 在上游创建失败时归还已预留的额度。归还失败时保留
// 预留值，宁可降低当天可创建次数，也不在磁盘异常时放宽风控。
func (m *autoTaskManager) releaseManualCreation(accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counter := m.manualDaily[accountID]
	if counter.Date != taskDate(time.Now()) || counter.Count < 1 {
		return
	}
	counter.Count--
	m.manualDaily[accountID] = counter
	if err := m.saveLocked(); err != nil {
		counter.Count++
		m.manualDaily[accountID] = counter
	}
}

func nextDailyRun(now time.Time) time.Time {
	startOfTomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return startOfTomorrow.Add(5 * time.Minute)
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
