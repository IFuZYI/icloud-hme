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
	_, ts := newTestServer(t, f)
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
	_, ts := newTestServer(t, f)
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
	_, ts := newTestServer(t, f)
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
	// 固定时钟与画像: 配额按作息周期重置, 必须让测试的 DailyDate 与周期一致。
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	p := personaByName("standard")
	m.mu.Lock()
	task.Enabled = true
	task.Persona = "standard"
	task.DailyCount = 10
	task.TodayQuota = 10 // 今天只该创建 10 个(中午启动)
	task.DailyDate = p.cycleDateAt(now.In(time.Local))
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
	m.rand = func() float64 { return 0.5 }

	// DailyLimit=20 但今日配额 10、已建 9: 剩余 1 个, 应给出正间隔而非按 20 计。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 9, TodayQuota: 10, Persona: "standard"}
	now := testWeekday(12, 0)
	delay := m.nextAutoDelay(task, now)
	if delay <= 0 || delay > 12*time.Hour {
		t.Fatalf("delay = %v, 应在窗口内", delay)
	}
	// 已建 10(达到今日配额): 必须推迟到下一作息段(次日睡醒), 而不是按
	// DailyLimit=20 继续排——若实现忽略 TodayQuota, 落点会留在当天。
	// 反变异: nextAutoDelay 用 DailyLimit-DailyCount 时此断言失败。
	exhausted := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 10, TodayQuota: 10, Persona: "standard"}
	got := m.nextAutoDelay(exhausted, now)
	if got <= 16*time.Hour {
		t.Fatalf("配额用尽的间隔 %v 应推到次日睡醒(约 17h)", got)
	}
}

// ============ 自主任务: 拟人模型（画像 / 会话自激 / 滚动门控） ============

func TestPersonaCurveShape(t *testing.T) {
	p := personaByName("standard")
	if p.weightAt(testWeekday(10, 0)) <= p.weightAt(testWeekday(22, 0)) {
		t.Fatal("上午权重应高于深夜")
	}
	if p.weightAt(testWeekday(12, 0)) >= p.weightAt(testWeekday(10, 0)) {
		t.Fatal("午休权重应低于上午高峰")
	}
	if p.weightAt(testWeekday(3, 0)) != 0 {
		t.Fatal("睡眠时段(03:00)权重应为 0")
	}
	if p.avgWeekday <= 0 {
		t.Fatalf("平均权重应为正, got %v", p.avgWeekday)
	}
}
