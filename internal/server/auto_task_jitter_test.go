package server

import (
	"testing"
	"time"
)

// atHour 返回固定日期(2026-10-07 周三)的时刻, 与 testWeekday 同源,
// 避免测试依赖运行时刻的日历日(跨零点运行时可能翻转, 曾造成偶发失败)。
func atHour(hour, min int) time.Time {
	return time.Date(2026, 10, 7, hour, min, 0, 0, time.Local)
}

// ---- 拟人模型在调度路径上的行为（配速/自校正/窗口边界）----

func TestNextAutoDelaySelfCorrectsWhenBehind(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = func() float64 { return 0.5 }

	// 20:00(standard 画像): 剩余配额越少, 平均间隔越长(把配额铺开到入睡前),
	// 即落后时自动加速、完成多时自动放缓——自校正方向必须被定量断言。
	// 反变异: 实现若用 DailyLimit 而非剩余配额(丢掉自校正), 两者相等, 本测试失败。
	now := testWeekday(20, 0)
	full := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 0, TodayQuota: 10, Persona: "standard"}
	late := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 8, TodayQuota: 10, Persona: "standard"}
	dFull := m.nextAutoDelay(full, now)
	dLate := m.nextAutoDelay(late, now)
	if dFull <= 0 || dLate <= 0 {
		t.Fatalf("delay 应为正: full=%v late=%v", dFull, dLate)
	}
	if dLate <= dFull {
		t.Fatalf("自校正方向错误: 剩余少应间隔更长(late=%v), 剩余多应更短(full=%v)", dLate, dFull)
	}
}

func TestNextAutoDelayNeverBelowSpacingFloor(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = func() float64 { return 0.5 }

	// 每日 50 个但已接近入睡：间隔不得低于最短间隔地板。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 50, DailyCount: 0, TodayQuota: 50, Persona: "standard"}
	delay := m.nextAutoDelay(task, testWeekday(22, 50))
	if delay < minCreationSpacing {
		t.Fatalf("delay %v 低于最短间隔 %v", delay, minCreationSpacing)
	}
}

func TestNextAutoDelayWaitsForWakeDuringSleep(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = func() float64 { return 0.5 }

	// 03:00 处于 standard 画像(23-5 睡)的睡眠期，应顺延到睡醒之后。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 0, TodayQuota: 10, Persona: "standard"}
	now := testWeekday(3, 0)
	delay := m.nextAutoDelay(task, now)
	at := now.Add(delay)
	if p := personaByName("standard"); p.sleepingAt(at) {
		t.Fatalf("落点 %v 仍在睡眠期 (delay=%v)", at, delay)
	}
	if delay < 2*time.Hour {
		t.Fatalf("delay = %v, 应至少等到 05:00 后", delay)
	}
}

func TestNextAutoDelayDefersWhenDailyBudgetExhausted(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.rand = func() float64 { return 0.5 }

	task := AliasTask{Mode: taskModeAuto, DailyLimit: 10, DailyCount: 10, TodayQuota: 10, Persona: "standard"}
	now := testWeekday(14, 0)
	delay := m.nextAutoDelay(task, now)
	at := now.Add(delay)
	if p := personaByName("standard"); p.sleepingAt(at) {
		t.Fatalf("配额耗尽后的落点 %v 不应在睡眠期", at)
	}
	if delay < time.Hour {
		t.Fatalf("配额耗尽应推迟到下一作息段, got %v", delay)
	}
}
