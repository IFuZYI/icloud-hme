package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

// ============ 手动创建别名: 标签自由输入 ============

func TestCreateAliasAcceptsCustomLabel(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com", Label: "我的标签"}}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"我的自定义标签"}`)
	if status != http.StatusOK {
		t.Fatalf("自定义标签应被接受, got %d: %s", status, body)
	}
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestCreateAliasRejectsEmptyLabel(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com"}}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	for _, label := range []string{``, `   `} {
		status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"`+label+`"}`)
		if status != http.StatusBadRequest || !strings.Contains(body, `"code":"VALIDATION_ERROR"`) {
			t.Fatalf("空标签 %q 应 400, got %d: %s", label, status, body)
		}
	}
}

func TestCreateAliasRejectsOverlongLabel(t *testing.T) {
	f := &fakeBackend{created: &hme.CreateResult{Email: "new@icloud.com"}}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	long := strings.Repeat("标", 201)
	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodPost, "/api/create", `{"account_id":"acc_1","label":"`+long+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("超长标签应 400, got %d: %s", status, body)
	}
}

// ============ 自主任务: 每日数量任意输入 + 默认 20 ============

func TestNormalizeAutoTaskAcceptsArbitraryDailyLimit(t *testing.T) {
	// 任意 1-50 的整数均可(不再限定 5 的倍数)。
	for _, n := range []int{1, 7, 17, 33, 50} {
		in := aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 20, DailyLimit: n}
		task, err := normalizeAliasTask(in)
		if err != nil {
			t.Fatalf("daily_limit=%d 应被接受: %v", n, err)
		}
		if task.DailyLimit != n {
			t.Fatalf("daily_limit = %d, want %d", task.DailyLimit, n)
		}
	}
	// 0 → 默认 20。
	in := aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 20}
	task, err := normalizeAliasTask(in)
	if err != nil {
		t.Fatalf("daily_limit 缺省应取默认值: %v", err)
	}
	if task.DailyLimit != defaultAutoDailyLimit {
		t.Fatalf("默认 daily_limit = %d, want %d", task.DailyLimit, defaultAutoDailyLimit)
	}
	// 越界拒绝。
	for _, n := range []int{-1, 51} {
		in := aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 20, DailyLimit: n}
		if _, err := normalizeAliasTask(in); err == nil {
			t.Fatalf("daily_limit=%d 应被拒绝", n)
		}
	}
}

// ============ 自主任务: 首日按剩余时间折算今日配额 ============

func TestFirstDayQuotaProratesByRemainingTime(t *testing.T) {
	cases := []struct {
		hour, min int
		want      int
	}{
		{0, 0, 20},  // 零点开始 → 全天 20 个
		{8, 0, 13},  // 剩 16h/24h → round(13.33)
		{12, 0, 10}, // 中午 → 一半
		{18, 0, 5},  // 剩 6h → round(5)
		{23, 30, 0}, // 剩 30 分钟 → 不足 1 个
	}
	for _, tc := range cases {
		now := atHour(tc.hour, tc.min)
		if got := firstDayQuota(20, now); got != tc.want {
			t.Fatalf("firstDayQuota(20, %02d:%02d) = %d, want %d", tc.hour, tc.min, got, tc.want)
		}
	}
	if got := firstDayQuota(0, atHour(12, 0)); got != 0 {
		t.Fatalf("dailyLimit=0 应返回 0, got %d", got)
	}
}

func TestCreateTaskSnapshotsFirstDayQuota(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	noon := atHour(12, 0)
	m.now = func() time.Time { return noon }

	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if task.TodayQuota != 10 {
		t.Fatalf("中午创建的任务 TodayQuota = %d, want 10(20 的一半)", task.TodayQuota)
	}
	if task.DailyDate != taskDate(noon) {
		t.Fatalf("DailyDate = %q, want %q", task.DailyDate, taskDate(noon))
	}
}

func TestResetDailyCountRestoresFullQuotaNextDay(t *testing.T) {
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 10, DailyDate: "2026-01-01", TodayQuota: 10}
	next := time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local)
	got := resetDailyCount(task, next)
	if got.DailyCount != 0 || got.TodayQuota != 20 || got.DailyDate != "2026-01-02" {
		t.Fatalf("跨天后应恢复满额配额: %+v", got)
	}
	// 同一天不重置。
	same := resetDailyCount(got, time.Date(2026, 1, 2, 22, 0, 0, 0, time.Local))
	if same.TodayQuota != 20 || same.DailyCount != 0 {
		t.Fatalf("同一天不应重置: %+v", same)
	}
}

func TestRunOnceHonorsTodayQuota(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task.Enabled = true
	task.DailyCount = 10
	task.TodayQuota = 10 // 今天只该创建 10 个(中午启动)
	task.DailyDate = taskDate(time.Now())
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if len(be.labels) != 0 {
		t.Fatalf("今日配额已用尽时不应创建, got %v", be.labels)
	}
	if !out.Enabled {
		t.Fatalf("配额用尽是推迟而非暂停: %+v", out)
	}
	if out.NextRun == "" {
		t.Fatal("应排到次日")
	}
}

func TestNextAutoDelayUsesTodayQuota(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }

	// DailyLimit=20 但今日配额 10、已建 9:剩余 1 个,应在窗口内配速而非按 20 计。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 9, TodayQuota: 10}
	now := atHour(12, 0)
	delay := m.nextAutoDelay(task, now)
	if delay <= 0 || delay > 12*time.Hour {
		t.Fatalf("delay = %v, 应在窗口内", delay)
	}
	// 已建 10(达到今日配额):推迟到次日。
	exhausted := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 10, TodayQuota: 10}
	if got := m.nextAutoDelay(exhausted, now); got != nextDailyRun(now).Sub(now) {
		t.Fatalf("配额用尽应推迟到次日, got %v", got)
	}
}

// ============ 自主任务: 拟人化扰动与活跃度权重 ============

func TestActivityWeightCurveShape(t *testing.T) {
	if activityWeight(10) <= activityWeight(23) {
		t.Fatal("上午权重应高于深夜")
	}
	if activityWeight(12) >= activityWeight(10) {
		t.Fatal("午休权重应低于上午高峰")
	}
	if activityWeight(0) != 0 || activityWeight(7) != 0 {
		t.Fatal("窗口外(0-7 点)权重应为 0")
	}
	if avg := averageActivityWeight(); avg <= 0 {
		t.Fatalf("平均权重应为正, got %v", avg)
	}
}

func TestPaceDelayScalesByActivityWeight(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }
	_, end := autoActiveWindow(atHour(10, 0), 1)

	// 高活跃时段(14:00)间隔应小于均匀配速;深夜(23:00)权重最低,
	// 结果被钳制在窗口剩余时长内(不会再往外推)。
	uniform := end.Sub(atHour(14, 0)) / 4
	fast := m.paceDelay(atHour(14, 0), end, 4)
	if fast >= uniform {
		t.Fatalf("14:00 的间隔 %v 应小于均匀配速 %v", fast, uniform)
	}
	remaining := end.Sub(atHour(23, 0))
	slow := m.paceDelay(atHour(23, 0), end, 1)
	if slow > remaining {
		t.Fatalf("23:00 的间隔 %v 不应超过窗口剩余 %v", slow, remaining)
	}
	// 深夜权重(0.4)低于全天平均: 未钳制前应比均匀配速更长。
	if w := activityWeight(23); w >= averageActivityWeight() {
		t.Fatalf("23:00 权重 %v 应低于平均 %v", w, averageActivityWeight())
	}
}

func TestNextAutoDelayNeverBelowCooldownWithWeights(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{})
	m.jitter = func() float64 { return 1.0 }
	// 深夜(权重最低)配速会被压到 20 分钟以下,必须由冷却地板兜住。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 50, DailyCount: 0, TodayQuota: 50}
	delay := m.nextAutoDelay(task, atHour(23, 55))
	if delay < minCreationCooldown {
		t.Fatalf("delay %v 低于冷却下限 %v", delay, minCreationCooldown)
	}
}

func TestHumanJitterProducesOccasionalBreaks(t *testing.T) {
	lo, hi := 1-autoJitterSigma, 1+autoJitterSigma
	breaks := 0
	const n = 20000
	for i := 0; i < n; i++ {
		f := humanJitter()
		switch {
		case f >= lo-1e-9 && f <= hi+1e-9:
			// 常规扰动
		case f >= 2.0 && f < 4.0:
			breaks++
		default:
			t.Fatalf("jitter %v 落在常规与长暂停之外", f)
		}
	}
	ratio := float64(breaks) / float64(n)
	if ratio < 0.06 || ratio > 0.20 {
		t.Fatalf("长暂停比例 %.3f 偏离预期(≈%.2f)", ratio, autoBreakChance)
	}
}
