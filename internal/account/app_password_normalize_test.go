package account

import (
	"strings"
	"testing"
)

// TestSetAppPasswordNormalizesEmail 验证 App 密码流程的邮箱也走规范化。
//
// 回归背景(规范审查发现): SetAppPassword 曾直接把用户输入写入 ICloudEmail,
// 未走 normalizeEmail——与「邮箱地址格式无效」报障同一类问题:
// 全角＠/前后空格的邮箱会原样入库, 之后 IMAP 连接失败只报
// 「IMAP 验证失败」, 用户难以定位是输入格式问题。
//
// 本测试在 IMAP 连接必然失败的环境下运行, 因此只能断言
// 「规范化发生在连接尝试之前」——即错误不来自邮箱格式, 而是连接本身。
func TestSetAppPasswordNormalizesEmail(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}

	// 全角＠ + 空格: 规范化后应得到干净地址再尝试连接。
	err = m.SetAppPassword(sum.ID, " user＠icloud.com ", "xxxx-xxxx-xxxx-xxxx")
	if err == nil {
		// 网络可达且凭据意外有效时不判定失败; 但此时入库值必须是规范化后的。
		acc, _ := m.GetAccount(sum.ID)
		if acc.ICloudEmail != "user@icloud.com" {
			t.Fatalf("入库邮箱 = %q, 期望规范化后的 %q", acc.ICloudEmail, "user@icloud.com")
		}
		return
	}
	// IMAP 连接失败是预期路径: 错误不应是邮箱格式类错误。
	msg := err.Error()
	if strings.Contains(msg, "格式无效") || strings.Contains(msg, "不能为空") {
		t.Fatalf("规范化后的邮箱不应触发格式错误: %v", err)
	}

	// 格式非法(无 @)的输入仍应被拒绝。
	err = m.SetAppPassword(sum.ID, "not-an-email", "xxxx-xxxx-xxxx-xxxx")
	if err == nil {
		t.Fatal("非法邮箱应被拒绝")
	}
	if !strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("非法邮箱错误 = %v, 期望含「格式无效」", err)
	}
}
