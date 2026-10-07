package server

import (
	"math"
	mrand "math/rand/v2"
	"os"
	"sort"
	"testing"
	"time"
)

// testWeekday 返回固定在 2026-10-07(周三) 的时刻, 使日/周周期相关断言可复现
// (2026-10-07 为周三, 无论测试机时区如何, 该日历日的星期都是周三)。
func testWeekday(hour, min int) time.Time {
	return time.Date(2026, 10, 7, hour, min, 0, 0, time.Local)
}

// ====================================================================
// 拟人调度模型: 用户画像 / 分层 NHPP 采样 / 会话自激
// ====================================================================

// ---- 用户画像 ----

func TestPersonaForSeedStableAndCoversAll(t *testing.T) {
	// 稳定性: 同名种子的映射跨多次独立调用一致(纯函数, 与调用顺序无关)。
	// 注意不要写成 personaForSeed(i) != personaForSeed(i) 的自比较(恒真空断言);
	// 跨重启的稳定性由 TestTaskPersonaStableAcrossReload 钉住。
	seen := map[string]bool{}
	for i := uint64(0); i < 300; i++ {
		name := personaForSeed(i).Name
		if name == "" {
			t.Fatalf("种子 %d 映射到空画像", i)
		}
		seen[name] = true
	}
	for _, p := range personaTable {
		if !seen[p.Name] {
			t.Fatalf("画像 %s 未被任何种子命中(300 个种子内)", p.Name)
		}
	}
	if personaByName("不存在").Name != "standard" {
		t.Fatal("未知画像名应回退到 standard")
	}
}

func TestPersonaSleepWindowsAreFourToSixHours(t *testing.T) {
	for _, p := range personaTable {
		sleep := (p.SleepEnd - p.SleepStart + 24) % 24
		if sleep < 4 || sleep > 6 {
			t.Fatalf("%s 睡眠 %d 小时, 期望 4-6 小时", p.Name, sleep)
		}
		active := 24 - sleep
		if active < 18 || active > 20 {
			t.Fatalf("%s 活跃 %d 小时, 期望 18-20 小时", p.Name, active)
		}
	}
}

func TestPersonaCurveZeroInSleepAndPeaksAboveAverage(t *testing.T) {
	for _, p := range personaTable {
		cs := &p.weekday
		sum, active := 0.0, 0
		for h := 0; h < 24; h++ {
			if p.sleepingHour(h) {
				if cs.w[h] != 0 {
					t.Fatalf("%s 睡眠小时 %d 权重应为 0, got %v", p.Name, h, cs.w[h])
				}
				continue
			}
			if cs.w[h] <= 0 {
				t.Fatalf("%s 活跃小时 %d 权重应为正, got %v", p.Name, h, cs.w[h])
			}
			sum += cs.w[h]
			active++
		}
		avg := sum / float64(active)
		peak := 0.0
		for h := 0; h < 24; h++ {
			if cs.w[h] > peak {
				peak = cs.w[h]
			}
		}
		if peak < 1.4*avg {
			t.Fatalf("%s 峰值 %.3f 未显著高于均值 %.3f(日周期缺失?)", p.Name, peak, avg)
		}
	}
}

// circularMeanHour 返回曲线权重的圆周平均小时(处理跨零点作息)。
func circularMeanHour(c *curve) float64 {
	var sinSum, cosSum float64
	for h := 0; h < 24; h++ {
		if c.w[h] == 0 {
			continue
		}
		angle := (float64(h) + 0.5) / 24 * 2 * math.Pi
		sinSum += c.w[h] * math.Sin(angle)
		cosSum += c.w[h] * math.Cos(angle)
	}
	m := math.Atan2(sinSum, cosSum)
	if m < 0 {
		m += 2 * math.Pi
	}
	return m / (2 * math.Pi) * 24
}

func TestWeekendCurveSkewsLaterThanWeekday(t *testing.T) {
	for _, p := range personaTable {
		wd := circularMeanHour(&p.weekday)
		we := circularMeanHour(&p.weekend)
		if we <= wd {
			t.Fatalf("%s 周末活动重心 %.2f 应晚于工作日 %.2f(周周期缺失?)", p.Name, we, wd)
		}
	}
}

func TestTaskPersonaStableAcrossReload(t *testing.T) {
	dir := t.TempDir()
	m := newAutoTaskManager(dir+"/task.json", &taskBackend{})
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if task.Persona == "" {
		t.Fatal("创建时应分配画像")
	}
	m2 := newAutoTaskManager(dir+"/task.json", &taskBackend{})
	got, ok := m2.get(task.ID)
	if !ok || got.Persona != task.Persona {
		t.Fatalf("重载后画像应保持: got %q, want %q", got.Persona, task.Persona)
	}
}

func TestLegacyTaskPersonaBackfilledFromSeed(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"tasks":[{"id":"task_legacy","enabled":true,"account_id":"a","mode":"auto",` +
		`"daily_limit":20,"today_quota":20,"max_total":100,"next_number":1,"created_count":0,` +
		`"daily_count":0,"daily_date":"2026-01-01","last_success":0}]}`
	if err := os.WriteFile(dir+"/task.json", []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAutoTaskManager(dir+"/task.json", &taskBackend{})
	got, ok := m.get("task_legacy")
	if !ok {
		t.Fatal("旧任务应被加载")
	}
	if got.Persona == "" {
		t.Fatal("旧任务应从抽取种子回填画像")
	}
	if want := personaForSeed(got.LabelSeed).Name; got.Persona != want {
		t.Fatalf("回填画像 = %q, want %q", got.Persona, want)
	}
}

// ---- 分层 NHPP 采样 ----

func TestSampleAutoDelayNeverInSleep(t *testing.T) {
	for _, p := range personaTable {
		for _, now := range []time.Time{
			testWeekday(21, 30), testWeekday(22, 30), testWeekday(3, 0), testWeekday(5, 30),
		} {
			rng := mrand.New(mrand.NewPCG(1, 2))
			for i := 0; i < 150; i++ {
				d := sampleAutoDelay(p, now, 5, 10, creationGuard{}, rng.Float64)
				if at := now.Add(d); p.sleepingAt(at) {
					t.Fatalf("%s: 采样落在睡眠期 %v (now=%v, delay=%v)", p.Name, at, now, d)
				}
			}
		}
	}
}

func TestSampleAutoDelayNeverBelowMinSpacing(t *testing.T) {
	p := personaByName("standard")
	rng := mrand.New(mrand.NewPCG(3, 4))
	for i := 0; i < 2000; i++ {
		d := sampleAutoDelay(p, testWeekday(10, 0), 10, 20, creationGuard{}, rng.Float64)
		if d < minCreationSpacing {
			t.Fatalf("delay %v 低于最短间隔 %v", d, minCreationSpacing)
		}
	}
}

func TestSampleAutoDelaySelfCorrectsWithRemainingQuota(t *testing.T) {
	p := personaByName("standard")
	now := testWeekday(10, 0)
	medianDelay := func(done int) time.Duration {
		rng := mrand.New(mrand.NewPCG(7, 11))
		ds := make([]time.Duration, 300)
		for i := range ds {
			ds[i] = sampleAutoDelay(p, now, done, 20, creationGuard{}, rng.Float64)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	few := medianDelay(18) // 剩 2 个 → 目标分位靠后, 间隔更长
	many := medianDelay(1) // 剩 19 个 → 目标分位靠前, 间隔更短
	if few <= many {
		t.Fatalf("剩余 2 的间隔中位数 %v 应大于剩余 19 的 %v(配额自校正失效)", few, many)
	}
}

func TestSessionContinueProbabilityDecaysWithTime(t *testing.T) {
	p := personaByName("standard")
	just := sessionContinueProbability(p, 0)
	hour := sessionContinueProbability(p, time.Hour)
	far := sessionContinueProbability(p, 6*time.Hour)
	if !(just > hour && hour > far) {
		t.Fatalf("自激概率应随时间衰减: 0m=%v 60m=%v 6h=%v", just, hour, far)
	}
	if far < sessionBaseContinue-1e-9 || just > sessionBaseContinue+p.HawkesAlpha+1e-9 {
		t.Fatalf("概率超出 [base, base+alpha] 范围: %v / %v", far, just)
	}
}

// ---- 全天仿真: 死守全部约束 + 当日配额收敛 ----

// simulateAutoDay 用与生产相同的 nextAutoDelay + 账号级创建门控模拟画像的
// 一个完整作息日(从睡醒到次日同一时刻), 返回全部创建尝试时刻。
func simulateAutoDay(t *testing.T, p *persona, quota int, seed uint64) []time.Time {
	t.Helper()
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15)).Float64
	start := testWeekday(p.SleepEnd, 0)
	deadline := start.Add(24 * time.Hour)
	task := AliasTask{ID: "sim", Enabled: true, AccountID: "sim", Mode: taskModeAuto,
		DailyLimit: quota, TodayQuota: quota, MaxTotal: 1000, NextNumber: 1,
		Persona: p.Name, DailyDate: taskDate(start)}
	var attempts []time.Time
	sim := start
	for i := 0; i < 2000 && len(attempts) < quota; i++ {
		guard := creationGuard{}
		for _, a := range attempts {
			guard.RecentAttempts = append(guard.RecentAttempts, a.Format(time.RFC3339Nano))
		}
		if len(attempts) > 0 {
			guard.LastAttempt = attempts[len(attempts)-1].Format(time.RFC3339Nano)
		}
		m.mu.Lock()
		m.creationGuards["sim"] = guard
		task.DailyCount = len(attempts)
		m.tasks[task.ID] = task
		m.mu.Unlock()
		next := sim.Add(m.nextAutoDelay(task, sim))
		if !next.Before(deadline) {
			break
		}
		attempts = append(attempts, next)
		sim = next
	}
	return attempts
}

func TestSimulatedAutoDayObeysConstraintsAndDeliversQuota(t *testing.T) {
	for _, p := range personaTable {
		for _, seed := range []uint64{11, 42, 2026} {
			attempts := simulateAutoDay(t, p, 20, seed)
			if len(attempts) != 20 {
				t.Fatalf("%s seed=%d: 一个作息日创建 %d 个, 期望 20(配额收敛)", p.Name, seed, len(attempts))
			}
			for i, a := range attempts {
				if p.sleepingAt(a) {
					t.Fatalf("%s seed=%d: 第 %d 次创建落在睡眠期 %v", p.Name, seed, i, a)
				}
				lo := a.Add(-rollingHourWindow)
				count := 0
				for _, b := range attempts[:i+1] {
					if b.After(lo) {
						count++
					}
				}
				if count > maxCreationsPerHour {
					t.Fatalf("%s seed=%d: 第 %d 次创建时滚动 1 小时内已有 %d 次(上限 %d)", p.Name, seed, i, count, maxCreationsPerHour)
				}
			}
			for i := 1; i < len(attempts); i++ {
				if gap := attempts[i].Sub(attempts[i-1]); gap < minCreationSpacing {
					t.Fatalf("%s seed=%d: 间隔 %v 低于最短间隔 %v", p.Name, seed, gap, minCreationSpacing)
				}
			}
			// 成簇性: 会话内短间隔与会话间长间隔并存(不是均匀铺开)。
			short, long := 0, 0
			for i := 1; i < len(attempts); i++ {
				gap := attempts[i].Sub(attempts[i-1])
				if gap <= 45*time.Minute {
					short++
				}
				if gap >= 90*time.Minute {
					long++
				}
			}
			if short < 3 {
				t.Fatalf("%s seed=%d: 会话内短间隔仅 %d 个(期望 ≥3)", p.Name, seed, short)
			}
			if long < 2 {
				t.Fatalf("%s seed=%d: 会话间长间隔仅 %d 个(期望 ≥2)", p.Name, seed, long)
			}
		}
	}
}

func TestSimulatedAutoDayHandlesMaxQuota(t *testing.T) {
	p := personaByName("standard")
	attempts := simulateAutoDay(t, p, 50, 99)
	if len(attempts) != 50 {
		t.Fatalf("每日 50 个在作息日内只完成 %d 个(滚动上限下应仍可达成)", len(attempts))
	}
}

// ---- 效率基准 ----

func BenchmarkSampleAutoDelay(b *testing.B) {
	p := personaByName("standard")
	now := testWeekday(10, 0)
	rng := mrand.New(mrand.NewPCG(1, 2))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sampleAutoDelay(p, now, 10, 20, creationGuard{}, rng.Float64)
	}
}
