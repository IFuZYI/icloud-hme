package server

import (
	"strings"
	"testing"
	"time"
)

// ====================================================================
// 邮件清理任务管理器 (purge manager)
// ====================================================================
//
// 契约:
//   - 一次清理 = 一条 PurgeTask(无论多少封); 状态机:
//     running → done | paused | cancelled | throttled | interrupted;
//   - 按账号并发: 同账号同时最多 1 个任务(第二个提交报错);
//     不同账号可并行;
//   - 持久化: data/purge_tasks.json(原子写), 保留最近 50 条;
//     重启后 running/throttled → interrupted(可 resume)。

// purgeTestClock 可注入时钟。
type purgeTestClock struct {
	now time.Time
}

func (c *purgeTestClock) Now() time.Time { return c.now }

func (c *purgeTestClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// newPurgeTestManager 造一个纯内存管理器(注入空时钟与 sleep)。
func newPurgeTestManager(t *testing.T) (*purgeManager, *purgeTestClock) {
	t.Helper()
	m, err := newPurgeManager("", nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := &purgeTestClock{now: time.Now()}
	m.now = clock.Now
	m.sleep = func(time.Duration) {} // 测试不真睡
	return m, clock
}

// 任务创建后处于 running; ID 有 purge_ 前缀。
func TestPurgeCreateTask(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	tk, err := m.create("acc_1", "账号一", time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.ID == "" || !strings.HasPrefix(tk.ID, "purge_") {
		t.Fatalf("ID = %q, want purge_ 前缀", tk.ID)
	}
	if tk.AccountID != "acc_1" || tk.Status != "running" {
		t.Fatalf("任务字段异常: %+v", tk)
	}
}

// 同账号第二个任务被拒; 不同账号允许(按账号并发)。
func TestPurgeSameAccountExclusive(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	if _, err := m.create("acc_1", "一", time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("acc_1", "一", time.Now(), time.Now()); err == nil {
		t.Fatal("同账号第二个任务应被拒")
	}
	if _, err := m.create("acc_2", "二", time.Now(), time.Now()); err != nil {
		t.Fatalf("不同账号应可并行: %v", err)
	}
}

// 任务完成后账号锁释放, 可再建新任务。
func TestPurgeAccountUnlockedAfterFinish(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	tk, _ := m.create("acc_1", "一", time.Now(), time.Now())
	m.finish(tk.ID, "done", "")
	if _, err := m.create("acc_1", "一", time.Now(), time.Now()); err != nil {
		t.Fatalf("完成后应可再建: %v", err)
	}
}

// 取消: 状态 cancelled 且释放锁。
func TestPurgeCancel(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	tk, _ := m.create("acc_1", "一", time.Now(), time.Now())
	if err := m.cancel(tk.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, ok := m.get(tk.ID)
	if !ok || got.Status != "cancelled" {
		t.Fatalf("取消后状态 = %q", got.Status)
	}
	if _, err := m.create("acc_1", "一", time.Now(), time.Now()); err != nil {
		t.Fatalf("取消后应可再建: %v", err)
	}
}

// 暂停 → 继续。
func TestPurgePauseResume(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	tk, _ := m.create("acc_1", "一", time.Now(), time.Now())
	if err := m.pause(tk.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.get(tk.ID)
	if got.Status != "paused" {
		t.Fatalf("暂停后状态 = %q", got.Status)
	}
	if err := m.resume(tk.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ = m.get(tk.ID)
	if got.Status != "running" {
		t.Fatalf("继续后状态 = %q", got.Status)
	}
}

// 持久化: 重新加载后任务仍在; running 变 interrupted。
func TestPurgePersistenceAndInterrupt(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/purge_tasks.json"
	m1, err := newPurgeManager(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := m1.create("acc_1", "一", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 模拟运行中重启。
	m2, err := newPurgeManager(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m2.get(tk.ID)
	if !ok {
		t.Fatal("重新加载后任务应存在")
	}
	if got.Status != "interrupted" {
		t.Fatalf("重启后 running 应变 interrupted, got %q", got.Status)
	}
	// interrupted 可 resume。
	if err := m2.resume(tk.ID); err != nil {
		t.Fatalf("resume interrupted: %v", err)
	}
	got, _ = m2.get(tk.ID)
	if got.Status != "running" {
		t.Fatalf("resume 后 = %q", got.Status)
	}
}

// 列表新→旧, 保留上限 50(裁剪最旧的非活跃任务)。
func TestPurgeListOrderAndTrim(t *testing.T) {
	m, clock := newPurgeTestManager(t)
	var ids []string
	for i := 0; i < 55; i++ {
		clock.Advance(time.Second)
		tk, err := m.create("acc_"+string(rune('A'+i%26)), "n", time.Now(), time.Now())
		if err != nil {
			// 同账号互斥可能触发; 换个账号名重试。
			tk, err = m.create("acc_"+string(rune('a'+i%26)), "n", time.Now(), time.Now())
			if err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		}
		m.finish(tk.ID, "done", "")
		ids = append(ids, tk.ID)
	}
	list := m.list()
	if len(list) > purgeKeepTasks {
		t.Fatalf("列表 = %d, 应 ≤ %d", len(list), purgeKeepTasks)
	}
	// 最新的一定在。
	newest := ids[len(ids)-1]
	found := false
	for _, tk := range list {
		if tk.ID == newest {
			found = true
		}
	}
	if !found {
		t.Fatal("最新任务应在列表里")
	}
}

// 裁剪: 活跃任务无条件保留且不占额度; 终态任务只保留最新 purgeKeepTasks 条。
func TestPurgeTrimKeepsActiveTask(t *testing.T) {
	m, clock := newPurgeTestManager(t)
	old := purgeKeepTasks
	purgeKeepTasks = 3
	t.Cleanup(func() { purgeKeepTasks = old })

	// 1 个活跃任务(未完成, 占用账号锁)。
	active, err := m.create("acc_active", "活跃", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 5 个终态任务(依次完成)。
	var finished []string
	for i := 0; i < 5; i++ {
		clock.Advance(time.Second)
		tk, err := m.create("acc_f"+string(rune('0'+i)), "终态", time.Now(), time.Now())
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		m.finish(tk.ID, "done", "")
		finished = append(finished, tk.ID)
	}

	// 活跃任务必须仍在(此前会被按创建顺序裁掉)。
	got, ok := m.get(active.ID)
	if !ok || got.Status != "running" {
		t.Fatalf("活跃任务被裁: ok=%v got=%+v", ok, got)
	}
	if m.active["acc_active"] != active.ID {
		t.Fatalf("active 映射不一致: %v", m.active)
	}
	list := m.list()
	if len(list) != 4 { // 1 活跃 + 3 终态
		t.Fatalf("列表 = %d, want 4(1 活跃 + 3 终态)", len(list))
	}
	kept := map[string]bool{}
	for _, tk := range list {
		kept[tk.ID] = true
	}
	if !kept[active.ID] {
		t.Fatal("活跃任务应在列表里")
	}
	for i, id := range finished {
		if i < 2 && kept[id] {
			t.Fatalf("最旧的终态任务应被裁: %s", id)
		}
		if i >= 2 && !kept[id] {
			t.Fatalf("最新终态任务应保留: %s", id)
		}
	}
}

// 同账号互斥: 账号已被其它活跃任务占用时, resume 旧任务必须被拒
// (否则同账号会并发两个 runner, IMAP 命令数翻倍)。
func TestPurgeResumeRejectsWhenAccountBusy(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	// 活跃任务占用 acc_1(持锁)。
	busy, err := m.create("acc_1", "一", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 造一个同账号的「旧」暂停任务(模拟重启前遗留: 任务表里有, 锁被别人占)。
	stale, err := m.create("acc_2", "二", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pause(stale.ID); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.tasks[stale.ID].AccountID = "acc_1"
	delete(m.active, "acc_2")
	m.mu.Unlock()

	err = m.resume(stale.ID)
	if err == nil {
		t.Fatal("账号已被其它任务占用时 resume 应失败")
	}
	if !strings.Contains(err.Error(), "已有清理任务") {
		t.Fatalf("错误应含「已有清理任务」: %v", err)
	}
	// 状态不得被改成 running; 锁仍属于 busy。
	got, _ := m.get(stale.ID)
	if got.Status != "paused" {
		t.Fatalf("resume 被拒后状态应保持 paused, got %q", got.Status)
	}
	m.mu.Lock()
	owner := m.active["acc_1"]
	m.mu.Unlock()
	if owner != busy.ID {
		t.Fatalf("账号锁被误改: owner = %q, want %q", owner, busy.ID)
	}
}

// finish 只释放属于自己的账号锁: 双 runner 场景下先完成者不得放掉他人锁。
func TestPurgeFinishReleasesOnlyOwnLock(t *testing.T) {
	m, _ := newPurgeTestManager(t)
	first, err := m.create("acc_1", "一", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 模拟重启后 resume 的旧任务接管了账号锁。
	second, err := m.create("acc_2", "二", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.update(second.ID, func(t *PurgeTask) { t.AccountID = "acc_1" }); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.active["acc_1"] = second.ID
	m.mu.Unlock()

	m.finish(first.ID, "done", "")
	m.mu.Lock()
	owner := m.active["acc_1"]
	m.mu.Unlock()
	if owner != second.ID {
		t.Fatalf("先完成者不应放掉他人锁: owner = %q, want %q", owner, second.ID)
	}
}

// 区间校验: 非法格式 / end==start / end<start 都报错; 合法通过。
func TestParsePurgeRangeValidation(t *testing.T) {
	valid := "2026-01-01T00:00:00Z"
	cases := []struct {
		name    string
		start   string
		end     string
		wantErr bool
	}{
		{"start 非法格式", "not-a-time", valid, true},
		{"end 非法格式", valid, "2026/01/02", true},
		{"start 为空", "", valid, true},
		{"end 为空", valid, "", true},
		{"end 等于 start", valid, valid, true},
		{"end 早于 start", "2026-01-02T00:00:00Z", valid, true},
		{"合法", "2025-12-01T00:00:00Z", "2026-01-01T00:00:00Z", false},
		{"首尾空白合法", " 2025-12-01T00:00:00Z ", "2026-01-01T00:00:00Z ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, msg := parsePurgeRange(tc.start, tc.end)
			if tc.wantErr && msg == "" {
				t.Fatalf("应返回错误: start=%q end=%q", tc.start, tc.end)
			}
			if !tc.wantErr && msg != "" {
				t.Fatalf("应通过: start=%q end=%q got=%q", tc.start, tc.end, msg)
			}
		})
	}
}
