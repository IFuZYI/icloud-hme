package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

// ====================================================================
// 失败容错: 单次失败不再暂停; 连续 2 次失败或最近 5 次中 >2 次失败才暂停
// ====================================================================
//
// 背景: 原行为「任意一次创建失败立即暂停」会把上游偶发误报(超时/限流提示)
// 当作确定性失败, 任务被无谓停掉, 须用户手动恢复。新行为给失败留出容错
// 预算: 只有「连续 2 次失败」或「最近 5 次结果中失败 ≥3 次」才暂停; 单次
// 失败保持启用, 由调度器按既有间隔自动重试。
//
// 本地持久化失败(状态保存/预留失败)不属于上游误报, 仍立即暂停(见既有
// 测试 TestAutoTaskRunOnceReservationFailureSkipsBackendAndPauses 等)。

// newToleranceTask 构造一个启用的定时任务并注入管理器, 配合固定虚拟时钟,
// 隔离作息/配额逻辑对失败容错断言的干扰。
func newToleranceTask(t *testing.T, m *autoTaskManager, now time.Time) AliasTask {
	t.Helper()
	task := AliasTask{ID: "task_tolerance", Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, DailyLimit: maxTaskDailyLimit, MaxTotal: 10, NextNumber: 1,
		DailyDate: taskDate(now)}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	return task
}

// scriptedBackend 按调用序号返回预设的上游结果: failures[i] 为第 i 次调用的
// 错误(nil 表示成功); 超出脚本长度后一律成功。用于构造确定性的成败序列。
type scriptedBackend struct {
	taskBackend
	failures []error
}

func (s *scriptedBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	s.mu.Lock()
	idx := len(s.labels)
	s.labels = append(s.labels, label)
	var err error
	if idx < len(s.failures) {
		err = s.failures[idx]
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &hme.CreateResult{Email: label}, nil
}

// 单次创建失败: 保持启用、安排重试、记录失败原因。
func TestSingleCreationFailureKeepsTaskEnabled(t *testing.T) {
	be := &taskBackend{createErr: errors.New("upstream timeout")}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	out := m.runOnce(task.ID)
	if len(be.labels) != 1 {
		t.Fatalf("应发起一次上游调用: %v", be.labels)
	}
	if !out.Enabled {
		t.Fatalf("单次失败应容错(保持启用): %+v", out)
	}
	if out.NextRun == "" {
		t.Fatalf("容错后应安排重试: %+v", out)
	}
	if !strings.Contains(out.LastError, "upstream timeout") {
		t.Fatalf("失败原因应记录: %q", out.LastError)
	}
}

// 连续第 2 次失败: 暂停并清空 next_run, 同时写入暂停日志。
func TestTwoConsecutiveFailuresPauseTask(t *testing.T) {
	be := &taskBackend{createErr: errors.New("boom")}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	out1 := m.runOnce(task.ID)
	if !out1.Enabled {
		t.Fatalf("第 1 次失败应容错: %+v", out1)
	}
	// 步进 61 分钟: 同时越过最短创建间隔与滚动小时窗口, 使第 2 次尝试必然执行。
	now = now.Add(61 * time.Minute)
	out2 := m.runOnce(task.ID)
	if len(be.labels) != 2 {
		t.Fatalf("应发起两次上游调用: %v", be.labels)
	}
	if out2.Enabled {
		t.Fatalf("连续第 2 次失败应暂停: %+v", out2)
	}
	if out2.NextRun != "" {
		t.Fatalf("暂停应清空 NextRun: %q", out2.NextRun)
	}
	if !strings.Contains(out2.LastError, "boom") || !strings.Contains(out2.LastError, "暂停") {
		t.Fatalf("暂停原因应记录: %q", out2.LastError)
	}
	pausedLogged := false
	for _, entry := range m.listLogs() {
		if strings.Contains(entry.Message, "任务已暂停") {
			pausedLogged = true
		}
	}
	if !pausedLogged {
		t.Fatalf("暂停应写入日志: %#v", m.listLogs())
	}
}

// 成功清零连续失败计数: 中断「连续」判定。序列 fail/ok/fail/ok/ok 中
// 连续失败从未达到 2、窗口内失败仅 2 次 → 始终启用。
// (对照: 若成功不清零连续计数, 第 3 次(第 2 个 fail)就会被误判为「连续 2 次」而暂停。)
func TestSuccessResetsConsecutiveFailureStreak(t *testing.T) {
	be := &scriptedBackend{failures: []error{
		errors.New("flaky upstream"), nil, errors.New("flaky upstream"), nil, nil,
	}}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	for i := 0; i < 5; i++ {
		out := m.runOnce(task.ID)
		if !out.Enabled {
			t.Fatalf("成功已打断连续失败, 不应暂停(第 %d 次): %+v", i+1, out)
		}
		now = now.Add(61 * time.Minute)
	}
}

// 窗口规则: 最近 5 次中失败 ≥3 次即暂停, 即使没有连续 2 次失败。
// 序列 fail/ok/fail/ok/fail → 窗口内失败 3 次 → 第 5 次后暂停。
func TestWindowFailureRulePausesOnThirdFailureInFive(t *testing.T) {
	be := &scriptedBackend{failures: []error{
		errors.New("e1"), nil, errors.New("e2"), nil, errors.New("e3"),
	}}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	for i := 0; i < 4; i++ {
		out := m.runOnce(task.ID)
		if !out.Enabled {
			t.Fatalf("前 4 次(窗口内失败 2 次)不应暂停(第 %d 次): %+v", i+1, out)
		}
		now = now.Add(61 * time.Minute)
	}
	out := m.runOnce(task.ID)
	if out.Enabled {
		t.Fatalf("窗口内第 3 次失败应暂停: %+v", out)
	}
	if len(be.labels) != 5 {
		t.Fatalf("应发起 5 次上游调用: %v", be.labels)
	}
}

// 窗口规则边界: 连续失败 2 次后成功, 连续计数清零; 窗口内失败不够 3 次则不暂停。
// 序列 fail/fail(暂停) 的对照: fail/ok/ok/fail/ok → 窗口内失败 2 → 保持启用。
func TestWindowCountsFailsNotStreakWhenStreakBroken(t *testing.T) {
	be := &scriptedBackend{failures: []error{
		errors.New("e1"), nil, nil, errors.New("e2"), nil,
	}}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	for i := 0; i < 5; i++ {
		out := m.runOnce(task.ID)
		if !out.Enabled {
			t.Fatalf("窗口内失败未超阈值不应暂停(第 %d 次): %+v", i+1, out)
		}
		now = now.Add(61 * time.Minute)
	}
}

// 失败历史跨重启持久化: 重启不得重置容错预算, 否则「连续失败」永远无法累计。
// 单次失败 → 重启 → 再失败一次, 应判定为连续 2 次并暂停。
func TestFailureHistorySurvivesRestart(t *testing.T) {
	file := t.TempDir() + "/task.json"
	be := &taskBackend{createErr: errors.New("boom")}
	m := newAutoTaskManager(file, be)
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	out1 := m.runOnce(task.ID)
	if !out1.Enabled {
		t.Fatalf("第 1 次失败应容错: %+v", out1)
	}
	m.close()

	// 重启: 从同一文件加载, 失败历史必须保留。
	m2 := newAutoTaskManager(file, be)
	defer m2.close()
	m2.now = func() time.Time { return now.Add(61 * time.Minute) }
	got, ok := m2.get(task.ID)
	if !ok {
		t.Fatal("任务应在重载后存在")
	}
	if len(got.RecentOutcomes) != 1 || got.RecentOutcomes[0] != "fail" {
		t.Fatalf("失败历史应跨重启保留: %#v", got.RecentOutcomes)
	}
	out2 := m2.runOnce(task.ID)
	if out2.Enabled {
		t.Fatalf("重启后连续第 2 次失败应暂停: %+v", out2)
	}
}

// 用户手动启用任务: 清空失败历史, 给用户显式恢复后的任务一个干净的容错预算。
func TestToggleEnableClearsFailureHistory(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{createErr: errors.New("boom")})
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	// 连败到暂停。
	m.runOnce(task.ID)
	now = now.Add(61 * time.Minute)
	if out := m.runOnce(task.ID); out.Enabled {
		t.Fatalf("连续 2 次失败应暂停: %+v", out)
	}
	// 用户手动启用: 历史清零, 下一次失败应重新按「单次」容错。
	enabled, err := m.toggle(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled {
		t.Fatalf("toggle 应重新启用任务: %+v", enabled)
	}
	if len(enabled.RecentOutcomes) != 0 {
		t.Fatalf("手动启用应清空失败历史: %#v", enabled.RecentOutcomes)
	}
	// 注: 实现从 nil 改为 t.RecentOutcomes[:0] 属等价变异(长度同为 0), 本测试
	// 用 len() 断言无法也无需区分——两者的可观察行为一致。真正的回归(启用时
	// 完全不动历史, 旧记录残留)会被上面的断言捕获。
	now = now.Add(61 * time.Minute)
	out := m.runOnce(task.ID)
	if !out.Enabled {
		t.Fatalf("清空历史后单次失败应容错: %+v", out)
	}
}

// 阈值边界(表驱动): 用户规则为「近 5 次失败超过 2 次才停」「连续 2 次失败才停」。
// 逐点覆盖 阈值-1 / 恰好阈值 / 阈值+1, 防止 `>` 被写成 `>=` 之类的 off-by-one。
func TestShouldPauseAfterFailureBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		outcomes []string
		want     bool
	}{
		// 连续失败边界: 1 次不停, 2 次停。
		{"连续 1 次失败→不停", []string{"fail"}, false},
		{"连续 2 次失败→停", []string{"fail", "fail"}, true},
		{"连续 3 次失败→停", []string{"fail", "fail", "fail"}, true},
		// 窗口失败边界(无连续): 窗口内 2 次失败不停, 3 次停。
		{"窗口内 2 次失败(无连续)→不停", []string{"fail", "ok", "fail"}, false},
		{"窗口内恰好 3 次失败→停", []string{"fail", "ok", "fail", "ok", "fail"}, true},
		{"窗口内 4 次失败→停", []string{"fail", "ok", "fail", "fail", "fail"}, true},
		// 成功打断连续后, 仅按窗口计数。
		{"成功打断连续(1 次失败)→不停", []string{"fail", "ok"}, false},
		// 边界: 窗口容量 5, 第 6 个结果挤出最早的失败后不再触发窗口规则。
		{"失败滑出窗口后不再触发", []string{"fail", "ok", "ok", "ok", "ok", "ok"}, false},
		{"全成功→不停", []string{"ok", "ok", "ok", "ok", "ok"}, false},
		{"空历史→不停", nil, false},
	}
	for _, tc := range cases {
		if got := shouldPauseAfterFailure(tc.outcomes); got != tc.want {
			t.Errorf("%s: shouldPauseAfterFailure(%v) = %v, want %v", tc.name, tc.outcomes, got, tc.want)
		}
	}
}

// recordTaskOutcome 必须把历史裁剪到窗口大小(最近 5 条, 用户规格的窗口宽度)。
//
// 断言用字面量 5 而非 taskOutcomeWindow 常量: 否则把常量改成 4, 断言跟着变,
// 测试无法发现窗口宽度被改坏(变异 M12 实测会存活)。
func TestRecordTaskOutcomeTrimsToWindow(t *testing.T) {
	task := AliasTask{}
	for i := 0; i < 8; i++ {
		task.RecentOutcomes = task.recordTaskOutcome(i%2 == 0)
	}
	if len(task.RecentOutcomes) != 5 {
		t.Fatalf("历史应裁剪到 5 条, got %d: %#v", len(task.RecentOutcomes), task.RecentOutcomes)
	}
	if taskOutcomeWindow != 5 {
		t.Fatalf("容错窗口宽度应为 5, got %d", taskOutcomeWindow)
	}
	// 最近一条对应最后一次调用(i=7 → 失败)。
	if last := task.RecentOutcomes[len(task.RecentOutcomes)-1]; last != "fail" {
		t.Fatalf("最近结果应为 fail, got %q", last)
	}
}

// 上游失败本身的容错不能放过「审计日志写盘失败」——后者是本地持久化问题,
// 与原行为一致必须暂停(数据完整性优先于容错)。场景: 单次上游失败(在容错
// 预算内, 本应保持启用) + 失败日志写入失败 → 仍必须暂停。
func TestLogPersistenceFailurePausesEvenWhenFailureTolerated(t *testing.T) {
	file := t.TempDir() + "/task.json"
	m := newAutoTaskManager(file, &taskBackend{createErr: errors.New("upstream timeout")})
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)
	// 审计日志写盘失败(状态写盘正常)。
	m.writeLogs = func(string, any) error { return errors.New("injected log failure") }

	out := m.runOnce(task.ID)
	if out.Enabled {
		t.Fatalf("日志写盘失败必须暂停(即使上游失败在容错预算内): %+v", out)
	}
	if out.NextRun != "" {
		t.Fatalf("暂停应清空 NextRun: %q", out.NextRun)
	}
	if !strings.Contains(out.LastError, "日志保存失败") {
		t.Fatalf("应记录日志保存失败: %q", out.LastError)
	}
	if persisted := readPersistedTask(t, file, task.ID); persisted.Enabled {
		t.Fatalf("暂停必须落盘: %+v", persisted)
	}
}

// 编辑保存任务(update): 任务会自动重新启用, 因此同样应清空失败历史。
//
// 变异说明: 删除 update 里的白名单式 `n.RecentOutcomes = nil` 后本测试仍
// 通过——因为 normalizeAliasTaskAt 新建的 n 本就不含历史(该行是防御性冗余,
// 与 n.CreatedAt = old.CreatedAt 这类"保留旧字段"的写法相反)。本测试钉住的
// 契约是: **update 的输出不得携带旧任务的失败历史**, 无论实现是靠新建还是
// 靠显式清空。若将来改为保留旧字段, 本测试即失败(已用携带历史的变异验证)。
func TestUpdateClearsFailureHistory(t *testing.T) {
	m := newAutoTaskManager(t.TempDir()+"/task.json", &taskBackend{createErr: errors.New("boom")})
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	m.runOnce(task.ID)
	now = now.Add(61 * time.Minute)
	if out := m.runOnce(task.ID); out.Enabled {
		t.Fatalf("连续 2 次失败应暂停: %+v", out)
	}

	updated, err := m.update(task.ID, aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 60, BatchCount: 1, TargetCount: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RecentOutcomes) != 0 {
		t.Fatalf("编辑保存应清空失败历史: %#v", updated.RecentOutcomes)
	}
	if !updated.Enabled {
		t.Fatalf("编辑保存后任务应启用: %+v", updated)
	}
}

// 暂停日志文案必须准确反映触发原因: 窗口规则(近 5 次中 3 次失败)触发时,
// 日志不得误写为「连续 N 次失败」——那会让排查者按错误方向去找。
func TestPauseLogTextMatchesWindowRuleTrigger(t *testing.T) {
	be := &scriptedBackend{failures: []error{
		errors.New("e1"), nil, errors.New("e2"), nil, errors.New("e3"),
	}}
	m := newAutoTaskManager(t.TempDir()+"/task.json", be)
	defer m.close()
	now := testWeekday(10, 0)
	m.now = func() time.Time { return now }
	task := newToleranceTask(t, m, now)

	for i := 0; i < 4; i++ {
		m.runOnce(task.ID)
		now = now.Add(61 * time.Minute)
	}
	out := m.runOnce(task.ID) // 第 5 次: 窗口内失败 3 次 → 暂停
	if out.Enabled {
		t.Fatalf("窗口内第 3 次失败应暂停: %+v", out)
	}
	var pauseLog string
	for _, entry := range m.listLogs() {
		if strings.Contains(entry.Message, "任务已暂停") {
			pauseLog = entry.Message
		}
	}
	if pauseLog == "" {
		t.Fatalf("暂停应写入日志: %#v", m.listLogs())
	}
	// 窗口规则触发时不得写成具体的「连续 N 次失败」(误导为连续失败导致),
	// 必须点明「累计」这一真实触发维度。
	if strings.Contains(pauseLog, "连续 2 次失败") || strings.Contains(pauseLog, "连续 2次失败") {
		t.Fatalf("窗口规则触发的暂停日志不应写具体的「连续 2 次失败」(误导): %q", pauseLog)
	}
	if !strings.Contains(pauseLog, "累计") {
		t.Fatalf("暂停日志应说明累计失败触发: %q", pauseLog)
	}
}
