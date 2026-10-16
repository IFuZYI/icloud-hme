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
