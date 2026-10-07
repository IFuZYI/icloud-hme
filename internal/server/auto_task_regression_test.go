package server

import (
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

// blockingBackend 的 CreateAlias 会阻塞直到 release 关闭, 用于模拟慢上游。
type blockingBackend struct {
	taskBackend
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	close(b.entered)
	<-b.release
	return b.taskBackend.CreateAlias(accountID, label)
}

// 账号级 50/天上限的 TOCTOU 回归: 计数必须在上游调用**之前**落账。
//
// 旧实现把计数推迟到 CreateAlias 返回后, 同一账号的另一个任务可在慢调用
// 期间通过同一配额检查(49 < 50), 两个任务各记一次 → 突破上限(审查探针
// 实测 49+2=51)。本测试在 CreateAlias 阻塞期间检查计数已经到 50。
func TestAccountDailyCountIncrementsBeforeUpstreamCall(t *testing.T) {
	be := &blockingBackend{entered: make(chan struct{}), release: make(chan struct{})}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 持久化与断言无关, 换成 no-op 避免磁盘 IO。
	m.writeState = func(string, any) error { return nil }
	m.writeLogs = func(string, any) error { return nil }
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }

	m.mu.Lock()
	// 预置: 该账号今天已创建 49 个(距上限差 1)。
	m.autoDaily["a"] = dailyCreationCounter{Date: taskDate(now), Count: maxTaskDailyLimit - 1}
	m.tasks["task_a"] = AliasTask{ID: "task_a", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 50, TodayQuota: 50, MaxTotal: 100, NextNumber: 1, Persona: "standard", DailyDate: taskDate(now)}
	m.mu.Unlock()

	go m.runOnce("task_a")
	select {
	case <-be.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runOnce 未进入上游调用")
	}

	// 上游仍在阻塞: 计数必须已经 +1 到 50(修复的核心断言)。
	m.mu.Lock()
	count := m.autoDaily["a"].Count
	m.mu.Unlock()
	if count != maxTaskDailyLimit {
		t.Fatalf("上游调用期间账号计数 = %d, 期望 %d(计数必须先于上游调用落账)", count, maxTaskDailyLimit)
	}
	// 同一时刻的配额检查也必须看到 50: 第二个任务不会通过检查。
	if got := m.accountDailyCountLocked("a", taskDate(now)); got < maxTaskDailyLimit {
		t.Fatalf("accountDailyCountLocked = %d, 期望 ≥ %d", got, maxTaskDailyLimit)
	}

	close(be.release)
	// 等待 task_a 完成(轮询 labels)。
	deadline := time.Now().Add(5 * time.Second)
	for {
		be.mu.Lock()
		n := len(be.labels)
		be.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task_a 未完成")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	final := m.autoDaily["a"].Count
	m.mu.Unlock()
	if final != maxTaskDailyLimit {
		t.Fatalf("完成后账号计数 = %d, 期望恰好 %d(不得重复计数)", final, maxTaskDailyLimit)
	}
}

// update() 回归: 编辑任务必须保持画像与种子一致, 且同一作息周期内不重置配额。
// 回归背景: normalizeAliasTaskAt 按新种子派生画像, 旧实现直接采用该值 →
// 编辑静默改变作息(12 次编辑 10 次变化)并借周期日变化重置每日配额。
func TestUpdateKeepsPersonaAndQuotaWithinCycle(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := testWeekday(12, 0)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if task.TodayQuota != 10 {
		t.Fatalf("中午创建配额应为 10, got %d", task.TodayQuota)
	}
	// 编辑 12 次(深夜): 画像必须始终与种子一致, 配额不得被重置。
	m.now = func() time.Time { return testWeekday(23, 50) }
	for i := 0; i < 12; i++ {
		updated, err := m.update(task.ID, aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
		if err != nil {
			t.Fatal(err)
		}
		want := personaForSeed(updated.LabelSeed).Name
		if updated.Persona != want {
			t.Fatalf("第 %d 次编辑后画像 = %q, 种子派生 = %q(编辑改变了作息)", i+1, updated.Persona, want)
		}
		if updated.TodayQuota != 10 {
			t.Fatalf("第 %d 次编辑后 TodayQuota = %d, want 10(同周期编辑不得重置配额)", i+1, updated.TodayQuota)
		}
	}
}

// 跨零点画像(night_owl)的首日折算: 凌晨创建时 TodayQuota 按剩余时间折算,
// 且 DailyDate 必须写作息周期日(而非日历日), 使首次 runOnce 不把它当跨周期
// 重置为满额。回归背景: 写日历日 → 周期日比较天然不等 → 折算被重置。
func TestNightOwlFirstDayQuotaSurvivesFirstRun(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// night_owl 画像(睡 3-9)在 09:00 睡醒; 用种子反推一个 night_owl 任务不可控,
	// 直接构造任务并在 14:00(清醒期)创建折算。
	p := personaByName("night_owl")
	now := testWeekday(14, 0)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// 强制画像为 night_owl(验证跨零点周期口径)。
	m.mu.Lock()
	task.Persona = p.Name
	task.DailyDate = p.cycleDateAt(now.In(time.Local))
	task.TodayQuota = firstDayQuota(20, now)
	m.tasks[task.ID] = task
	m.mu.Unlock()
	quotaBefore := task.TodayQuota
	if quotaBefore == 0 || quotaBefore == 20 {
		t.Fatalf("14:00 折算应介于 0 与 20 之间, got %d", quotaBefore)
	}

	// 首次 runOnce 后配额必须保持折算值(不得被重置为满额)。
	out := m.runOnce(task.ID)
	if out.TodayQuota != quotaBefore {
		t.Fatalf("首次 runOnce 后 TodayQuota = %d, want %d(折算被重置是回归点)", out.TodayQuota, quotaBefore)
	}
	if out.DailyDate != p.cycleDateAt(now.In(time.Local)) {
		t.Fatalf("DailyDate = %q, 应为周期日 %q", out.DailyDate, p.cycleDateAt(now.In(time.Local)))
	}
}

// 跨时区编辑: update() 的周期日比较必须按任务时区(与 resetDailyCount 同口径)。
// 回归背景(复审探针实测): 用服务器本地时区比较, 在任务时区与服务器不同且日期
// 已跨天时, 编辑被误判为「跨周期」→ DailyCount 清零、配额被重算, 且 Timezone
// 字段被丢弃(下次 runOnce 才重新解析)。
func TestUpdateKeepsCycleAndTimezoneAcrossTimezones(t *testing.T) {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	// UTC 08:00 = Denver 02:00: 两地的周期日不同(UTC 周三 / Denver 周二)。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	p := personaByName("standard")
	denverCycle := p.cycleDateAt(now.In(loc))
	if denverCycle == p.cycleDateAt(now) {
		t.Fatalf("测试前提失效: Denver 与服务器周期日应不同")
	}

	task := AliasTask{ID: "task_tz", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 20, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 15, DailyDate: denverCycle}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()

	updated, err := m.update(task.ID, aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DailyCount != 15 || updated.TodayQuota != 20 || updated.DailyDate != denverCycle {
		t.Fatalf("同周期编辑不得重置计数: count=%d quota=%d date=%q (want 15/20/%q)",
			updated.DailyCount, updated.TodayQuota, updated.DailyDate, denverCycle)
	}
	if updated.Timezone != "America/Denver" {
		t.Fatalf("编辑必须保留已解析时区, got %q(丢弃后与周期日口径不一致)", updated.Timezone)
	}
}

// 首次 runOnce 解析出任务时区后, 首日折算的配额不得被误重置。
// 回归背景(复审探针实测): DailyDate 按服务器本地周期日写入; 首次刷新时区后
// resetDailyCount 按任务时区比较 → 两地周期日不同时误判跨周期, 折算配额被
// 重置为满额。修复: refreshTimezone 在写入新时区时同步重写周期日。
func TestFirstRunTimezoneSwitchKeepsProratedQuota(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// UTC 08:00 = Denver 02:00(两地周期日不同)。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// 强制画像使周期日计算可预期, 并把时区清空以模拟「尚未解析」。
	m.mu.Lock()
	task.Persona = "standard"
	task.Timezone = ""
	task.DailyDate = personaByName("standard").cycleDateAt(now)
	task.TodayQuota = firstDayQuota(20, now)
	m.tasks[task.ID] = task
	m.mu.Unlock()
	quotaBefore := task.TodayQuota
	if quotaBefore == 0 || quotaBefore == 20 {
		t.Fatalf("测试前提: 08:00 UTC 折算应介于 0 与 20 之间, got %d", quotaBefore)
	}

	out := m.runOnce(task.ID)
	if out.Timezone != "America/Denver" {
		t.Fatalf("首次 runOnce 应解析出任务时区, got %q", out.Timezone)
	}
	if out.TodayQuota != quotaBefore {
		t.Fatalf("时区解析后首日配额被重置: %d -> %d(误判跨周期是回归点)", quotaBefore, out.TodayQuota)
	}
	if out.DailyCount != 0 {
		t.Fatalf("时区解析后不得清零/重置计数: %d", out.DailyCount)
	}
}

// normalizeAliasTaskAt 必须把 DailyDate 写作息周期日(而非日历日)。
// 回归背景(复审变异 M6 存活): 若 normalize 写日历日, 跨零点画像(如 standard
// 在 02:00)的周期日与日历日不同 → 首次 runOnce 被判跨周期, 折算配额被重置。
// 先前的 TestNightOwlFirstDayQuotaSurvivesFirstRun 手动覆盖了 DailyDate,
// 没有钉住 normalize 本身, 故本测试直接断言 normalize 的产物。
func TestNormalizeWritesCycleDateNotCalendarDate(t *testing.T) {
	// 02:00 时所有画像的周期日都是前一天(睡醒周期尚未开始新的一天)。
	now := time.Date(2026, 10, 7, 2, 0, 0, 0, time.Local)
	if taskDate(now) == "" {
		t.Fatal("测试前提失效")
	}
	cal := taskDate(now)
	for _, p := range personaTable {
		cycle := p.cycleDateAt(now)
		if cycle == cal {
			t.Fatalf("测试前提失效: %s 在 02:00 的周期日不应等于日历日", p.Name)
		}
	}
	task, err := normalizeAliasTaskAt(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20}, now)
	if err != nil {
		t.Fatal(err)
	}
	p := personaByName(task.Persona)
	want := p.cycleDateAt(now)
	if task.DailyDate != want {
		t.Fatalf("normalize 的 DailyDate = %q, want 周期日 %q(写日历日会让首日折算被误重置)", task.DailyDate, want)
	}
	if task.DailyDate == cal {
		t.Fatalf("normalize 的 DailyDate 写成了日历日 %q", cal)
	}
}
