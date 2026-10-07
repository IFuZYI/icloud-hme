package server

import (
	"errors"
	"testing"
	"time"
)

// ====================================================================
// 滚动 60 分钟窗口约束: 任意起点窗口内创建尝试 ≤ maxCreationsPerHour
// ====================================================================

// mkGuard 构造含指定历史尝试的账号级门控(基于固定基准时刻)。
func mkGuard(base time.Time, offsets ...time.Duration) creationGuard {
	g := creationGuard{}
	for _, off := range offsets {
		g.RecentAttempts = append(g.RecentAttempts, base.Add(-off).Format(time.RFC3339Nano))
	}
	if len(offsets) > 0 {
		g.LastAttempt = base.Add(-offsets[0]).Format(time.RFC3339Nano)
	}
	return g
}

func TestHourlyWaitReleasesAfterOldestAttemptExitsWindow(t *testing.T) {
	base := testWeekday(10, 0)
	// 5 次都在窗口内 → 等到最早一次滑出(还差 10 分钟) + 1 秒安全余量。
	// 余量的意义: 计数按闭区间 [at-60min, at] 执行, 保证用户可见的任意
	// 「8:00-9:00」式窗口内尝试数 ≤5。
	g5 := mkGuard(base, 50*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if w := hourlyWait(g5, base); w != 10*time.Minute+time.Second {
		t.Fatalf("5 次尝试应等待 10 分钟+余量, got %v", w)
	}
	// 只有 4 次在窗口内(最早一次已滑出) → 放行。
	g4 := mkGuard(base, 70*time.Minute, 50*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute)
	if w := hourlyWait(g4, base); w != 0 {
		t.Fatalf("窗口内仅 4 次应放行, got %v", w)
	}
	// 6 次(异常数据) → 等到第 2 早的滑出。
	g6 := mkGuard(base, 50*time.Minute, 45*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if w := hourlyWait(g6, base); w != 15*time.Minute+time.Second {
		t.Fatalf("6 次尝试应等待 15 分钟+余量(第 2 早滑出), got %v", w)
	}
	// 空记录 → 放行。
	if w := hourlyWait(creationGuard{}, base); w != 0 {
		t.Fatalf("无记录应放行, got %v", w)
	}
	// 边界: 恰好 60 分钟前的尝试仍计入闭区间窗口 → 仍需等到其滑出。
	gEdge := mkGuard(base, 60*time.Minute, 50*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute)
	if w := hourlyWait(gEdge, base); w != time.Second {
		t.Fatalf("恰好 60 分钟前的尝试在闭区间内, 应等待 1 秒余量, got %v", w)
	}
}

func TestSpacingWaitEnforcesMinSpacing(t *testing.T) {
	now := testWeekday(10, 0)
	g := creationGuard{LastAttempt: now.Add(-30 * time.Second).Format(time.RFC3339Nano)}
	if w := spacingWait(g, now); w != minCreationSpacing-30*time.Second {
		t.Fatalf("间隔未满应等待 %v, got %v", minCreationSpacing-30*time.Second, w)
	}
	g2 := creationGuard{LastAttempt: now.Add(-minCreationSpacing).Format(time.RFC3339Nano)}
	if w := spacingWait(g2, now); w != 0 {
		t.Fatalf("间隔已满应放行, got %v", w)
	}
	if w := spacingWait(creationGuard{}, now); w != 0 {
		t.Fatalf("无记录应放行, got %v", w)
	}
}

// ---- 运行时门控(与生产 runOnce 同路径) ----

func TestRunOnceDefersWhenRollingHourCapReached(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// 固定时钟与画像: 否则结果取决于运行时刻(睡眠期会提前推迟, 使断言假通过)。
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	// 5 次尝试都在滚动窗口内, 而最近一次在 30 分钟前——远超 3 分钟最短间隔。
	// 这样门控等待只可能来自滚动小时上限: 若实现丢掉 hourly 分量, 等待为 0,
	// runOnce 会直接创建, 本测试即失败(反变异: 曾因 LastAttempt 太近而空断言)。
	guard := creationGuard{}
	for i := 0; i < maxCreationsPerHour; i++ {
		guard.RecentAttempts = append(guard.RecentAttempts, now.Add(-time.Duration(i+1)*time.Minute).Format(time.RFC3339Nano))
	}
	guard.LastAttempt = now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	m.mu.Lock()
	task.Enabled = true
	task.DailyCount = 0
	task.TodayQuota = 20
	task.DailyDate = taskDate(now)
	task.Persona = "standard"
	m.tasks[task.ID] = task
	m.creationGuards["a"] = guard
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if len(be.labels) != 0 {
		t.Fatalf("滚动小时已满时不应创建, got %v", be.labels)
	}
	if !out.Enabled {
		t.Fatalf("小时上限是推迟而非暂停: %+v", out)
	}
	if out.NextRun == "" {
		t.Fatal("应排定下次执行时间")
	}
	next, perr := time.Parse(time.RFC3339, out.NextRun)
	if perr != nil {
		t.Fatalf("NextRun 解析失败: %v", perr)
	}
	// 最早一次(-5min)滑出窗口需再等约 55 分钟(+1s 余量): 落点必须显著后移。
	if d := next.Sub(now); d < 54*time.Minute {
		t.Fatalf("推迟落点 %v 距现在仅 %v, 应等到窗口滑出(约 55min)", next, d)
	}
}

// 门控等待必须包含滚动小时分量: 只返回最短间隔的实现在此失败。
func TestGateWaitIncludesHourlyComponent(t *testing.T) {
	base := testWeekday(10, 0)
	// 5 次尝试在窗口内, 最近一次 30 分钟前(最短间隔早已满足):
	// 等待只能来自滚动小时上限。
	g := mkGuard(base, 5*time.Minute, 4*time.Minute, 3*time.Minute, 2*time.Minute, 1*time.Minute)
	g.LastAttempt = base.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	if w := gateWaitAt(g, base); w <= 50*time.Minute {
		t.Fatalf("门控等待 %v 未包含滚动小时分量(应约 55min)", w)
	}
	// 对照组: 仅最短间隔未满、窗口有位置 → 等待恰为最短间隔剩余。
	g2 := creationGuard{LastAttempt: base.Add(-time.Minute).Format(time.RFC3339Nano)}
	if w := gateWaitAt(g2, base); w != minCreationSpacing-time.Minute {
		t.Fatalf("对照组等待 %v, 期望 %v", w, minCreationSpacing-time.Minute)
	}
}

// 亚秒级边界: 最早一次恰在窗口边缘前 0.5 秒, 需等 0.5s + 1s 余量 = 1.5s,
// 落点严格晚于「最早尝试 + 60 分钟」, 闭区间内计数降为 4(反子秒缺口)。
func TestHourlyWaitSubSecondBoundary(t *testing.T) {
	base := testWeekday(10, 0)
	g := mkGuard(base, 59*time.Minute+59*time.Second+500*time.Millisecond,
		40*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if w := hourlyWait(g, base); w != 1500*time.Millisecond {
		t.Fatalf("亚秒边界等待 %v, 期望 1.5s", w)
	}
}

func TestRunOnceCreatesWhenWindowHasRoom(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	// 固定时钟与画像, 保证断言与运行时刻无关。
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	guard := creationGuard{}
	// 5 次尝试但全部在窗口之外(61 分钟前起) → 应放行。
	for i := 0; i < maxCreationsPerHour; i++ {
		guard.RecentAttempts = append(guard.RecentAttempts, now.Add(-time.Duration(61+i)*time.Minute).Format(time.RFC3339Nano))
	}
	guard.LastAttempt = guard.RecentAttempts[0]
	m.mu.Lock()
	task.Enabled = true
	task.DailyCount = 0
	task.TodayQuota = 20
	task.DailyDate = taskDate(now)
	task.Persona = "standard"
	m.tasks[task.ID] = task
	m.creationGuards["a"] = guard
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if len(be.labels) != 1 {
		t.Fatalf("窗口外尝试不应阻塞, got %v (out=%+v)", be.labels, out)
	}
}

func TestScheduledBatchStopsAtRollingHourCap(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	m.batchDelay = 0
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeScheduled, IntervalMinutes: 20, BatchCount: 20, TargetCount: 100})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if len(be.labels) != maxCreationsPerHour {
		t.Fatalf("批内创建应被滚动小时上限截断到 %d, got %d", maxCreationsPerHour, len(be.labels))
	}
	if !out.Enabled {
		t.Fatalf("批内截断是推迟而非暂停: %+v", out)
	}
}

func TestManualCreationRejectsWhenRollingHourFull(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	now := time.Now()
	m.now = func() time.Time { return now }
	guard := creationGuard{}
	// 5 次尝试都在滚动窗口内，但最近一次已超出 20 分钟人工冷却，
	// 使测试精确命中「滚动小时上限」这条分支。
	for i := 0; i < maxCreationsPerHour; i++ {
		guard.RecentAttempts = append(guard.RecentAttempts, now.Add(-time.Duration(25+i)*time.Minute).Format(time.RFC3339Nano))
	}
	guard.LastAttempt = now.Add(-minCreationCooldown - time.Minute).Format(time.RFC3339Nano)
	m.mu.Lock()
	m.creationGuards["a"] = guard
	m.mu.Unlock()

	if err := m.reserveManualCreation("a"); err != errCreationHourlyLimit {
		t.Fatalf("滚动小时已满时人工创建应被拒, got %v", err)
	}
}

// ---- 调度时区跟随代理出口 IP ----

// 任务时区在 runOnce 时从后端刷新(跟随代理出口 IP 的地理时区)。
func TestRunOnceRefreshesTimezoneFromBackend(t *testing.T) {
	be := &taskBackend{}
	be.tzValue = "America/Denver"
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	task.Persona = "standard"
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if out.Timezone != "America/Denver" {
		t.Fatalf("时区应跟随代理出口 IP 刷新, got %q", out.Timezone)
	}
	if be.tzID != "a" {
		t.Fatalf("应按账号 ID 查询时区, got %q", be.tzID)
	}
}

// 时区解析失败时保留原值, 调度回退本地时区, 绝不因解析失败而停摆。
func TestRunOnceKeepsTimezoneOnResolveFailure(t *testing.T) {
	be := &taskBackend{}
	be.tzErr = errors.New("全部地理解析服务失败")
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 用任务时区(上海)构造时刻, 与机器时区无关: 上海 14:00 是标准画像的清醒期。
	// (曾用 testWeekday 的机器本地时间: 在 America/Denver 下映射为上海 00:00
	// 睡眠期, 创建被推迟导致断言失败——复审员在 TZ=America/Denver 抓到。)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	now := time.Date(2026, 10, 7, 14, 0, 0, 0, loc)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	task.Persona = "standard"
	task.Timezone = "Asia/Shanghai"
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if out.Timezone != "Asia/Shanghai" {
		t.Fatalf("解析失败应保留原时区, got %q", out.Timezone)
	}
	if len(be.labels) != 1 {
		t.Fatalf("解析失败不应阻塞创建, got %v", be.labels)
	}
}

// 不同时区下作息窗口按当地时间计算: 同一 UTC 时刻在丹佛(UTC-6)是深夜睡眠,
// 在上海(UTC+8)是白天工作 —— 用采样落点验证时区确实生效。
func TestTimezoneAffectsSchedulingWindow(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = func() float64 { return 0.5 }
	// 2026-10-07 18:00 UTC = 丹佛 12:00(清醒) / 上海 02:00(standard 睡眠)。
	utc := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)

	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区数据库不可用: %v", err)
	}
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 0, TodayQuota: 20, Persona: "standard", AccountID: "a"}
	p := personaByName("standard")

	task.Timezone = "America/Denver"
	denverDelay := m.nextAutoDelay(task, utc)
	denverAt := utc.In(denver).Add(denverDelay)
	if p.sleepingAt(denverAt) {
		t.Fatalf("丹佛时区落点 %v 不应在睡眠期", denverAt)
	}

	task.Timezone = "Asia/Shanghai"
	shanghaiDelay := m.nextAutoDelay(task, utc)
	shanghaiAt := utc.In(shanghai).Add(shanghaiDelay)
	if p.sleepingAt(shanghaiAt) {
		t.Fatalf("上海时区落点 %v 不应在睡眠期", shanghaiAt)
	}
	// 上海当地是凌晨睡眠期 → 首个间隔应显著长于丹佛中午(至少跨过睡眠)。
	if shanghaiDelay <= denverDelay {
		t.Fatalf("上海(凌晨)间隔 %v 应长于丹佛(中午) %v", shanghaiDelay, denverDelay)
	}
}
