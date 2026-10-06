package account

import (
	"strings"
	"testing"
)

// TestValidateEmailNormalizesInput 验证邮箱校验对真实世界输入做规范化:
// 全角＠(中文输入法)、首尾空格、带引号的显示名格式。
//
// 背景(用户报障): 「邮箱地址格式无效」在中文输入法下极易触发——
// 全角＠看起来与半角无异, 但 net/mail.ParseAddress 直接拒绝。
func TestValidateEmailNormalizesInput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // 期望规范化后的值; 空表示期望报错
	}{
		{"标准邮箱", "test@icloud.com", "test@icloud.com"},
		{"首尾空格", "  test@icloud.com  ", "test@icloud.com"},
		{"全角＠(中文输入法)", "test＠icloud.com", "test@icloud.com"},
		{"全角＠+空格", " test＠icloud.com ", "test@icloud.com"},
		{"大写域名", "Test@iCloud.com", "Test@iCloud.com"},
		{"加号别名", "user+tag@icloud.com", "user+tag@icloud.com"},
		{"中国区", "test@icloud.com.cn", "test@icloud.com.cn"},
		{"显示名格式", `"测试" <test@icloud.com>`, "test@icloud.com"},
		{"空", "", ""},
		{"无@", "not-an-email", ""},
		{"只有@", "@icloud.com", ""},
		{"尾随逗号", "test@icloud.com,", ""},
		{"中间空格", "test @icloud.com", ""},
	}
	for _, tc := range cases {
		got, err := normalizeEmail(tc.input)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: 期望报错, 得到 %q", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 期望成功, 得到错误 %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: 规范化 = %q, 期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestAddAccountNormalizesStoredEmail 验证入库的邮箱是规范化后的值。
// 回归背景: 曾存储带空格的原始输入, 登录时用带空格用户名必然失败。
func TestAddAccountNormalizesStoredEmail(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "  test＠icloud.com  "})
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	if sum.ICloudEmail != "test@icloud.com" {
		t.Fatalf("存储的邮箱 = %q, 期望规范化后的 %q", sum.ICloudEmail, "test@icloud.com")
	}
	if sum.RealEmail != "test@icloud.com" {
		t.Fatalf("RealEmail = %q, 期望规范化后的 %q", sum.RealEmail, "test@icloud.com")
	}

	// 编辑路径同样规范化
	newEmail := " edited＠icloud.com "
	got, err := m.UpdateMetadata(sum.ID, UpdateAccountInput{ICloudEmail: &newEmail})
	if err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	if got.ICloudEmail != "edited@icloud.com" {
		t.Fatalf("编辑后邮箱 = %q, 期望 %q", got.ICloudEmail, "edited@icloud.com")
	}
}

// TestValidateEmailRejectsDisplayNameFormat 显示名格式应被提取出裸地址而非拒绝,
// 但多个地址(逗号分隔)必须拒绝——无法确定用哪个登录。
func TestValidateEmailRejectsMultipleAddresses(t *testing.T) {
	if _, err := normalizeEmail("a@icloud.com, b@icloud.com"); err == nil {
		t.Fatal("多个地址应当被拒绝")
	}
	got, err := normalizeEmail("主号 <a@icloud.com>")
	if err != nil {
		t.Fatalf("显示名格式应提取地址: %v", err)
	}
	if got != "a@icloud.com" {
		t.Fatalf("提取 = %q, 期望 a@icloud.com", got)
	}
}

// TestNormalizeEmailTrimsUnicodeSpaces 验证 Unicode 空白(全角空格)也被清理。
func TestNormalizeEmailTrimsUnicodeSpaces(t *testing.T) {
	got, err := normalizeEmail("\u3000test@icloud.com\u3000") // 全角空格
	if err != nil {
		t.Fatalf("全角空格应被清理: %v", err)
	}
	if got != "test@icloud.com" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "\u3000") {
		t.Fatal("结果仍含全角空格")
	}
}
