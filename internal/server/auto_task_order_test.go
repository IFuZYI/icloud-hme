package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// taskIDs 提取任务列表的 ID 序列, 便于断言展示顺序。
func taskIDs(tasks []AliasTask) []string {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	return ids
}

// 列表顺序必须跨调用稳定。
//
// 回归背景: taskListLocked 直接遍历 Go map, 而 map 迭代顺序每次调用都随机
// —— 前端每 2 秒轮询一次, 每次拿到的数组顺序都不同, 卡片就会「闪现换位」
// (用户实测反馈)。修复要求: 无论内部存储如何, list() 多次调用必须返回同一顺序。
func TestListStableOrderAcrossCalls(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()

	m.mu.Lock()
	for _, id := range []string{"task_e", "task_c", "task_a", "task_f", "task_b", "task_d"} {
		m.tasks[id] = AliasTask{ID: id, AccountID: "a", Mode: taskModeScheduled,
			IntervalMinutes: 60, BatchCount: 1, DailyLimit: maxTaskDailyLimit, MaxTotal: 10, NextNumber: 1}
	}
	m.mu.Unlock()

	first := taskIDs(m.list())
	for i := 1; i <= 50; i++ {
		got := taskIDs(m.list())
		if !slices.Equal(got, first) {
			t.Fatalf("第 %d 次 list() 顺序发生变化(闪现换位根因: map 迭代顺序随机): %v != %v", i, got, first)
		}
	}
}

// 列表按创建时间展示: 后创建的任务排在末尾; created_at 跨重启持久化,
// 重启后展示顺序不得变化(否则每次重启都是新一轮「换位」)。
func TestListOrdersByCreationTimeAndSurvivesReload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()

	base := testWeekday(10, 0)
	created := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		m.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeScheduled,
			IntervalMinutes: 60, BatchCount: 1, TargetCount: 10})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, task.ID)
	}
	// 新任务必须追加在末尾, 已有任务位置不变。
	if got := taskIDs(m.list()); !slices.Equal(got, created) {
		t.Fatalf("新任务应追加在末尾: got %v, want %v", got, created)
	}

	// 模拟重启: 从同一文件重新加载, 展示顺序必须保持。
	reloaded := newAutoTaskManager(file, &taskBackend{})
	defer reloaded.close()
	if got := taskIDs(reloaded.list()); !slices.Equal(got, created) {
		t.Fatalf("重启后顺序必须保持: got %v, want %v", got, created)
	}
}

// 旧数据(created_at 字段引入前保存)按文件中的数组位置补写创建序,
// 首次 list() 之后写入落盘, 后续读取稳定。
//
// 关键: 文件数组顺序刻意与 ID 字典序不同(z, x, y)——若补写实现用错来源
// (如退化为按 ID 排序), 本测试必须失败。展示顺序 = 文件数组顺序, 且
// 补写后再次重启(此时 created_at 已在文件中)顺序不变。
func TestLegacyTasksWithoutCreatedAtGetStableOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	legacy := `{"tasks":[` +
		`{"id":"task_z","account_id":"a","mode":"scheduled","interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,"next_number":1},` +
		`{"id":"task_x","account_id":"a","mode":"scheduled","interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,"next_number":1},` +
		`{"id":"task_y","account_id":"a","mode":"scheduled","interval_minutes":60,"batch_count":1,"daily_limit":50,"max_total":10,"next_number":1}` +
		`]}`
	if err := os.WriteFile(file, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded := newAutoTaskManager(file, &taskBackend{})
	defer reloaded.close()
	want := []string{"task_z", "task_x", "task_y"}
	first := taskIDs(reloaded.list())
	if !slices.Equal(first, want) {
		t.Fatalf("旧数据展示顺序必须为文件数组顺序: got %v, want %v", first, want)
	}
	for i := 1; i <= 20; i++ {
		if got := taskIDs(reloaded.list()); !slices.Equal(got, first) {
			t.Fatalf("旧数据顺序不稳定: %v != %v", got, first)
		}
	}

	// 补写结果必须已落盘: 再次重启后顺序与首次展示一致(不依赖文件数组位置)。
	reloaded2 := newAutoTaskManager(file, &taskBackend{})
	defer reloaded2.close()
	if got := taskIDs(reloaded2.list()); !slices.Equal(got, first) {
		t.Fatalf("补写创建时刻后再次重启顺序必须保持: %v != %v", got, first)
	}
}

// 编辑任务不得改变展示位置与创建时刻: 用户编辑中间任务后, 它的位置必须
// 保持不动(禁止闪现换位), CreatedAt 也不得被 normalize 重建时丢弃。
func TestUpdatePreservesCreatedAtAndOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	m := newAutoTaskManager(file, &taskBackend{})
	defer m.close()

	base := testWeekday(10, 0)
	created := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		m.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		task, err := m.create(aliasTaskInput{Enabled: false, AccountID: "a", Mode: taskModeScheduled,
			IntervalMinutes: 60, BatchCount: 1, TargetCount: 10})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, task.ID)
	}
	first := taskIDs(m.list())
	before, ok := m.get(created[1])
	if !ok {
		t.Fatal("任务不存在")
	}

	// 编辑中间的任务(改目标数量与周期)。
	if _, err := m.update(created[1], aliasTaskInput{Enabled: true, AccountID: "a", Mode: taskModeScheduled,
		IntervalMinutes: 120, BatchCount: 2, TargetCount: 20}); err != nil {
		t.Fatal(err)
	}

	after, _ := m.get(created[1])
	if after.CreatedAt != before.CreatedAt {
		t.Fatalf("编辑必须保留创建时刻: got %q, want %q", after.CreatedAt, before.CreatedAt)
	}
	if got := taskIDs(m.list()); !slices.Equal(got, first) {
		t.Fatalf("编辑任务不得改变展示位置: got %v, want %v", got, first)
	}
}
