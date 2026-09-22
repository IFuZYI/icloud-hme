package server

import (
	"testing"
	"time"
)

// atHour 返回今天指定小时的时刻，便于活动窗口相关断言。
func atHour(hour, min int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
}

func TestNextAutoDelayPacesRemainingBudgetWithinWindow(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	// 关闭随机扰动，验证纯配速逻辑。
	m.jitter = func() float64 { return 1.0 }

	// 每日 10 个，已创建 0 个，正处于窗口起点 08:00。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 0}
	now := atHour(autoActiveStartHour, 0)
	delay := m.nextAutoDelay(task, now)

	// 窗口 08:00-24:00 共 16h，10 个 → 基准约 96 分钟。
	windowMinutes := float64(autoActiveEndHour-autoActiveStartHour) * 60
	want := time.Duration(windowMinutes/10) * time.Minute
	if delay != want {
		t.Fatalf("base pacing delay = %v, want %v", delay, want)
	}
}

func TestNextAutoDelaySelfCorrectsWhenBehind(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }

	// 每日 10 个，已到 20:00 还剩 8 个未创建：剩余 4h/8 = 30 分钟，比初始基准更密。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 2}
	now := atHour(20, 0)
	delay := m.nextAutoDelay(task, now)
	want := 30 * time.Minute
	if delay != want {
		t.Fatalf("self-correct delay = %v, want %v", delay, want)
	}
}

func TestNextAutoDelayNeverBelowCooldown(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }

	// 每日 50 个但只剩 5 分钟窗口：基准会远小于冷却下限，必须被夹到 20 分钟。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 50, DailyCount: 0}
	now := atHour(autoActiveEndHour, 0).Add(-5 * time.Minute) // 23:55
	delay := m.nextAutoDelay(task, now)
	if delay < minCreationCooldown {
		t.Fatalf("delay %v breached cooldown floor %v", delay, minCreationCooldown)
	}
}

func TestNextAutoDelayWaitsForWindowStart(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }

	// 凌晨 03:00 处于窗口外，应顺延到窗口起点 08:00。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 0}
	now := atHour(3, 0)
	delay := m.nextAutoDelay(task, now)
	want := atHour(autoActiveStartHour, 0).Sub(now)
	if delay != want {
		t.Fatalf("pre-window delay = %v, want %v (until %02d:00)", delay, want, autoActiveStartHour)
	}
}

func TestNextAutoDelayDefersWhenDailyBudgetExhausted(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }

	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 10}
	now := atHour(14, 0)
	delay := m.nextAutoDelay(task, now)
	want := nextDailyRun(now).Sub(now)
	if delay != want {
		t.Fatalf("exhausted-budget delay = %v, want defer to next day %v", delay, want)
	}
}

func TestTriangularJitterStaysWithinBounds(t *testing.T) {
	lo := 1 - autoJitterSigma
	hi := 1 + autoJitterSigma
	for i := 0; i < 10000; i++ {
		f := triangularJitter()
		if f < lo-1e-9 || f > hi+1e-9 {
			t.Fatalf("jitter factor %v out of [%v,%v]", f, lo, hi)
		}
	}
}

func TestActiveWindowExpandsForLargeDailyCount(t *testing.T) {
	now := atHour(12, 0)
	// 50 个 × 20 分钟冷却 = 1000 分钟 = 16.67h，超过默认 16h 窗口，起点须前移。
	start, end := autoActiveWindow(now, 50)
	span := end.Sub(start)
	needed := time.Duration(50) * minCreationCooldown
	if span < needed {
		t.Fatalf("window span %v cannot fit %v of required cooldown", span, needed)
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if start.Before(dayStart) {
		t.Fatalf("window start %v moved before midnight", start)
	}
}
