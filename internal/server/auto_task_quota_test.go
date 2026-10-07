package server

import (
	"os"
	"testing"
	"time"
)

// ============ 今日配额 0 的语义 ============
//
// 回归背景: firstDayQuota 按剩余时间折算可能得到 0(深夜创建, 或小目标
// 折算不足 1 个), 约定「今日不排」。但 0 曾被当作「未设置」处理:
//   - effectiveDailyQuota 的 `TodayQuota > 0` 判断回退到满额;
//   - json 的 omitempty 让 0 不落盘, 重启加载后又回填为满额。
// 结果: 深夜创建的任务当天照样创建、重启后配额丢失。

func TestEffectiveDailyQuotaTreatsZeroAsValid(t *testing.T) {
	// 自主任务: TodayQuota=0 是合法值, 表示今日不排, 不得回退到 DailyLimit。
	task := AliasTask{Mode: taskModeAuto, DailyLimit: 20, DailyCount: 0, TodayQuota: 0}
	if got := task.effectiveDailyQuota(); got != 0 {
		t.Fatalf("effectiveDailyQuota() = %d, want 0(今日不排)", got)
	}
	// 定时任务没有该字段, 仍用 DailyLimit。
	scheduled := AliasTask{Mode: taskModeScheduled, DailyLimit: 50}
	if got := scheduled.effectiveDailyQuota(); got != 50 {
		t.Fatalf("定时任务应回退 DailyLimit, got %d", got)
	}
}

func TestRunOnceWithZeroQuotaSkipsToday(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	// 固定时钟与画像: 配额按作息周期重置, 测试的 DailyDate 必须与周期一致。
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	p := personaByName("standard")
	m.mu.Lock()
	task.Persona = "standard"
	task.TodayQuota = 0 // 深夜创建: 今日不排
	task.DailyDate = p.cycleDateAt(now.In(time.Local))
	m.tasks[task.ID] = task
	m.mu.Unlock()

	out := m.runOnce(task.ID)
	if len(be.labels) != 0 {
		t.Fatalf("今日配额为 0 时不应创建, got %v", be.labels)
	}
	if !out.Enabled || out.NextRun == "" {
		t.Fatalf("配额为 0 应推迟到次日而非暂停: %+v", out)
	}
}

func TestLateNightZeroQuotaSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	be := &taskBackend{}
	m := newAutoTaskManager(dir+"/task.json", be)
	m.now = func() time.Time { return atHour(23, 30) }
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if task.TodayQuota != 0 {
		t.Fatalf("23:30 创建 TodayQuota = %d, want 0", task.TodayQuota)
	}

	// 重启: 重新加载同一文件, 0 配额必须保留(不能回填为满额)。
	m2 := newAutoTaskManager(dir+"/task.json", be)
	m2.now = func() time.Time { return atHour(23, 30) }
	got, ok := m2.get(task.ID)
	if !ok {
		t.Fatal("任务应在重载后存在")
	}
	if got.TodayQuota != 0 {
		t.Fatalf("重载后 TodayQuota = %d, want 0(omitempty 丢失 0 是回归点)", got.TodayQuota)
	}
	out := m2.runOnce(task.ID)
	if len(be.labels) != 0 {
		t.Fatalf("重载后也不应创建, got %v", be.labels)
	}
	if !out.Enabled || out.NextRun == "" {
		t.Fatalf("应推迟到次日: %+v", out)
	}
}

func TestLoadBackfillsQuotaOnlyForLegacyTasks(t *testing.T) {
	dir := t.TempDir()
	today := taskDate(time.Now())

	// 旧格式: 没有 today_quota 键(本字段引入前保存的任务) → 回填满额。
	legacy := `{"tasks":[{"id":"task_legacy","enabled":true,"account_id":"a","mode":"auto",` +
		`"daily_limit":20,"max_total":100,"next_number":1,"created_count":0,` +
		`"daily_count":0,"daily_date":"` + today + `","last_success":0}]}`
	if err := os.WriteFile(dir+"/task.json", []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAutoTaskManager(dir+"/task.json", &taskBackend{})
	got, ok := m.get("task_legacy")
	if !ok {
		t.Fatal("旧任务应被加载")
	}
	if got.TodayQuota != 20 {
		t.Fatalf("旧任务应回填满额配额, got %d", got.TodayQuota)
	}

	// 新格式: 显式写入 today_quota=0 必须原样保留。
	modern := `{"tasks":[{"id":"task_modern","enabled":true,"account_id":"a","mode":"auto",` +
		`"daily_limit":20,"today_quota":0,"max_total":100,"next_number":1,"created_count":0,` +
		`"daily_count":0,"daily_date":"` + today + `","last_success":0}]}`
	if err := os.WriteFile(dir+"/task.json", []byte(modern), 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := newAutoTaskManager(dir+"/task.json", &taskBackend{})
	got2, ok := m2.get("task_modern")
	if !ok {
		t.Fatal("任务应被加载")
	}
	if got2.TodayQuota != 0 {
		t.Fatalf("显式 0 配额应保留, got %d", got2.TodayQuota)
	}
}

func TestUpdatePreservesSameDayQuota(t *testing.T) {
	be := &taskBackend{}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	m.now = func() time.Time { return atHour(12, 0) }
	task, err := m.create(aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if task.TodayQuota != 10 {
		t.Fatalf("中午创建配额应为 10, got %d", task.TodayQuota)
	}

	// 深夜(23:50)编辑: 不得把当日已定配额重算为 0, 也不得回退满额。
	m.now = func() time.Time { return atHour(23, 50) }
	updated, err := m.update(task.ID, aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeAuto, TargetCount: 100, DailyLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if updated.TodayQuota != 10 {
		t.Fatalf("同日编辑应保留配额 10, got %d", updated.TodayQuota)
	}
}
