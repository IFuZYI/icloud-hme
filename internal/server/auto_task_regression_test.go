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

// 首次 runOnce 解析出任务时区后, 首日配额必须按任务(代理 IP)时区重新折算,
// 与计划安排同口径。
//
// 回归背景(用户实测): 配额在创建时按服务器本地时区折算(部署在 Asia/Shanghai
// 时≈浏览器时区)并直接展示, 而调度按代理 IP 时区 —— 「今日已生成/今日总数」
// 与计划安排口径不一致。修复: 解析出时区后, 若配额仍是首日折算值(非满额)
// 则按任务时区重算, 并写入 QuotaTimezone 标记避免重复折算; 周期日重写后
// 不得被 resetDailyCount 误判跨周期(否则折算被重置为满额、计数清零)。
func TestFirstRunReproratesQuotaInTaskTimezone(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 2026-10-07 08:00 UTC: 丹佛 02:00。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	// 时区未解析时, 配额与周期日按服务器本地时区写入(生产行为);
	// 本机时区与任务时区口径相同时无法区分新旧行为 → 跳过。
	hostQuota := firstDayQuota(20, now.In(time.Local))
	want := firstDayQuota(20, now.In(loc))
	if hostQuota == want {
		t.Skipf("本机口径与任务时区口径折算相同(%d), 无法区分新旧行为", want)
	}

	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟「时区尚未解析」的创建后状态: 配额与周期日均为服务器本地口径。
	m.mu.Lock()
	task.Persona = "standard"
	task.Timezone = ""
	task.DailyDate = personaByName("standard").cycleDateAt(now.In(time.Local))
	task.TodayQuota = hostQuota
	task.DailyCount = 3
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if out.Timezone != "America/Denver" {
		t.Fatalf("首次 runOnce 应解析出任务时区, got %q", out.Timezone)
	}
	if out.TodayQuota != want {
		t.Fatalf("首次解析后 TodayQuota = %d, want %d(按丹佛口径折算; 本机口径为 %d)", out.TodayQuota, want, hostQuota)
	}
	if out.DailyCount != 3 {
		t.Fatalf("时区解析后不得清零/重置计数: %d", out.DailyCount)
	}
	if out.DailyDate != "2026-10-06" {
		t.Fatalf("DailyDate = %q, want 丹佛周期日 2026-10-06(写回旧标签会误判跨周期)", out.DailyDate)
	}
	if out.QuotaTimezone != "America/Denver" {
		t.Fatalf("重折算后应标记 QuotaTimezone, got %q", out.QuotaTimezone)
	}
}

// 存量任务迁移: 旧版本已解析过时区(Timezone 非空)但配额仍是创建时服务器
// 本地口径的折算值(QuotaTimezone 标记缺失)。首次 runOnce 必须按任务时区
// 重折算一次 —— 这是对既有任务的修复路径(用户实测问题的存量部分)。
func TestLegacyTaskReproratesQuotaOnFirstRun(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	// 旧版本写入的任务: 时区已解析, 配额是服务器口径的折算值(13), 标记缺失。
	m.mu.Lock()
	m.tasks["task_old"] = AliasTask{ID: "task_old", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 13, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 0, DailyDate: "2026-10-06"}
	m.mu.Unlock()

	out := m.runOnce("task_old")
	if out.TodayQuota != 18 {
		t.Fatalf("存量任务首次 runOnce 后 TodayQuota = %d, want 18(按任务时区重折算)", out.TodayQuota)
	}
	if out.QuotaTimezone != "America/Denver" {
		t.Fatalf("重折算后应标记 QuotaTimezone = 任务时区, got %q", out.QuotaTimezone)
	}
	if len(be.labels) != 0 {
		t.Fatalf("丹佛 02:00 是睡眠期不应创建, got %v", be.labels)
	}
}

// 首次解析 + 周期日已过期: 旧周期日在旧时区(本地)下也已不是当前周期
// (如停用数月后再启用), 此时不得重写周期日掩盖重置 —— resetDailyCount
// 必须正常执行: 计数清零、配额恢复满额。
func TestFirstResolveStaleCycleStillResets(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC) // 丹佛 02:00(睡眠)
	m.now = func() time.Time { return now }

	// 旧数据: 时区从未解析, 周期日停在数月前, 计数 15。
	m.mu.Lock()
	m.tasks["task_stale"] = AliasTask{ID: "task_stale", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 10, MaxTotal: 100, NextNumber: 16,
		Persona: "standard", Timezone: "",
		DailyCount: 15, DailyDate: "2026-01-01"}
	m.mu.Unlock()

	out := m.runOnce("task_stale")
	if out.DailyCount != 0 {
		t.Fatalf("过期的周期日应触发重置: got count=%d(重写周期日掩盖了重置)", out.DailyCount)
	}
	if out.TodayQuota != 20 {
		t.Fatalf("新周期配额应为满额 20: got %d", out.TodayQuota)
	}
	if out.DailyDate != "2026-10-06" {
		t.Fatalf("DailyDate = %q, want 丹佛当前周期日 2026-10-06", out.DailyDate)
	}
	if out.Timezone != "America/Denver" {
		t.Fatalf("时区应解析, got %q", out.Timezone)
	}
}

// 存量任务迁移不得掩盖真实跨周期重置: 时区未变、但周期日已过期(跨周期)
// 的任务, 迁移只重算配额标记, 周期日重写必须留给 resetDailyCount 正常
// 判断——若迁移无条件重写周期日, 跨周期的计数清零会被掩盖。
func TestLegacyStaleCycleStillResetsOnMigration(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC) // 丹佛 02:00
	m.now = func() time.Time { return now }

	// 旧数据: 周期日停在很久以前(已跨周期), 计数 15, 配额为旧的折算值。
	m.mu.Lock()
	m.tasks["task_stale"] = AliasTask{ID: "task_stale", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 10, MaxTotal: 100, NextNumber: 16,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 15, DailyDate: "2026-01-01"}
	m.mu.Unlock()

	out := m.runOnce("task_stale")
	if out.DailyCount != 0 {
		t.Fatalf("跨周期的存量任务应被重置计数: got %d(迁移重写周期日会掩盖重置)", out.DailyCount)
	}
	if out.TodayQuota != 20 {
		t.Fatalf("跨周期后配额应为满额 20: got %d", out.TodayQuota)
	}
	if out.DailyDate != "2026-10-06" {
		t.Fatalf("DailyDate = %q, want 丹佛当前周期日 2026-10-06", out.DailyDate)
	}
}

// 首次解析之后的时区变更(如用户更换代理)不得重新折算当日配额:
// 配额已按旧时区折算过(QuotaTimezone 非空), 换时区只重写周期日、保留
// 快照——重新折算会把「首日按剩余时间折算」的语义泄漏到后续周期。
func TestTimezoneChangeAfterFirstResolveKeepsFullQuota(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "Asia/Tokyo"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	// 运行中的任务: 配额已按旧时区折算(15/20), 标记已写入。
	// 若实现无标记守卫而按新时区(东京 01:00 → 折算 19)重算, 断言即失败。
	m.mu.Lock()
	m.tasks["task_a"] = AliasTask{ID: "task_a", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 15, MaxTotal: 100, NextNumber: 5,
		Persona: "standard", Timezone: "America/New_York", QuotaTimezone: "America/New_York",
		DailyCount: 5, DailyDate: personaByName("standard").cycleDateAt(now.In(loc))}
	m.mu.Unlock()

	out := m.runOnce("task_a")
	if out.Timezone != "Asia/Tokyo" {
		t.Fatalf("时区应随代理变更刷新, got %q", out.Timezone)
	}
	if out.TodayQuota != 15 {
		t.Fatalf("后续时区变更不得重新折算配额: got %d, want 15(保留旧折算值)", out.TodayQuota)
	}
	if out.DailyCount != 5 {
		t.Fatalf("后续时区变更不得重置计数: got %d", out.DailyCount)
	}
}

// 存量任务迁移的边界: 满额配额(已跨过首日, 或首日折算恰好满额)不得被
// 重折算缩小——只写入标记, 配额保持满额。
func TestLegacyFullQuotaNotShrunkOnMigration(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 丹佛 02:00: 若误重算会得到 18 ≠ 20, 断言即失败。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	m.mu.Lock()
	m.tasks["task_full"] = AliasTask{ID: "task_full", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 20, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 0, DailyDate: "2026-10-06"}
	m.mu.Unlock()

	out := m.runOnce("task_full")
	if out.TodayQuota != 20 {
		t.Fatalf("满额配额不得被迁移重折算缩小: got %d, want 20", out.TodayQuota)
	}
	if out.QuotaTimezone != "America/Denver" {
		t.Fatalf("迁移后应写入标记, got %q", out.QuotaTimezone)
	}
}

// 首次解析重新折算时, 配额不得低于当日已创建计数(剩余不为负):
// 时区解析失败期间任务按本地时区运行并可能已创建若干; 解析成功后若新
// 时区折算值小于已创建数, 取已创建数(当天到此为止, 等下一周期恢复)。
func TestFirstResolveReprorationClampsToDailyCount(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "Asia/Tokyo"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// UTC 08:00 = 东京 17:00: 折算 20×7/24 ≈ 6; 已创建 8 个 → 钳制到 8。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if got := firstDayQuota(20, now.In(time.FixedZone("JST", 9*3600))); got != 6 {
		t.Fatalf("测试前提失效: 东京 17:00 折算 = %d, want 6", got)
	}

	m.mu.Lock()
	m.tasks["task_a"] = AliasTask{ID: "task_a", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 10, MaxTotal: 100, NextNumber: 9,
		Persona: "standard", Timezone: "",
		DailyCount: 8, DailyDate: personaByName("standard").cycleDateAt(now)}
	m.mu.Unlock()

	out := m.runOnce("task_a")
	if out.TodayQuota != 8 {
		t.Fatalf("折算值(6)低于已创建数(8)时应钳制到 8, got %d", out.TodayQuota)
	}
	if out.DailyCount != 8 {
		t.Fatalf("不得重置计数, got %d", out.DailyCount)
	}
	if len(be.labels) != 0 {
		t.Fatalf("配额已耗尽不应再创建, got %v", be.labels)
	}
}

// 跨周期编辑: update() 重新折算今日配额必须按任务时区(与调度/展示同口径)。
// 回归背景(用户实测): 折算用服务器本地时区, 「今日总数」显示成浏览器/
// 服务器时区的值, 与按代理 IP 时区计算的计划安排不一致。
func TestUpdateCrossCycleReproratesQuotaInTaskTimezone(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	// UTC 18:00 = 丹佛 12:00: 本机(UTC)口径折算 5, 丹佛口径折算 10。
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if firstDayQuota(20, now) == firstDayQuota(20, now.In(loc)) {
		// 本机时区与任务时区折算相同(如 TZ=America/Denver 的矩阵轮次),
		// 新旧口径不可区分 → 跳过; 其它时区轮次仍会执行并钉住修复。
		t.Skipf("本机口径与任务时区口径折算相同, 无法区分新旧行为")
	}
	m.mu.Lock()
	m.tasks["task_tz"] = AliasTask{ID: "task_tz", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 20, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 7, DailyDate: "2026-01-01"} // 停在旧周期
	m.mu.Unlock()

	updated, err := m.update("task_tz", aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// update() 以旧种子重派生画像; 本 fixture 的 LabelSeed 为 0, 期望值随之一致。
	p := personaForSeed(0)
	wantQuota := firstDayQuota(20, now.In(loc))
	if updated.TodayQuota != wantQuota {
		t.Fatalf("跨周期编辑 TodayQuota = %d, want %d(按任务时区折算; 服务器口径为 %d)",
			updated.TodayQuota, wantQuota, firstDayQuota(20, now))
	}
	if updated.DailyCount != 0 {
		t.Fatalf("跨周期编辑应清零计数, got %d", updated.DailyCount)
	}
	if updated.DailyDate != p.cycleDateAt(now.In(loc)) {
		t.Fatalf("DailyDate = %q, want %q(任务时区周期日)", updated.DailyDate, p.cycleDateAt(now.In(loc)))
	}
}

// 跨周期编辑写入的折算值必须带迁移标记: 编辑后若用户又更换代理(时区变化),
// 迁移逻辑不得把当日已定配额按新时区重复折算。若 update 不写标记, 时区变化
// 会被误判为「配额迁移待办」→ 配额被二次折算, 当日额度被改变。
func TestUpdateCrossCycleQuotaSurvivesLaterTimezoneChange(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 丹佛 12:00: 折算 10。
	now := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.tasks["task_x"] = AliasTask{ID: "task_x", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 20, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 7, DailyDate: "2026-01-01"} // 停在旧周期
	m.mu.Unlock()

	updated, err := m.update("task_x", aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TodayQuota != 10 {
		t.Fatalf("跨周期编辑折算 = %d, want 10", updated.TodayQuota)
	}

	// 数小时后用户更换代理 → 时区变为东京(UTC 10-08 00:00 = 东京 09:00,
	// 折算应为 13 —— 若被误重折算, 配额会从 10 变成 13)。
	be.tzValue = "Asia/Tokyo"
	now = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	out := m.runOnce("task_x")
	if out.Timezone != "Asia/Tokyo" {
		t.Fatalf("时区应随代理刷新, got %q", out.Timezone)
	}
	if out.TodayQuota != 10 {
		t.Fatalf("编辑后换代理不得重复折算配额: got %d, want 10(保持编辑时定的折算值)", out.TodayQuota)
	}
}

// 列表展示的每日计数/配额必须与任务时区口径的当前周期一致: 任务跨周期后
// 即使尚未触发 runOnce(如暂停后重新启用、长时间睡眠), 列表也不得显示旧
// 周期的计数——「今日已生成/今日总数」的展示与计划安排同口径。
func TestListReflectsCycleResetInTaskTimezone(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 丹佛 2026-10-07 02:00(standard 睡眠期, 不会触发 runOnce)。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }

	// 任务停在更早的周期: 计数 7、折算配额 10。
	m.mu.Lock()
	m.tasks["task_a"] = AliasTask{ID: "task_a", Enabled: true, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 10, MaxTotal: 100, NextNumber: 8,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 7, DailyDate: "2026-10-05"}
	m.mu.Unlock()

	list := m.list()
	if len(list) != 1 {
		t.Fatalf("list 应返回 1 个任务, got %d", len(list))
	}
	got := list[0]
	if got.DailyCount != 0 {
		t.Fatalf("跨周期后列表计数应为 0(展示与计划安排同口径), got %d", got.DailyCount)
	}
	if got.TodayQuota != 20 {
		t.Fatalf("跨周期后列表配额应为满额 20, got %d", got.TodayQuota)
	}
	if got.DailyDate != "2026-10-06" {
		t.Fatalf("DailyDate = %q, want 丹佛当前周期日 2026-10-06", got.DailyDate)
	}
}

// 列表读取必须一次性迁移存量任务的配额口径: 用户现有任务(时区已解析、
// 配额仍是服务器口径折算值、无迁移标记)即使暂停/睡眠期不触发 runOnce,
// 打开任务页也必须看到按任务时区折算的「今日总数」。
func TestListMigratesLegacyQuota(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 服务器(UTC)口径折算 13; 丹佛口径折算 18。
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	// 旧数据: 周期日已是丹佛当前周期(未跨周期), 配额是服务器口径的 13。
	m.mu.Lock()
	m.tasks["task_legacy"] = AliasTask{ID: "task_legacy", Enabled: false, AccountID: "a", Mode: taskModeAuto,
		DailyLimit: 20, TodayQuota: 13, MaxTotal: 100, NextNumber: 1,
		Persona: "standard", Timezone: "America/Denver",
		DailyCount: 2, DailyDate: personaByName("standard").cycleDateAt(now.In(loc))}
	m.mu.Unlock()

	list := m.list()
	if len(list) != 1 {
		t.Fatalf("list 应返回 1 个任务, got %d", len(list))
	}
	got := list[0]
	if got.TodayQuota != 18 {
		t.Fatalf("列表读取应迁移配额到任务时区口径: got %d, want 18(服务器口径为 13)", got.TodayQuota)
	}
	if got.QuotaTimezone != "America/Denver" {
		t.Fatalf("迁移后应写入标记, got %q", got.QuotaTimezone)
	}
	if got.DailyCount != 2 {
		t.Fatalf("迁移不得重置计数: got %d", got.DailyCount)
	}
	// 再次读取: 迁移只做一次(标记已写入), 配额不得再变化。
	again := m.list()[0]
	if again.TodayQuota != 18 {
		t.Fatalf("二次读取配额不应变化: got %d", again.TodayQuota)
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

// ====================================================================
// 定时任务的「日」边界跟随任务(代理 IP)时区
// ====================================================================

// 定时任务的每日重置必须按任务时区的自然日: 服务器(UTC)与丹佛的日期不同时,
// 不得以服务器日期误判跨日(重置会清空计数、显示与任务口径对不上)。
func TestScheduledDailyResetUsesTaskTimezone(t *testing.T) {
	if _, err := time.LoadLocation("America/Denver"); err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	// UTC 03:00 = 丹佛 10-06 21:00: 任务日(丹佛)仍是 10-06。
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	task := AliasTask{Mode: taskModeScheduled, DailyLimit: 50, DailyCount: 40,
		DailyDate: "2026-10-06", Timezone: "America/Denver"}
	got := resetDailyCount(task, now)
	if got.DailyCount != 40 || got.DailyDate != "2026-10-06" {
		t.Fatalf("任务时区(丹佛)尚未跨日, 不得重置: count=%d date=%q", got.DailyCount, got.DailyDate)
	}
	// UTC 07:00 = 丹佛 10-07 01:00: 跨日 → 重置。
	now2 := time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC)
	got2 := resetDailyCount(got, now2)
	if got2.DailyCount != 0 || got2.DailyDate != "2026-10-07" {
		t.Fatalf("丹佛跨日后应重置: count=%d date=%q", got2.DailyCount, got2.DailyDate)
	}
}

// 定时任务配额用尽后的下次执行时刻必须落在任务时区的次日零点(而非服务器
// 本地零点), 与「每日边界跟随代理 IP 时区」一致。
func TestScheduledNextRunTargetsTaskTimezoneMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC) // 丹佛 10-06 21:00
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.tasks["task_s"] = AliasTask{ID: "task_s", Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, DailyLimit: 50, MaxTotal: 100, NextNumber: 1,
		Timezone: "America/Denver", DailyCount: 50, DailyDate: "2026-10-06"}
	m.mu.Unlock()

	out := m.runOnce("task_s")
	if len(be.labels) != 0 {
		t.Fatalf("配额已用尽不应创建, got %v", be.labels)
	}
	next, perr := time.Parse(time.RFC3339, out.NextRun)
	if perr != nil {
		t.Fatalf("NextRun 解析失败: %v", perr)
	}
	want := time.Date(2026, 10, 7, 0, 5, 0, 0, loc) // 丹佛次日 00:05
	if !next.Equal(want) {
		t.Fatalf("NextRun = %v, want %v(任务时区次日零点, 而非服务器零点)", next, want)
	}
}

// 列表展示的定时任务计数必须按任务时区惰性刷新: 任务日尚未跨(服务器日已跨)
// 时不得清空计数; 任务日跨过后显示归零。
func TestListRefreshesScheduledCountInTaskTimezone(t *testing.T) {
	if _, err := time.LoadLocation("America/Denver"); err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC) // 丹佛 10-06 21:00
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.tasks["task_s"] = AliasTask{ID: "task_s", Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, DailyLimit: 50, MaxTotal: 100, NextNumber: 1,
		Timezone: "America/Denver", DailyCount: 40, DailyDate: "2026-10-06"}
	m.mu.Unlock()

	list := m.list()
	if got := list[0]; got.DailyCount != 40 || got.DailyDate != "2026-10-06" {
		t.Fatalf("任务时区尚未跨日, 列表不得重置: count=%d date=%q", got.DailyCount, got.DailyDate)
	}

	// 丹佛跨日(UTC 07:00 = 丹佛 10-07 01:00)后: 列表显示归零。
	now = time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	list2 := m.list()
	if got := list2[0]; got.DailyCount != 0 || got.DailyDate != "2026-10-07" {
		t.Fatalf("丹佛跨日后列表应归零: count=%d date=%q", got.DailyCount, got.DailyDate)
	}
}

// 定时任务换代理时区的周期日口径修正: 旧日(旧时区口径)在旧时区下仍是当前
// 日时, 重写为新时区当前日, 避免下一次 resetDailyCount 因两地日期不同而
// 误判跨日、清空计数。配额设满避免创建干扰断言。
func TestScheduledDailyDateRewrittenOnTimezoneChange(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Tokyo"); err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	be := &taskBackend{}
	be.tzValue = "Asia/Tokyo"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// UTC 03:00 = 丹佛 10-06 21:00 = 东京 10-07 12:00: 换时区后两地日期不同。
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.tasks["task_s"] = AliasTask{ID: "task_s", Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, DailyLimit: 50, MaxTotal: 100, NextNumber: 1,
		Timezone: "America/Denver", DailyCount: 50, DailyDate: "2026-10-06"}
	m.mu.Unlock()

	out := m.runOnce("task_s")
	if out.Timezone != "Asia/Tokyo" {
		t.Fatalf("时区应刷新, got %q", out.Timezone)
	}
	if out.DailyCount != 50 {
		t.Fatalf("换时区不得清空计数: got %d(周期日未重写是回归点)", out.DailyCount)
	}
	if out.DailyDate != "2026-10-07" {
		t.Fatalf("DailyDate = %q, want 东京当前日 2026-10-07", out.DailyDate)
	}
	if len(be.labels) != 0 {
		t.Fatalf("配额已满不应创建, got %v", be.labels)
	}
}

// 定时任务换代理时区 + 旧日已过期: 保留旧日让 resetDailyCount 正常重置
// (重置后配额有空, 正常创建 1 个 → 计数为 1 而非旧值)。
func TestScheduledStaleDateStillResetsOnTimezoneChange(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Tokyo"); err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	be := &taskBackend{}
	be.tzValue = "Asia/Tokyo"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.mu.Lock()
	m.tasks["task_s"] = AliasTask{ID: "task_s", Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, DailyLimit: 50, MaxTotal: 100, NextNumber: 1,
		Timezone: "America/Denver", DailyCount: 40, DailyDate: "2026-01-01"}
	m.mu.Unlock()

	out := m.runOnce("task_s")
	// 过期日被重置(0)后配额有空, 正常创建 1 个 → 1。若重写周期日掩盖重置,
	// 计数会保留 40 并再创建 1 个 → 41, 断言即失败。
	if out.DailyCount != 1 {
		t.Fatalf("过期的周期日应触发重置后正常创建: got %d(want 1; 40=未重置, 41=掩盖重置)", out.DailyCount)
	}
	if out.DailyDate != "2026-10-07" {
		t.Fatalf("DailyDate = %q, want 东京当前日 2026-10-07", out.DailyDate)
	}
	if len(be.labels) != 1 {
		t.Fatalf("重置后应正常创建 1 个, got %v", be.labels)
	}
}
