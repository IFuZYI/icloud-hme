package apikey

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ====================================================================
// 秘钥管理: 生成 / 校验 / 持久化 / 权限
// ====================================================================
//
// 设计要点(与实现契约一一对应):
//   - 自动生成的秘钥格式: sk- 前缀 + 24 位随机字符(大小写字母+数字);
//   - 支持手动指定秘钥(留空自动生成), 手动值校验长度与字符集, 且必须唯一;
//   - 秘钥明文存于 data/api_keys.json(0600, 与 accounts.json 同权限级别),
//     面板可查看——本工具是本地单管理员模型, 与账号凭据的存储级别一致;
//   - 校验失败与「存在但已停用」对外表现一致(不泄露秘钥是否存在);
//   - 每个秘钥独立权限列表, 权限 ID 由 server 层目录校验(本包只透传)。

// newTestManager 建一个指向临时目录的 Manager。
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "api_keys.json")
	m, err := NewManager(file)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, file
}

// 自动生成的秘钥必须是 sk- 前缀 + 24 位字母数字。
func TestCreateAutoGeneratesSKKey(t *testing.T) {
	m, _ := newTestManager(t)

	rec, err := m.Create(CreateInput{Name: "脚本A", Permissions: []string{"inbox.list"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(rec.Key, "sk-") {
		t.Fatalf("key = %q, 应以 sk- 开头", rec.Key)
	}
	body := strings.TrimPrefix(rec.Key, "sk-")
	if len(body) != 24 {
		t.Fatalf("随机部分长度 = %d, want 24 (key=%q)", len(body), rec.Key)
	}
	for _, r := range body {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			t.Fatalf("随机部分含非法字符 %q (key=%q)", r, rec.Key)
		}
	}
	if !strings.HasPrefix(rec.ID, "key_") {
		t.Fatalf("id = %q, 应以 key_ 开头", rec.ID)
	}
	if rec.Name != "脚本A" {
		t.Fatalf("name = %q", rec.Name)
	}
	if !rec.Enabled {
		t.Fatal("新建秘钥默认应启用")
	}
	if rec.CreatedAt == "" {
		t.Fatal("created_at 不应为空")
	}
	if len(rec.Permissions) != 1 || rec.Permissions[0] != "inbox.list" {
		t.Fatalf("permissions = %v", rec.Permissions)
	}
}

// 连续生成的秘钥必须互不相同(随机性)。
func TestCreateAutoKeysAreUnique(t *testing.T) {
	m, _ := newTestManager(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		rec, err := m.Create(CreateInput{Name: "批量"})
		if err != nil {
			t.Fatalf("第 %d 次 Create: %v", i, err)
		}
		if seen[rec.Key] {
			t.Fatalf("秘钥重复: %q", rec.Key)
		}
		seen[rec.Key] = true
	}
}

// 手动指定的秘钥原样保存; 留空则自动生成。
func TestCreateAcceptsCustomKey(t *testing.T) {
	m, _ := newTestManager(t)

	rec, err := m.Create(CreateInput{Name: "手动", Key: "my-script-key-2026"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Key != "my-script-key-2026" {
		t.Fatalf("key = %q, want 手动值原样", rec.Key)
	}
}

// 非法手动秘钥(过短/过长/含空白/含控制字符)必须拒绝。
func TestCreateRejectsInvalidCustomKey(t *testing.T) {
	m, _ := newTestManager(t)
	cases := []struct {
		name string
		key  string
	}{
		{"过短", "short"},
		{"过长", strings.Repeat("a", 129)},
		{"含空格", "has space in key"},
		{"含制表符", "tab\tkey-12345"},
		{"含换行", "line\nbreak-key"},
		{"含中文", "秘钥中文不该允许-abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Create(CreateInput{Name: "x", Key: tc.key}); err == nil {
				t.Fatalf("非法秘钥 %q 应被拒绝", tc.key)
			}
		})
	}
	// 恰好 8 位与 128 位是边界合法值。
	if _, err := m.Create(CreateInput{Key: "12345678"}); err != nil {
		t.Fatalf("8 位应合法: %v", err)
	}
	if _, err := m.Create(CreateInput{Key: strings.Repeat("a", 128)}); err != nil {
		t.Fatalf("128 位应合法: %v", err)
	}
}

// 重复秘钥必须拒绝(自动生成理论不重复, 手动指定必须查重)。
func TestCreateRejectsDuplicateKey(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Create(CreateInput{Name: "a", Key: "dup-key-123456"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(CreateInput{Name: "b", Key: "dup-key-123456"}); err == nil {
		t.Fatal("重复秘钥应被拒绝")
	}
	if !errors.Is(mustErr(m, CreateInput{Name: "b", Key: "dup-key-123456"}), ErrKeyExists) {
		t.Fatal("应返回 ErrKeyExists")
	}
}

// mustErr 返回 Create 的错误(用于 errors.Is 断言)。
func mustErr(m *Manager, in CreateInput) error {
	_, err := m.Create(in)
	return err
}

// 秘钥数量上限(防止无界增长)。
func TestCreateCapsKeyCount(t *testing.T) {
	m, _ := newTestManager(t)
	for i := 0; i < maxKeys; i++ {
		if _, err := m.Create(CreateInput{Name: "x"}); err != nil {
			t.Fatalf("第 %d 个: %v", i, err)
		}
	}
	if _, err := m.Create(CreateInput{Name: "overflow"}); !errors.Is(err, ErrTooManyKeys) {
		t.Fatalf("超出上限应返回 ErrTooManyKeys, got %v", err)
	}
}

// 校验: 有效秘钥返回记录并记录 last_used; 未知/停用秘钥返回 false。
func TestValidateAcceptsAndTouches(t *testing.T) {
	m, _ := newTestManager(t)
	rec, err := m.Create(CreateInput{Name: "脚本", Permissions: []string{"inbox.list"}})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := m.Validate(rec.Key, "10.0.0.9")
	if !ok {
		t.Fatal("有效秘钥应校验通过")
	}
	if got.ID != rec.ID || !got.HasPermission("inbox.list") {
		t.Fatalf("got = %+v", got)
	}
	if got.LastUsedAt == "" || got.LastUsedIP != "10.0.0.9" {
		t.Fatalf("last_used 未记录: %+v", got)
	}

	if _, ok := m.Validate("sk-unknown-key-000000", "10.0.0.9"); ok {
		t.Fatal("未知秘钥不应通过")
	}
	if _, ok := m.Validate("", "10.0.0.9"); ok {
		t.Fatal("空秘钥不应通过")
	}

	// 停用后立即失效。
	disabled := false
	if _, err := m.Update(rec.ID, UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Validate(rec.Key, "10.0.0.9"); ok {
		t.Fatal("停用的秘钥不应通过")
	}
}

// 更新: 名称 / 权限 / 启用状态; 不存在的 ID 返回 ErrKeyNotFound。
func TestUpdateChangesFields(t *testing.T) {
	m, _ := newTestManager(t)
	rec, err := m.Create(CreateInput{Name: "旧名", Permissions: []string{"inbox.list"}})
	if err != nil {
		t.Fatal(err)
	}

	name := "新名"
	perms := []string{"inbox.list", "inbox.get"}
	enabled := false
	updated, err := m.Update(rec.ID, UpdateInput{Name: &name, Permissions: &perms, Enabled: &enabled})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "新名" || len(updated.Permissions) != 2 || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.Key != rec.Key {
		t.Fatal("更新不应改变秘钥值")
	}
	// 秘钥值不可更新(即使传入 Key 字段也不生效——UpdateInput 无此字段)。

	if _, err := m.Update("key_nope", UpdateInput{Name: &name}); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("不存在应返回 ErrKeyNotFound, got %v", err)
	}
}

// 删除后秘钥立即失效, 且列表不再包含。
func TestDeleteRemovesKey(t *testing.T) {
	m, _ := newTestManager(t)
	rec, err := m.Create(CreateInput{Name: "待删"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := m.Validate(rec.Key, "127.0.0.1"); ok {
		t.Fatal("删除后秘钥不应通过")
	}
	for _, k := range m.List() {
		if k.ID == rec.ID {
			t.Fatal("列表仍包含已删秘钥")
		}
	}
	if err := m.Delete(rec.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("重复删除应返回 ErrKeyNotFound, got %v", err)
	}
}

// 持久化: 重建 Manager 后秘钥、权限、启用状态与 last_used 均保留。
func TestPersistenceRoundTrip(t *testing.T) {
	prev := lastUsedPersistInterval
	lastUsedPersistInterval = 0 // 测试: 每次校验立即落盘
	t.Cleanup(func() { lastUsedPersistInterval = prev })

	m, file := newTestManager(t)
	rec, err := m.Create(CreateInput{Name: "脚本", Key: "persist-key-1234", Permissions: []string{"inbox.list", "create.alias"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Validate(rec.Key, "192.168.1.2"); !ok {
		t.Fatal("校验失败")
	}

	m2, err := NewManager(file)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	list := m2.List()
	if len(list) != 1 {
		t.Fatalf("重新加载后应有 1 个秘钥, got %d", len(list))
	}
	got := list[0]
	if got.Key != "persist-key-1234" || got.Name != "脚本" || len(got.Permissions) != 2 {
		t.Fatalf("重新加载后记录不符: %+v", got)
	}
	if got.LastUsedAt == "" || got.LastUsedIP != "192.168.1.2" {
		t.Fatalf("last_used 应持久化: %+v", got)
	}
	if _, ok := m2.Validate("persist-key-1234", "127.0.0.1"); !ok {
		t.Fatal("重新加载后校验应通过")
	}
}

// 列表按创建时间新→旧(最近创建的在前)。
func TestListNewestFirst(t *testing.T) {
	m, _ := newTestManager(t)
	first, _ := m.Create(CreateInput{Name: "第一个"})
	second, _ := m.Create(CreateInput{Name: "第二个"})
	list := m.List()
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("顺序错误: %s, %s", list[0].ID, list[1].ID)
	}
}

// 损坏的 JSON 必须让 NewManager 报错(与 account.Manager 同语义: 不静默丢数据)。
func TestCorruptFileRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "api_keys.json")
	if err := os.WriteFile(file, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(file); err == nil {
		t.Fatal("损坏文件应报错")
	}
}

// 落盘文件权限必须是 0600(与 accounts.json 同级)。
func TestFilePermissions0600(t *testing.T) {
	m, file := newTestManager(t)
	if _, err := m.Create(CreateInput{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}
	// 临时文件不残留。
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件未清理")
	}
}

// 空文件路径 = 纯内存模式(测试便捷): 功能可用、不落盘。
func TestInMemoryModeWithoutFile(t *testing.T) {
	m, err := NewManager("")
	if err != nil {
		t.Fatalf("NewManager(\"\"): %v", err)
	}
	rec, err := m.Create(CreateInput{Name: "内存"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Validate(rec.Key, "127.0.0.1"); !ok {
		t.Fatal("内存模式校验应通过")
	}
}

// HasPermission 对空权限列表返回 false(失败关闭)。
func TestHasPermission(t *testing.T) {
	k := Key{}
	if k.HasPermission("inbox.list") {
		t.Fatal("空权限不应放行")
	}
	k = Key{Permissions: []string{"inbox.list"}}
	if !k.HasPermission("inbox.list") || k.HasPermission("accounts.list") {
		t.Fatal("权限匹配错误")
	}
}
