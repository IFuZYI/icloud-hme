package account

import (
	"strings"
	"testing"

	"icloud-hme/internal/hme"
	"icloud-hme/internal/hmetest"
)

// overrideEndpointsForAccountTest 把 hme 认证端点指向 mock 服务器。
func overrideEndpointsForAccountTest(mock *hmetest.Server) func() {
	return hme.OverrideEndpointsForTest(mock.URL, mock.URL+"/setup/ws/1")
}

// TestCheckAccountUpdatesStatusOnSuccess 验证检测成功时:
// 状态置 active、last_validated 刷新、last_error 清空。
func TestCheckAccountUpdatesStatusOnSuccess(t *testing.T) {
	mock := hmetest.New(t)
	restore := overrideEndpointsForAccountTest(mock)
	defer restore()

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	// 手动放入 Cookie(检测需要)
	m.mu.Lock()
	m.accounts[sum.ID].Cookies = map[string]string{"X-APPLE-WEBAUTH-TOKEN": "v=1:t=x"}
	m.accounts[sum.ID].Status = "error"
	m.accounts[sum.ID].LastError = "Cookie 校验失败: old"
	m.mu.Unlock()

	got, err := m.CheckAccount(sum.ID)
	if err != nil {
		t.Fatalf("检测失败: %v", err)
	}
	if got.Status != "active" {
		t.Fatalf("状态 = %q, 期望 active", got.Status)
	}
	if got.LastValidated == "" {
		t.Fatal("last_validated 应被刷新")
	}
	acc, _ := m.GetAccount(sum.ID)
	if acc.LastError != "" {
		t.Fatalf("last_error 应清空, 得到 %q", acc.LastError)
	}
	if _, _, _, _ = mock.Snapshot(); true {
		// validate 应被调用
		_, _, validateHits, _ := mock.Snapshot()
		if validateHits == 0 {
			t.Fatal("validate 端点未被调用")
		}
	}
}

// TestCheckAccountMarksErrorOnInvalidSession 验证会话失效时:
// 状态置 error 并记录可读原因; 不把秘密写进 LastError。
func TestCheckAccountMarksErrorOnInvalidSession(t *testing.T) {
	mock := hmetest.New(t)
	mock.FailValidate = true
	restore := overrideEndpointsForAccountTest(mock)
	defer restore()

	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[sum.ID].Cookies = map[string]string{"X-APPLE-WEBAUTH-TOKEN": "secret-value"}
	m.accounts[sum.ID].Status = "active"
	m.mu.Unlock()

	got, err := m.CheckAccount(sum.ID)
	if err == nil {
		t.Fatal("会话失效时检测应返回错误")
	}
	if got.Status != "error" {
		t.Fatalf("状态 = %q, 期望 error", got.Status)
	}
	acc, _ := m.GetAccount(sum.ID)
	if acc.LastError == "" {
		t.Fatal("last_error 应记录原因")
	}
	if strings.Contains(acc.LastError, "secret-value") {
		t.Fatalf("last_error 泄露秘密: %q", acc.LastError)
	}
}

// TestCheckAccountRequiresCookies 验证无 Cookie 的账号给出明确错误,
// 不发起网络请求。
func TestCheckAccountRequiresCookies(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.CheckAccount(sum.ID)
	if err == nil {
		t.Fatal("无 Cookie 应报错")
	}
	if !strings.Contains(err.Error(), "未配置 Cookie") {
		t.Fatalf("错误 = %v, 期望提示未配置 Cookie", err)
	}
}
