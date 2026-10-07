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
	// minCreationCooldown 是「人工」创建的最低间隔：两次手动创建尝试至少相隔
	// 20 分钟，避免误触/连点。自动任务的间隔由拟人模型决定（见 auto_task_persona.go），
	// 但两者共享账号级滚动小时上限（任意 60 分钟内 ≤5 次）。
	minCreationCooldown = 20 * time.Minute

	// 任务类型：
	//   auto      自主任务——设定每天创建数量，由系统按拟人模型分摊到全天；
	//   scheduled 定时任务——每隔固定分钟创建固定数量，直到达到目标总数。
	taskModeAuto      = "auto"
	taskModeScheduled = "scheduled"

	// scheduled 任务单个周期允许创建的数量上限（防止一次性爆发）。
	maxBatchPerInterval = 20

	// defaultAutoDailyLimit 是自主任务未显式指定每日数量时的默认值。
	defaultAutoDailyLimit = 20
)

var (
	errAliasTaskValidation  = errors.New("alias task validation error")
	errAliasTaskNotFound    = errors.New("alias task not found")
	errAliasTaskPersistence = errors.New("alias task persistence error")
	errCreationCooldown     = errors.New("creation cooldown")
	errCreationDailyLimit   = errors.New("creation daily limit")
	errCreationHourlyLimit  = errors.New("creation hourly limit")
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
	// TodayQuota 是「今日」实际可创建数量的上限。自主任务创建/跨天时按当天
	// 剩余时间折算(见 firstDayQuota): 中午启动只生成半天目标,避免首日过密。
	// 0 是合法值(深夜创建时折算不足 1 个 → 今日不排), 因此不能 omitempty;
	// 与「旧任务未设置」的区别由 load 按 JSON 键是否存在判定。
	TodayQuota int `json:"today_quota"`
	// LabelMode 为标签生成方式：library(名称库自动) / sequential(顺序) / hash(哈希)。
	LabelMode string `json:"label_mode"`
	// LabelPrefix 为手动标签前缀（sequential/hash 模式使用）。
	LabelPrefix string `json:"label_prefix,omitempty"`
	// HashLength 为哈希后缀长度（hash 模式使用）。
	HashLength int `json:"hash_length,omitempty"`
	// LabelSeed 为 library 模式的名称库抽取种子。每个任务独立随机生成，使不同
	// 任务的抽取序列彼此错开，降低跨任务撞名概率；为 0 时按任务 ID 派生（兼容旧任务）。
	LabelSeed uint64 `json:"label_seed,omitempty"`
	// Persona 为拟人画像名（standard/early_bird/night_owl/active），创建时由
	// LabelSeed 稳定派生；旧任务加载时回填。
	Persona string `json:"persona,omitempty"`
	// Timezone 为调度使用的 IANA 时区名（如 America/Denver），跟随账号代理出口
	// IP 的地理时区；空串表示使用服务器本地时区。作息窗口、日/周周期与每日配额
	// 边界都按该时区计算。
	Timezone     string `json:"timezone,omitempty"`
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
	AutoDaily      map[string]dailyCreationCounter `json:"auto_daily,omitempty"`
	CreationGuards map[string]creationGuard        `json:"creation_guards,omitempty"`
}

type dailyCreationCounter struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// creationGuard 记录账号最近的创建尝试（成功或失败、人工或自动均计入）。
// 即使上游失败也保留记录，避免失败后立刻重试造成更高风险。
type creationGuard struct {
	LastAttempt string `json:"last_attempt"`
	// RecentAttempts 保存最近约 2 小时内的尝试时刻（RFC3339Nano），
	// 用于执行「任意连续 60 分钟内创建尝试 ≤ maxCreationsPerHour」的硬约束。
	RecentAttempts []string `json:"recent_attempts,omitempty"`
}

type AliasTaskLog struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// now 与 rand 可注入，便于测试确定化。rand 返回 [0,1) 均匀随机数。
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
	// autoDaily 是账号级「自然日」自动创建计数(人工之外的部分)。
	// 任务的每日配额按作息周期重置(可能跨自然日), 因此账号级 50/天安全上限
	// 不能从任务字段求和, 必须单独按自然日计数。
	autoDaily map[string]dailyCreationCounter
	// closed 在 close() 时关闭, 让 scheduleStart 的延时协程及时退出,
	// 避免测试或停机后仍触发一轮 runOnce(审查探针: 新测试文件从未 join 该协程)。
	closed     chan struct{}
	closeOnce  sync.Once
	logs       []AliasTaskLog
	logFile    string
	batchDelay time.Duration
	writeState func(string, any) error
	writeLogs  func(string, any) error
	now        func() time.Time
	rand       func() float64
}

// normalizeAliasTask 以当前时间归一化任务输入(测试与旧调用方入口)。
func normalizeAliasTask(in aliasTaskInput) (AliasTask, error) {
	return normalizeAliasTaskAt(in, time.Now())
}

// normalizeAliasTaskAt 以指定时刻归一化任务输入。
// now 用于计算自主任务的首日配额(按当天剩余时间折算),可注入以便测试。
func normalizeAliasTaskAt(in aliasTaskInput, now time.Time) (AliasTask, error) {
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
	// 拟人画像由种子稳定派生：同一任务在任意时刻、任意重启后都保持同一作息。
	task.Persona = personaForSeed(task.LabelSeed).Name

	mode := in.Mode
	if mode == "" {
		mode = taskModeAuto
	}
	switch mode {
	case taskModeAuto:
		// 自主任务：用户设定每天创建数量(1-50 任意整数,缺省 20),系统自动安排时间。
		daily := in.DailyLimit
		if daily == 0 {
			daily = defaultAutoDailyLimit
		}
		if daily < 1 || daily > maxTaskDailyLimit {
			return AliasTask{}, aliasTaskValidationError("daily_limit 必须为 1-%d", maxTaskDailyLimit)
		}
		task.Mode = taskModeAuto
		task.DailyLimit = daily
		// 今日配额: 按创建时刻在当天剩余时间折算(中午启动只生成半天目标)。
		task.TodayQuota = firstDayQuota(daily, now)
		// 「日」边界用画像的作息周期(睡醒→睡醒), 与 resetDailyCount 口径一致:
		// 写日历日会让跨零点作息(如夜猫子)的首次 runOnce 被判为跨周期,
		// 把折算好的首日配额重置为满额。
		task.DailyDate = personaByName(task.Persona).cycleDateAt(now)
		// 自主任务的实际执行间隔由 nextAutoDelay 在每次调度时按活动窗口+剩余预算
		// +随机扰动动态计算；此处仅存一个按每日数量分摊的基准值作为 NextRun 初值估算。
		task.IntervalMinutes = autoIntervalMinutes(daily)
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

// firstDayQuota 按「今天剩余时间」折算自主任务今天应创建的数量。
//
// 以自然日 24 小时为基准: 中午 12 点创建任务 → 今天只排一半(如每日 20 个排 10 个),
// 避免首日把全天目标压进半天造成节奏过密。四舍五入到整数,
// 不足 1 个则今天不排(次日跨天时恢复满额)。
func firstDayQuota(dailyLimit int, now time.Time) int {
	if dailyLimit <= 0 {
		return 0
	}
	const daySeconds = 24 * 3600
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	remaining := dayStart.Add(24 * time.Hour).Sub(now).Seconds()
	if remaining <= 0 {
		return 0
	}
	if remaining > daySeconds {
		remaining = daySeconds
	}
	quota := int(float64(dailyLimit)*remaining/daySeconds + 0.5)
	if quota > dailyLimit {
		quota = dailyLimit
	}
	return quota
}

// taskTimezone 加载任务时区；空串或加载失败时回退到服务器本地时区。
func taskTimezone(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// nextAutoDelay 计算自主任务下一次创建前应等待的时长。
//
// 委托给拟人模型 sampleAutoDelay（分层 NHPP + 画像 + 会话自激，见
// auto_task_persona.go）：把 now 换算到任务时区（跟随账号代理出口 IP），
// 由画像决定作息窗口与节奏；账号级创建门控（滚动小时上限 + 最短间隔）
// 在采样中已被考虑。
func (m *autoTaskManager) nextAutoDelay(t AliasTask, now time.Time) time.Duration {
	loc := taskTimezone(t.Timezone)
	p := personaByName(t.Persona)
	local := now.In(loc)
	remaining := t.effectiveDailyQuota() - t.DailyCount
	if remaining <= 0 {
		// 当日额度用尽：睡到下一个睡醒（次日窗口起点），附加少量抖动。
		return p.nextWakeAt(local).Sub(local) + time.Duration(m.randFunc()()*float64(wakeJitterMax))
	}
	guard := m.creationGuards[t.AccountID]
	return sampleAutoDelay(p, local, t.DailyCount, t.effectiveDailyQuota(), guard, m.randFunc())
}

// deferAutoRun 返回自主任务下一次尝试的最早时刻：在 now+wait 基础上，
// 若落点处于画像睡眠窗口则顺延到睡醒（附加抖动），保证自动创建永不落在睡眠期。
func (m *autoTaskManager) deferAutoRun(t AliasTask, now time.Time, wait time.Duration) time.Time {
	cand := now.Add(wait)
	loc := taskTimezone(t.Timezone)
	p := personaByName(t.Persona)
	local := cand.In(loc)
	if p.sleepingAt(local) {
		cand = cand.Add(p.wakeAfter(local) + time.Duration(m.randFunc()()*float64(wakeJitterMax)))
	}
	return cand
}

// effectiveDailyQuota 返回任务「今日」实际可创建数量的上限。
// 自主任务用 TodayQuota(创建/跨天时按剩余时间折算), 0 表示今日不排
// (深夜创建等), 不得回退到 DailyLimit; 定时任务用 DailyLimit。
func (t AliasTask) effectiveDailyQuota() int {
	if t.Mode == taskModeAuto {
		return t.TodayQuota
	}
	return t.DailyLimit
}

// randFunc 返回可注入的随机源，缺省为 math/rand/v2 全局源。
func (m *autoTaskManager) randFunc() func() float64 {
	if m.rand != nil {
		return m.rand
	}
	return mrand.Float64
}

// nowFunc 返回可注入的时钟，缺省为 time.Now。
func (m *autoTaskManager) nowFunc() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

func newAutoTaskManager(file string, backend Backend) *autoTaskManager {
	m := &autoTaskManager{file: file, logFile: filepath.Join(filepath.Dir(file), "alias_task_logs.json"), backend: backend, tasks: map[string]AliasTask{}, stops: map[string]chan struct{}{}, done: map[string]chan struct{}{}, creating: map[string]bool{}, manualDaily: map[string]dailyCreationCounter{}, autoDaily: map[string]dailyCreationCounter{}, creationGuards: map[string]creationGuard{}, closed: make(chan struct{}), batchDelay: 3 * time.Second, writeState: writeJSONAtomic, writeLogs: writeJSONAtomic, now: time.Now}
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
		if f.AutoDaily != nil {
			m.autoDaily = f.AutoDaily
		}
		if f.CreationGuards != nil {
			m.creationGuards = f.CreationGuards
		}
		// 逐任务探测 today_quota 键是否存在: 显式 0(今日不排)必须保留,
		// 只有旧格式(该字段引入前保存)才回填满额。
		var probe struct {
			Tasks []map[string]json.RawMessage `json:"tasks"`
		}
		_ = json.Unmarshal(raw, &probe)
		for i, t := range f.Tasks {
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
			// 兼容旧任务：未写入画像时按种子回填，使旧任务也获得拟人作息。
			if t.Persona == "" {
				t.Persona = personaForSeed(t.LabelSeed).Name
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
			// 兼容旧任务：仅在旧格式(无 today_quota 键)时按满额补齐；
			// 显式写入的 0 是合法配额(今日不排), 必须保留。
			if t.Mode == taskModeAuto && !taskHasQuotaKey(probe.Tasks, i) {
				t.TodayQuota = t.DailyLimit
			}
			if t.IntervalMinutes < minTaskIntervalMinutes || t.IntervalMinutes > maxTaskIntervalMinutes {
				t.IntervalMinutes = maxTaskIntervalMinutes
			}
			m.tasks[t.ID] = t
		}
	}
}
func (m *autoTaskManager) saveLocked() error {
	return m.writeState(m.file, aliasTaskFile{Tasks: m.taskListLocked(), ManualDaily: m.manualDaily, AutoDaily: m.autoDaily, CreationGuards: m.creationGuards})
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
	t, e := normalizeAliasTaskAt(in, m.nowFunc())
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
		select {
		case <-time.After(10 * time.Second):
		case <-m.closed:
			return
		}
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
	n, e := normalizeAliasTaskAt(in, m.nowFunc())
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
	// 画像必须与种子保持一致(注释承诺「同一任务在任意时刻、任意重启后都保持
	// 同一作息」): normalizeAliasTaskAt 已按新种子派生了画像, 这里以保留的
	// 旧种子重新派生, 否则编辑会静默改变作息并借周期日变化重置每日配额。
	n.Persona = personaForSeed(old.LabelSeed).Name
	// 保留已解析的调度时区: n 由 normalize 新构造, 不带该字段; 丢弃会导致
	// 编辑后时区回退本地、下次 runOnce 重新解析(跨时区任务还会与周期日口径不一致)。
	n.Timezone = old.Timezone
	n.CreatedCount = old.CreatedCount
	n.DailyCount = old.DailyCount
	n.DailyDate = old.DailyDate
	if n.Mode == taskModeAuto {
		// 周期日必须按任务时区计算(与 resetDailyCount 的 now.In(loc) 口径一致):
		// 用服务器本地时区会在跨时区任务上误判跨周期, 编辑即重置当日计数。
		loc := taskTimezone(old.Timezone)
		cycle := personaByName(n.Persona).cycleDateAt(m.nowFunc().In(loc))
		if old.Mode == taskModeAuto && old.DailyDate == cycle {
			// 同一作息周期内编辑保留当日已定配额(避免深夜编辑把中午折算的配额
			// 重置为 0 或满额)。周期日与日历日对跨零点作息天然不同, 必须按
			// cycleDateAt 比较(审查探针实测日历日比较会让每次编辑都重算)。
			n.TodayQuota = old.TodayQuota
		} else {
			// 跨周期编辑(旧任务停在上一周期或模式切换): 按当前时刻重新折算,
			// 旧计数属于旧周期, 归零。
			n.DailyDate = cycle
			n.DailyCount = 0
		}
	}
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
	m.closeOnce.Do(func() {
		if m.closed != nil {
			close(m.closed)
		}
	})
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

// refreshTimezone 从后端刷新任务的调度时区(跟随账号代理出口 IP 的地理时区)。
//
// 后端(account.Manager.TimezoneFor)按账号缓存解析结果: 首次调用解析并缓存,
// 之后是纯内存读取; 代理变更时缓存被清除, 下次调用自动重新解析。
// 解析失败/无代理时保留原值——调度回退本地时区, 绝不因解析失败而停摆。
// 网络调用在锁外执行, 不阻塞其它任务。
func (m *autoTaskManager) refreshTimezone(id string) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok || !t.Enabled {
		return
	}
	tz, err := m.backend.TimezoneFor(t.AccountID)
	if err != nil || tz == "" || tz == t.Timezone {
		return
	}
	m.mu.Lock()
	if cur, ok := m.tasks[id]; ok {
		cur.Timezone = tz
		// 时区从空(本地)解析为具体时区后, DailyDate 的旧字符串按本地时区表达;
		// 若不重写, 下一次 resetDailyCount 按新时区算出不同周期日 → 误判跨周期,
		// 把首日折算的配额重置为满额、计数清零(审查探针实测)。这里只把周期日
		// 重写为新时区口径的当前值, 保留 DailyCount/TodayQuota 快照——
		// 宁可在真实跨周期时晚一拍重置, 也不误重置(账号级 50/天安全上限兜底)。
		if cur.Mode == taskModeAuto && cur.DailyDate != "" {
			cur.DailyDate = personaByName(cur.Persona).cycleDateAt(m.nowFunc().In(taskTimezone(tz)))
		}
		m.tasks[id] = cur
		_ = m.saveLocked()
	}
	m.mu.Unlock()
}

func (m *autoTaskManager) runOnce(id string) AliasTask {
	// 先刷新调度时区(代理出口 IP 变化后自动跟随), 再进入创建流程。
	m.refreshTimezone(id)
	m.mu.Lock()
	if m.creating[id] {
		t := m.tasks[id]
		m.mu.Unlock()
		return t
	}
	t, ok := m.tasks[id]
	if ok {
		t = resetDailyCount(t, m.nowFunc())
		m.tasks[id] = t
	}
	if !ok || !t.Enabled || t.CreatedCount >= t.MaxTotal {
		m.mu.Unlock()
		return t
	}
	remaining := t.MaxTotal - t.CreatedCount
	now := m.nowFunc()
	today := taskDate(now)
	if m.accountDailyCountLocked(t.AccountID, today) >= maxTaskDailyLimit || t.DailyCount >= t.effectiveDailyQuota() {
		if t.Mode == taskModeAuto {
			// 自主任务：睡到下一个睡醒（次日窗口起点），附加少量抖动。
			t.NextRun = now.Add(m.nextAutoDelay(t, now)).Format(time.RFC3339)
		} else {
			t.NextRun = nextDailyRun(now).Format(time.RFC3339)
		}
		m.tasks[id] = t
		_ = m.saveLocked()
		m.mu.Unlock()
		return t
	}
	// 自主任务睡眠感知：本地时间处于画像睡眠窗口时推迟到睡醒（附加抖动）。
	if t.Mode == taskModeAuto {
		loc := taskTimezone(t.Timezone)
		if p := personaByName(t.Persona); p.sleepingAt(now.In(loc)) {
			local := now.In(loc)
			wait := p.wakeAfter(local) + time.Duration(m.randFunc()()*float64(wakeJitterMax))
			t.NextRun = now.Add(wait).Format(time.RFC3339)
			m.tasks[id] = t
			_ = m.saveLocked()
			m.mu.Unlock()
			return t
		}
	}
	// 账号级创建门控（自动路径）：滚动小时上限（任意 60 分钟 ≤5 次）与
	// 自动最短间隔（会话内突发地板）。定时任务同样受滚动上限约束。
	if wait := m.creationGateWaitLocked(t.AccountID, now); wait > 0 {
		next := now.Add(wait)
		if t.Mode == taskModeAuto {
			// 自主任务顺延落点若处于睡眠窗口，继续推到睡醒。
			next = m.deferAutoRun(t, now, wait)
		}
		t.NextRun = next.Format(time.RFC3339)
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
			current = resetDailyCount(current, m.nowFunc())
			m.tasks[id] = current
		}
		if !exists || !current.Enabled || current.CreatedCount >= current.MaxTotal || current.DailyCount >= current.effectiveDailyQuota() || m.accountDailyCountLocked(current.AccountID, taskDate(m.nowFunc())) >= maxTaskDailyLimit {
			m.mu.Unlock()
			break
		}
		// 批内逐次校验滚动小时上限（任意 60 分钟 ≤5 次）：达到即停止本轮，
		// 剩余数量推迟到窗口空出后继续（批内间隔由 batchDelay 控制，
		// 不再叠加自动采样的最短间隔地板）。
		if hourlyWait(m.creationGuards[current.AccountID], m.nowFunc()) > 0 {
			m.mu.Unlock()
			break
		}
		if err := m.reserveAutoCreationAttemptLocked(current.AccountID, m.nowFunc()); err != nil {
			lastErr = "创建冷却状态保存失败：" + err.Error()
			current.Enabled = false
			current.NextRun = ""
			current.LastRun = m.nowFunc().Format(time.RFC3339)
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
			current.LastRun = m.nowFunc().Format(time.RFC3339)
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
			current.LastRun = m.nowFunc().Format(time.RFC3339)
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
			current.LastRun = m.nowFunc().Format(time.RFC3339)
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
	t.LastRun = m.nowFunc().Format(time.RFC3339)
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
	if t.Enabled && t.CreatedCount < t.MaxTotal && t.DailyCount >= t.effectiveDailyQuota() {
		if t.Mode == taskModeAuto {
			// 配额用尽: 睡到下一个睡醒(作息周期边界), 而不是日历零点——
			// 否则跨零点作息(如夜猫子)会被午夜截断, 整个白天静默。
			t.NextRun = m.nowFunc().Add(m.nextAutoDelay(t, m.nowFunc())).Format(time.RFC3339)
		} else {
			t.NextRun = nextDailyRun(m.nowFunc()).Format(time.RFC3339)
		}
	} else if t.Enabled && t.CreatedCount < t.MaxTotal {
		if t.Mode == taskModeAuto {
			t.NextRun = m.nowFunc().Add(m.nextAutoDelay(t, m.nowFunc())).Format(time.RFC3339)
		} else {
			t.NextRun = m.nowFunc().Add(time.Duration(t.IntervalMinutes) * time.Minute).Format(time.RFC3339)
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

// taskHasQuotaKey 判断持久化记录里是否显式写有 today_quota 键。
// 用于区分「显式 0(今日不排, 必须保留)」与「旧任务未设置(需回填)」。
func taskHasQuotaKey(probes []map[string]json.RawMessage, i int) bool {
	if i >= len(probes) || probes[i] == nil {
		return false
	}
	_, ok := probes[i]["today_quota"]
	return ok
}

// resetDailyCount 在跨「日」时清零当日计数,并重算自主任务的今日配额。
//
// 「日」的边界按任务模式区分:
//   - 自主任务: 按作息周期(见 persona.cycleDateAt)——从睡醒到下次睡醒,
//     使跨零点的作息(如夜猫子 09:00-03:00)不被日历零点截断;
//   - 定时任务: 按自然日(日历零点)。
func resetDailyCount(task AliasTask, now time.Time) AliasTask {
	if task.Mode == taskModeAuto {
		p := personaByName(task.Persona)
		loc := taskTimezone(task.Timezone)
		cycle := p.cycleDateAt(now.In(loc))
		if task.DailyDate != cycle {
			task.DailyDate = cycle
			task.DailyCount = 0
			task.TodayQuota = task.DailyLimit
		}
		return task
	}
	today := taskDate(now)
	if task.DailyDate != today {
		task.DailyDate = today
		task.DailyCount = 0
	}
	return task
}

// accountDailyCountLocked 返回一个账号在当前自然日内创建的总数(自动 + 人工)。
//
// 自动部分按独立的「自然日」计数器统计(autoDaily): 任务的每日配额按作息
// 周期重置(可能跨自然日), 不能直接对任务字段求和, 否则账号级 50/天 安全
// 上限在跨零点作息下会失真。调用方必须已持有 m.mu。
func (m *autoTaskManager) accountDailyCountLocked(accountID, day string) int {
	total := 0
	if counter := m.autoDaily[accountID]; counter.Date == day {
		total += counter.Count
	}
	if counter := m.manualDaily[accountID]; counter.Date == day {
		total += counter.Count
	}
	return total
}

// recordAutoCreationLocked 记录一次自动创建到账号级自然日计数器。
// 调用方必须已持有 m.mu。
func (m *autoTaskManager) recordAutoCreationLocked(accountID string, now time.Time) {
	day := taskDate(now)
	counter := m.autoDaily[accountID]
	if counter.Date != day {
		counter = dailyCreationCounter{Date: day}
	}
	counter.Count++
	m.autoDaily[accountID] = counter
}

func (m *autoTaskManager) resetManualDailyLocked(accountID string, now time.Time) {
	counter := m.manualDaily[accountID]
	if counter.Date != taskDate(now) {
		m.manualDaily[accountID] = dailyCreationCounter{Date: taskDate(now)}
	}
}

// creationCooldownRemainingLocked 返回同一账号再次「人工」创建前需要等待的时间。
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

// reserveAutoCreationAttemptLocked 记录自动任务的尝试时间（含滚动窗口历史），
// 并同步递增账号级「自然日」创建计数。
//
// 计数与门控在同一笔写入中落盘、且发生在**上游调用之前**：若计数推迟到
// CreateAlias 返回后才写, 同一账号的另一个任务可在慢调用期间通过同一配额
// 检查, 两个任务各记一次 → 突破 50/天上限(审查探针实测 49+2=51)。
// 调用方必须持有 m.mu；写入失败时不发起上游请求，避免重启后绕过冷却窗口。
func (m *autoTaskManager) reserveAutoCreationAttemptLocked(accountID string, now time.Time) error {
	oldGuard, hadGuard := m.creationGuards[accountID]
	prevCounter := m.autoDaily[accountID]
	m.creationGuards[accountID] = advanceGuard(oldGuard, now)
	m.recordAutoCreationLocked(accountID, now)
	if err := m.saveLocked(); err != nil {
		if hadGuard {
			m.creationGuards[accountID] = oldGuard
		} else {
			delete(m.creationGuards, accountID)
		}
		m.autoDaily[accountID] = prevCounter
		return err
	}
	return nil
}

// reserveManualCreation 原子地预留一次人工创建额度。人工创建与自动任务
// 共享每天 50 个的硬上限与滚动小时上限（任意 60 分钟 ≤5 次），
// 且同一账号任意两次「人工」创建尝试至少相隔 20 分钟。
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
	if hourlyWait(m.creationGuards[accountID], now) > 0 {
		return errCreationHourlyLimit
	}
	m.resetManualDailyLocked(accountID, now)
	if m.accountDailyCountLocked(accountID, taskDate(now)) >= maxTaskDailyLimit {
		return errCreationDailyLimit
	}
	oldGuard, hadGuard := m.creationGuards[accountID]
	m.creationGuards[accountID] = advanceGuard(oldGuard, now)
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
