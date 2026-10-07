package server

import (
	"fmt"
	mrand "math/rand/v2"
	"testing"
	"time"
)

// ====================================================================
// 加速时间流速仿真
//
// 虚拟时钟驱动生产 runOnce 路径(与真实定时器调用的是同一条代码), 时间不
// 真实流逝: 每次循环把虚拟时间推进到生产代码算出的下次执行时刻(NextRun)。
// 通过大量种子 × 画像 × 配额组合重复运行, 排除实验偶然性。
//
// 注意: NextRun 以 RFC3339(秒级)持久化, 虚拟时钟据此推进会带来 <1 秒的
// 截断误差 —— 断言使用 simClockTolerance 容忍该仿真伪影, 算法本身的精确
// 边界(最短间隔、滚动窗口)由 auto_task_persona.go 的单元测试逐点钉住。
// ====================================================================

// simClockTolerance 是虚拟时钟推进的截断容差(RFC3339 秒级持久化)。
const simClockTolerance = 2 * time.Second

// newSimManager 构建一个由虚拟时钟驱动的任务管理器。
func newSimManager(t *testing.T, seed uint64) (*autoTaskManager, *taskBackend) {
	t.Helper()
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	m.rand = mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15)).Float64
	// 持久化与调度逻辑无关; 换成内存 no-op 以免仿真被磁盘 IO 拖慢。
	m.writeState = func(string, any) error { return nil }
	m.writeLogs = func(string, any) error { return nil }
	return m, be
}

// simulateVirtualDays 用虚拟时钟模拟 days 个自然日的完整调度, 返回全部创建
// 尝试时刻(虚拟时间)。任务直接构造(不走 create 的真实 10 秒启动协程)。
func simulateVirtualDays(t *testing.T, p *persona, quota int, seed uint64, days int) []time.Time {
	t.Helper()
	m, be := newSimManager(t, seed)
	now := testWeekday(p.SleepEnd, 0)
	m.now = func() time.Time { return now }
	task := AliasTask{ID: "task_sim", Enabled: true, AccountID: "sim", Mode: taskModeAuto,
		DailyLimit: quota, TodayQuota: quota, MaxTotal: 1_000_000, NextNumber: 1,
		Persona: p.Name, DailyDate: taskDate(now)}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()

	deadline := now.Add(time.Duration(days) * 24 * time.Hour)
	var attempts []time.Time
	for step := 0; step < 20000; step++ {
		if !now.Before(deadline) {
			break
		}
		before := len(be.labels)
		out := m.runOnce(task.ID)
		if len(be.labels) > before {
			attempts = append(attempts, now)
		}
		if out.NextRun == "" || !out.Enabled {
			break
		}
		next, perr := time.Parse(time.RFC3339, out.NextRun)
		if perr != nil {
			t.Fatalf("NextRun 解析失败: %v", perr)
		}
		if !next.After(now) {
			t.Fatalf("NextRun %v 未推进虚拟时钟(now=%v)", next, now)
		}
		now = next
	}
	return attempts
}

// assertSimConstraints 校验一次仿真的全部硬约束与配额收敛。
//
//  1. 睡眠窗口: 任何创建不落在画像睡眠期;
//  2. 滚动 60 分钟: 任意窗口内创建尝试 ≤ maxCreationsPerHour(死守红线);
//  3. 最短间隔: 相邻尝试 ≥ minCreationSpacing;
//  4. 每日上限: 任一自然日创建数 ≤ 2×quota(跨零点作息的一个周期横跨两个
//     自然日, 但任何自然日最多承载一个完整周期, 且账号级 50/天安全上限
//     由独立计数器保证);
//  5. 配额收敛: 每个完整作息周期恰好完成 quota 个(算法可行性)。
func assertSimConstraints(t *testing.T, p *persona, quota int, seed uint64, days int, attempts []time.Time) {
	t.Helper()
	label := fmt.Sprintf("%s seed=%d quota=%d days=%d", p.Name, seed, quota, days)
	if len(attempts) == 0 {
		t.Fatalf("%s: 仿真未产生任何创建", label)
	}
	// 1. 睡眠窗口
	for i, a := range attempts {
		if p.sleepingAt(a) {
			t.Fatalf("%s: 第 %d 次创建落在睡眠期 %v", label, i, a)
		}
	}
	// 2. 滚动 60 分钟窗口(闭区间口径): 任意窗口内 ≤ maxCreationsPerHour。
	// 窗口判定容忍 2 秒仿真伪影, 但任何超过容差的窗口(即真实违规)都会命中。
	for i := range attempts {
		for j := i + 1; j < len(attempts); j++ {
			if attempts[j].Sub(attempts[i]) > rollingHourWindow-simClockTolerance {
				break
			}
			if j-i+1 > maxCreationsPerHour {
				t.Fatalf("%s: 窗口 [%v, %v] 内 %d 次创建(上限 %d)",
					label, attempts[i], attempts[j], j-i+1, maxCreationsPerHour)
			}
		}
	}
	// 3. 最短间隔
	for i := 1; i < len(attempts); i++ {
		if gap := attempts[i].Sub(attempts[i-1]); gap < minCreationSpacing-simClockTolerance {
			t.Fatalf("%s: 间隔 %v 低于最短间隔 %v", label, gap, minCreationSpacing)
		}
	}
	// 4. 每日上限(自然日口径): 跨零点作息的一个周期横跨两个自然日,
	// 单日最多承载一个周期的量; 再加半周期余量容忍边界抖动。
	perDay := map[string]int{}
	for _, a := range attempts {
		perDay[taskDate(a)]++
	}
	for d, n := range perDay {
		if n > quota+quota/2+1 {
			t.Fatalf("%s: %s 创建 %d 个, 超过单日承载上限", label, d, n)
		}
	}
	// 5. 配额收敛: 完整作息周期(起点为睡醒时刻, 24 小时一循环)恰好完成 quota。
	cycleStart := testWeekday(p.SleepEnd, 0)
	perCycle := map[int]int{}
	for _, a := range attempts {
		k := int(a.Sub(cycleStart) / (24 * time.Hour))
		perCycle[k]++
	}
	for k := 0; k <= days-2; k++ {
		if perCycle[k] != quota {
			t.Fatalf("%s: 第 %d 个完整作息周期创建 %d 个, 期望 %d(配额收敛)",
				label, k, perCycle[k], quota)
		}
	}
}

// TestAcceleratedSimulationHoldsAllConstraints 加速仿真主矩阵:
// 全部画像 × 三档配额 × 25 个种子 × 3 天 = 300 次独立仿真。
func TestAcceleratedSimulationHoldsAllConstraints(t *testing.T) {
	quotas := []int{5, 20, 50}
	for _, p := range personaTable {
		for _, quota := range quotas {
			for seed := uint64(1); seed <= 25; seed++ {
				attempts := simulateVirtualDays(t, p, quota, seed, 3)
				assertSimConstraints(t, p, quota, seed, 3, attempts)
			}
		}
	}
}

// TestAcceleratedSimulationLongRun 长跑稳定性: 单任务 30 天连续调度,
// 验证配额收敛与约束在长期运行下不退化。
func TestAcceleratedSimulationLongRun(t *testing.T) {
	for _, p := range personaTable {
		attempts := simulateVirtualDays(t, p, 20, 2026, 30)
		assertSimConstraints(t, p, 20, 2026, 30, attempts)
		if len(attempts) < 28*20 {
			t.Fatalf("%s: 30 天仅创建 %d 个(期望 ≥560)", p.Name, len(attempts))
		}
	}
}

// TestAcceleratedSimulationWeekendShift 跨周末: 从周五睡醒起跑 4 天,
// 覆盖周末作息后移(睡眠窗口 +1 小时)的边界。
func TestAcceleratedSimulationWeekendShift(t *testing.T) {
	friday := time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local) // 周五
	for _, p := range personaTable {
		m, be := newSimManager(t, 77)
		now := time.Date(friday.Year(), friday.Month(), friday.Day(), p.SleepEnd, 0, 0, 0, friday.Location())
		m.now = func() time.Time { return now }
		task := AliasTask{ID: "task_sim", Enabled: true, AccountID: "sim", Mode: taskModeAuto,
			DailyLimit: 20, TodayQuota: 20, MaxTotal: 1_000_000, NextNumber: 1,
			Persona: p.Name, DailyDate: taskDate(now)}
		m.mu.Lock()
		m.tasks[task.ID] = task
		m.mu.Unlock()

		deadline := now.Add(4 * 24 * time.Hour)
		var attempts []time.Time
		for step := 0; step < 20000 && now.Before(deadline); step++ {
			before := len(be.labels)
			out := m.runOnce(task.ID)
			if len(be.labels) > before {
				attempts = append(attempts, now)
			}
			if out.NextRun == "" || !out.Enabled {
				break
			}
			next, perr := time.Parse(time.RFC3339, out.NextRun)
			if perr != nil {
				t.Fatalf("NextRun 解析失败: %v", perr)
			}
			if !next.After(now) {
				t.Fatalf("NextRun %v 未推进虚拟时钟(now=%v)", next, now)
			}
			now = next
		}
		// 周末窗口只校验睡眠与滚动红线(周期数因周末偏移不做精确断言)。
		if len(attempts) == 0 {
			t.Fatalf("%s: 跨周末仿真未产生创建", p.Name)
		}
		for i, a := range attempts {
			if p.sleepingAt(a) {
				t.Fatalf("%s: 第 %d 次创建落在睡眠期 %v", p.Name, i, a)
			}
		}
		for i := range attempts {
			for j := i + 1; j < len(attempts); j++ {
				if attempts[j].Sub(attempts[i]) > rollingHourWindow-simClockTolerance {
					break
				}
				if j-i+1 > maxCreationsPerHour {
					t.Fatalf("%s: 跨周末窗口 [%v, %v] 内 %d 次创建(上限 %d)",
						p.Name, attempts[i], attempts[j], j-i+1, maxCreationsPerHour)
				}
			}
		}
	}
}
