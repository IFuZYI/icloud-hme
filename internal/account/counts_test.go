package account

import (
	"testing"

	"icloud-hme/internal/hme"
)

// TestUpdateAliasCountsRecomputes 验证按别名列表重算 total/active 并持久化。
func TestUpdateAliasCountsRecomputes(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	id := sum.ID
	if sum.AliasTotal != 0 || sum.AliasActive != 0 {
		t.Fatalf("初始计数应为 0/0, 得到 %d/%d", sum.AliasActive, sum.AliasTotal)
	}

	aliases := []hme.Alias{
		{Email: "x@icloud.com", Active: true},
		{Email: "y@icloud.com", Active: false},
		{Email: "z@icloud.com", Active: true},
	}
	if err := m.UpdateAliasCounts(id, aliases); err != nil {
		t.Fatal(err)
	}
	got, ok := m.GetAccount(id)
	if !ok {
		t.Fatal("账号丢失")
	}
	if got.AliasTotal != 3 || got.AliasActive != 2 {
		t.Fatalf("期望 2/3, 得到 %d/%d", got.AliasActive, got.AliasTotal)
	}

	// 幂等: 用更小的列表重算应覆盖而非累加。
	if err := m.UpdateAliasCounts(id, []hme.Alias{{Email: "x@icloud.com", Active: false}}); err != nil {
		t.Fatal(err)
	}
	got, _ = m.GetAccount(id)
	if got.AliasTotal != 1 || got.AliasActive != 0 {
		t.Fatalf("重算未覆盖旧值: 期望 0/1, 得到 %d/%d", got.AliasActive, got.AliasTotal)
	}
}

// TestUpdateAliasCountsUnknownAccount 验证未知账号返回错误。
func TestUpdateAliasCountsUnknownAccount(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateAliasCounts("acc_missing", nil); err == nil {
		t.Fatal("未知账号应返回错误")
	}
}

// TestUpdateAliasCountsPersists 验证计数写入磁盘, 重载后仍在。
func TestUpdateAliasCountsPersists(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateAliasCounts(sum.ID, []hme.Alias{{Email: "x@icloud.com", Active: true}}); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m2.GetAccount(sum.ID)
	if !ok {
		t.Fatal("重载后账号丢失")
	}
	if got.AliasTotal != 1 || got.AliasActive != 1 {
		t.Fatalf("重载后计数错误: 期望 1/1, 得到 %d/%d", got.AliasActive, got.AliasTotal)
	}
}
