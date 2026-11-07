package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkInvalidSetsExpiredAndSanitizesDetail 验证业务接口判定会话失效后的回写:
// 状态置 expired、last_error 记录原因、status_detail 对外脱敏且文案可操作。
func TestMarkInvalidSetsExpiredAndSanitizesDetail(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}

	m.MarkInvalid(sum.ID, "HTTP 401: 代理 http://user:secret@host:1080 拒绝")

	acc, ok := m.GetAccount(sum.ID)
	if !ok {
		t.Fatal("账号丢失")
	}
	if acc.Status != StatusExpired {
		t.Fatalf("status = %q, 期望 %q", acc.Status, StatusExpired)
	}
	if acc.LastError == "" {
		t.Fatal("last_error 应记录失效原因")
	}

	s := acc.Summary()
	if !strings.Contains(s.StatusMessage, "Cookie") {
		t.Fatalf("status_message = %q, 期望提示 Cookie 失效", s.StatusMessage)
	}
	if strings.Contains(s.StatusDetail, "secret") {
		t.Fatalf("status_detail 泄露凭据: %q", s.StatusDetail)
	}
	if !strings.Contains(s.StatusDetail, "://***@") {
		t.Fatalf("status_detail 未脱敏: %q", s.StatusDetail)
	}
}

// TestMarkInvalidIdempotent 验证重复 401 不反复改写落盘内容。
func TestMarkInvalidIdempotent(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}

	m.MarkInvalid(sum.ID, "第一次 401")
	first, _ := m.GetAccount(sum.ID)
	m.MarkInvalid(sum.ID, "第二次 401")
	second, _ := m.GetAccount(sum.ID)

	if second.LastError != first.LastError {
		t.Fatalf("重复回写不应覆盖已有原因: %q -> %q", first.LastError, second.LastError)
	}
	if second.Status != StatusExpired {
		t.Fatalf("status = %q", second.Status)
	}
}

// TestMarkInvalidRetriesAfterSaveFailure 验证一次写盘失败不会永久丢失状态:
// 下一次 MarkInvalid 会重试写入。
func TestMarkInvalidRetriesAfterSaveFailure(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}

	// 删除数据目录制造写失败(进程以 root 运行时 chmod 无法阻断写入)。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkInvalid(sum.ID, "第一次 401"); err == nil {
		t.Fatal("写盘失败时应返回错误")
	}

	// 恢复目录后重试必须真正落盘(而不是被幂等守卫跳过)。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkInvalid(sum.ID, "第二次 401"); err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !strings.Contains(string(raw), `"status": "expired"`) {
		t.Fatalf("expired 未落盘: %s", string(raw))
	}
}
