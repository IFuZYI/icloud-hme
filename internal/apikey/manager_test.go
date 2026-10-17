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

// Update 写盘失败必须回滚内存变更: 内存态与持久态不得发散。
//
// 背景(独立审查发现): Update() 先原地修改 *Key 再 saveLocked(), 写盘失败
// 时只返回错误、内存已被改——API 契约「报错 = 未生效」被违反, 且重启后
// 磁盘旧态静默覆盖。Create 有回滚(delete), Update 没有。
//
// 注入: 把文件路径指到不可写位置(已存在同名目录), 强制 saveLocked 失败。
func TestUpdateRollbackOnPersistFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "api_keys.json")
	m, err := NewManager(file)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := m.Create(CreateInput{Name: "原名", Permissions: []string{"inbox.list"}})
	if err != nil {
		t.Fatal(err)
	}

	// 让后续写盘必然失败: 把文件替换为同名目录(atomic write 无法覆盖目录)。
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}

	name := "改名"
	enabled := false
	if _, err := m.Update(rec.ID, UpdateInput{Name: &name, Enabled: &enabled}); err == nil {
		t.Fatal("写盘失败应返回错误")
	}

	// 内存态必须回滚到原值(报错 = 未生效)。
	got, ok := m.Get(rec.ID)
	if !ok {
		t.Fatal("秘钥不应消失")
	}
	if got.Name != "原名" || !got.Enabled {
		t.Fatalf("写盘失败后内存应回滚: name=%q enabled=%v", got.Name, got.Enabled)
	}
}

// Delete 写盘失败必须回滚内存变更: 与 Update 同一契约「报错 = 未生效」。
//
// 注入手法与 TestUpdateRollbackOnPersistFailure 相同: 把 api_keys.json 替换为
// 同名目录, 使原子写(Rename 到目录)必然失败。
func TestDeleteRollbackOnPersistFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "api_keys.json")
	m, err := NewManager(file)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := m.Create(CreateInput{Name: "原名", Permissions: []string{"inbox.list"}})
	if err != nil {
		t.Fatal(err)
	}

	// 让后续写盘必然失败: 把文件替换为同名目录(atomic write 无法覆盖目录)。
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := m.Delete(rec.ID); err == nil {
		t.Fatal("写盘失败应返回错误")
	}

	// 内存态必须回滚: 键仍在, 字段原样(报错 = 未生效)。
	got, ok := m.Get(rec.ID)
	if !ok {
		t.Fatal("秘钥不应消失")
	}
	if got.Name != "原名" || !got.Enabled {
		t.Fatalf("写盘失败后内存应回滚: name=%q enabled=%v", got.Name, got.Enabled)
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

// ====================================================================
// 账号使用范围: Key.AccountIDs
// ====================================================================
//
// 语义(失败关闭):
//   - 空 AccountIDs = 不限账号(全部账号可用) —— 保持建秘钥时的默认行为;
//   - 非空 AccountIDs = 白名单, 只有列出的账号可用;
//   - 未列入白名单的账号一律拒绝(不允许空范围扩张成"全部")。

// 空范围放行任意账号(向后兼容: 旧秘钥不受影响)。
func TestAllowsAccountEmptyScopeMeansAll(t *testing.T) {
	k := Key{}
	for _, id := range []string{"acc_1", "acc_2", ""} {
		if !k.AllowsAccount(id) {
			t.Fatalf("空范围应对账号 %q 放行", id)
		}
	}
}

// 非空范围只放行列出的账号; 未列出的账号(含空串)必须拒绝。
func TestAllowsAccountWhitelist(t *testing.T) {
	k := Key{AccountIDs: []string{"acc_1", "acc_3"}}
	if !k.AllowsAccount("acc_1") || !k.AllowsAccount("acc_3") {
		t.Fatal("白名单内账号应放行")
	}
	if k.AllowsAccount("acc_2") {
		t.Fatal("白名单外账号 acc_2 必须拒绝")
	}
	if k.AllowsAccount("") {
		t.Fatal("空账号 ID 在白名单模式下必须拒绝(不能当作通配)")
	}
}

// 创建时带账号范围应被保存并去重。
func TestCreateStoresAccountIDs(t *testing.T) {
	m, _ := newTestManager(t)
	rec, err := m.Create(CreateInput{
		Name:        "脚本A",
		Permissions: []string{"inbox.list"},
		AccountIDs:  []string{"acc_1", "acc_2", "acc_1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(rec.AccountIDs) != 2 {
		t.Fatalf("AccountIDs = %v, want 去重后 2 项", rec.AccountIDs)
	}
	got, _ := m.Get(rec.ID)
	if len(got.AccountIDs) != 2 {
		t.Fatalf("Get 后 AccountIDs = %v, want 2 项", got.AccountIDs)
	}
}

// 账号范围可经 Update 修改; nil 保持不变, 显式空切片清空为"全部"。
func TestUpdateAccountIDs(t *testing.T) {
	m, _ := newTestManager(t)
	rec, _ := m.Create(CreateInput{Name: "x", AccountIDs: []string{"acc_1"}})

	// nil: 不变
	got, err := m.Update(rec.ID, UpdateInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AccountIDs) != 1 || got.AccountIDs[0] != "acc_1" {
		t.Fatalf("nil 更新后 AccountIDs = %v, want [acc_1]", got.AccountIDs)
	}

	// 显式空切片: 清空 = 全部
	empty := []string{}
	got, err = m.Update(rec.ID, UpdateInput{AccountIDs: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AccountIDs) != 0 {
		t.Fatalf("清空后 AccountIDs = %v, want 空(全部)", got.AccountIDs)
	}
}

// 账号范围需持久化, 重新加载后仍然生效。
func TestAccountIDsPersistAcrossReload(t *testing.T) {
	m, file := newTestManager(t)
	rec, _ := m.Create(CreateInput{Name: "x", AccountIDs: []string{"acc_7"}})

	reloaded, err := NewManager(file)
	if err != nil {
		t.Fatalf("重新加载: %v", err)
	}
	got, ok := reloaded.Get(rec.ID)
	if !ok {
		t.Fatal("重新加载后找不到秘钥")
	}
	if len(got.AccountIDs) != 1 || got.AccountIDs[0] != "acc_7" {
		t.Fatalf("重新加载后 AccountIDs = %v, want [acc_7]", got.AccountIDs)
	}
}

// ====================================================================
// 恒定时间秘钥比较
// ====================================================================
//
// 契约: Validate 用 crypto/subtle 常量时间比较, 且不因某个秘钥匹配而提前
// 中断遍历——时间开销与「匹配项位置」无关, 减少时序侧信道。
// (手动秘钥允许 8 位, 低熵场景下时序信息更敏感。)

// Validate 对整个列表做常量时间比较: 匹配项在头部与尾部时结果一致。
func TestValidateMatchesRegardlessOfPosition(t *testing.T) {
	m, _ := newTestManager(t)
	// 先建若干秘钥, 目标秘钥放最后。
	base := "sk-aaaaaaaaaaaaaaaaaaaaaa"
	for i := 0; i < 5; i++ {
		custom := string(rune('0'+i)) + base[3:]
		if _, err := m.Create(CreateInput{Name: string(rune('a' + i)), Key: custom}); err != nil {
			t.Fatal(err)
		}
	}
	target, err := m.Create(CreateInput{Name: "target", Key: "sk-bbbbbbbbbbbbbbbbbbbbbb"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Validate(target.Key, "127.0.0.1")
	if !ok || got.ID != target.ID {
		t.Fatalf("列表尾部秘钥应校验通过, ok=%v", ok)
	}
}

// 停用秘钥排在启用秘钥之前时, 不得因提前短路而误放行后面的启用秘钥。
func TestValidateDoesNotShortCircuitOnDisabled(t *testing.T) {
	m, _ := newTestManager(t)
	// 先建一个停用秘钥, 再建一个启用的。
	disabled, err := m.Create(CreateInput{Name: "disabled", Key: "sk-cccccccccccccccccccccccc"})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := m.Update(disabled.ID, UpdateInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	enabled, err := m.Create(CreateInput{Name: "enabled", Key: "sk-dddddddddddddddddddddddd"})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := m.Validate(enabled.Key, "127.0.0.1"); !ok {
		t.Fatal("启用秘钥应校验通过")
	}
	if _, ok := m.Validate(disabled.Key, "127.0.0.1"); ok {
		t.Fatal("停用秘钥不应通过")
	}
}

// 恒定时间比较的源码契约: Validate 必须使用 crypto/subtle 而非明文 == 比较。
//
// 理由: 手动秘钥允许 8 位低熵值, 明文字符串比较的短路时机可泄露前缀匹配
// 长度。本测试钉住实现方式(无运行时行为面可观测), 靠变异验证保证非空洞
// ——把 subtle 换回 == 会让本测试变红。
func TestValidateUsesConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "subtle.ConstantTimeCompare(") {
		t.Fatal("Validate 应使用 crypto/subtle 恒定时间比较, 未找到 subtle.ConstantTimeCompare")
	}
	// 防御性: Validate 函数体内不得出现明文的秘钥 == 比较。
	start := strings.Index(body, "func (m *Manager) Validate(")
	if start < 0 {
		t.Fatal("未找到 Validate 函数")
	}
	fn := body[start:]
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	if strings.Contains(fn, "k.Key ==") || strings.Contains(fn, "k.Key !=") {
		t.Fatal("Validate 仍含明文秘钥比较(应全部走 ConstantTimeCompare)")
	}
}

// ====================================================================
// F8: 数据目录权限收紧
// ====================================================================
//
// 契约: 秘钥目录创建时使用 0700(仅属主可进入)。秘钥文件本身 0600,
// 目录若为 0755 则同机其他用户可列目录、推断文件名(内容仍受 0600 保护)。

// 首次写入时创建的目录权限为 0700。
func TestSaveCreatesDirWith0700(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "sub", "deep")
	file := filepath.Join(nested, "api_keys.json")
	m, err := NewManager(file)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Create(CreateInput{Name: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("目录未创建: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("数据目录权限 = %o, want 0700", perm)
	}
}
