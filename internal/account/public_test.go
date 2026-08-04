package account

import (
	"encoding/json"
	"net/mail"
	"strings"
	"testing"
)

// TestSummaryDoesNotSerializeSecrets 验证 Summary 序列化后不包含任何秘密。
func TestSummaryDoesNotSerializeSecrets(t *testing.T) {
	acc := &Account{
		ID:          "acc_test",
		Name:        "主号",
		Cookies:     map[string]string{"token": "cookie-secret"},
		AppPassword: "app-secret",
		Proxy:       "http://user:proxy-secret@example.com:8080",
	}
	summary := acc.Summary()
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cookie-secret", "app-secret", "proxy-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("响应泄露秘密 %q: %s", secret, raw)
		}
	}
	if !summary.HasCookies || !summary.HasAppPassword || !summary.HasProxy {
		t.Fatalf("凭据状态错误: %+v", summary)
	}
}

// TestSummaryMapsStatusMessage 验证 status_message 使用固定文案,不泄露 LastError。
func TestSummaryMapsStatusMessage(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"pending", "等待配置或验证凭据"},
		{"error", "凭据验证失败"},
		{"active", ""},
	}
	for _, tc := range cases {
		acc := &Account{ID: "acc_" + tc.status, Status: tc.status, LastError: "内部错误: http://user:secret@host"}
		if got := acc.Summary().StatusMessage; got != tc.want {
			t.Errorf("status=%s 期望 StatusMessage=%q 得到 %q", tc.status, tc.want, got)
		}
	}
}

// TestAddAccountWithInputValidation 验证添加账号输入校验。
func TestAddAccountWithInputValidation(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		input AddAccountInput
		want  string // 期望错误片段;空表示成功
	}{
		{"空名称", AddAccountInput{Name: "   ", ICloudEmail: "a@icloud.com"}, "名称"},
		{"名称过长", AddAccountInput{Name: strings.Repeat("名", 65), ICloudEmail: "a@icloud.com"}, "名称"},
		{"非法主机", AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Host: "evil.com"}, "主机"},
		{"非法邮箱", AddAccountInput{Name: "主号", ICloudEmail: "not-an-email"}, "邮箱"},
		{"非法代理", AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "ftp://user:pass@host:21"}, "代理"},
		{"合法最小输入", AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"}, ""},
	}
	for _, tc := range cases {
		_, err := m.AddAccountWithInput(tc.input)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: 期望成功,得到 %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: 期望错误包含 %q,得到 %v", tc.name, tc.want, err)
		}
	}
}

// TestAddAccountWithInputValidatesEmailFormat 验证邮箱必须等于解析后的地址。
func TestAddAccountWithInputValidatesEmailFormat(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: `"Quoted" <a@icloud.com>`})
	if err == nil {
		t.Fatal("期望带显示名的邮箱被拒绝")
	}
	addr, err := mail.ParseAddress("a@icloud.com")
	if err != nil || addr.Address != "a@icloud.com" {
		t.Fatalf("测试前置错误: %v %q", err, addr)
	}
}

// TestUpdateMetadataValidation 验证编辑账号校验与行为。
func TestUpdateMetadataValidation(t *testing.T) {
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

	if _, err := m.UpdateMetadata(id, UpdateAccountInput{}); err == nil {
		t.Fatal("空更新应当报错")
	}
	empty := ""
	if _, err := m.UpdateMetadata(id, UpdateAccountInput{Name: &empty}); err == nil {
		t.Fatal("空名称应当报错")
	}
	badHost := "evil.com"
	if _, err := m.UpdateMetadata(id, UpdateAccountInput{Host: &badHost}); err == nil {
		t.Fatal("非法主机应当报错")
	}
	good := "新名称"
	got, err := m.UpdateMetadata(id, UpdateAccountInput{Name: &good})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "新名称" {
		t.Fatalf("期望名称被更新为 %q,得到 %q", "新名称", got.Name)
	}
	// 编辑不修改凭据
	if got.HasCookies || got.HasAppPassword {
		t.Fatalf("编辑不应产生凭据: %+v", got)
	}
}

// TestUpdateProxyClearsProxy 验证代理清除。
func TestUpdateProxyClearsProxy(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com", Proxy: "http://u:p@proxy.example.com:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.HasProxy {
		t.Fatalf("期望 HasProxy=true: %+v", sum)
	}
	got, err := m.UpdateProxy(sum.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasProxy {
		t.Fatalf("期望清除代理: %+v", got)
	}
}

// TestListSummariesStableOrder 验证 active → pending → error 排序及同状态稳定排序。
func TestListSummariesStableOrder(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts = map[string]*Account{
		"acc_3": {ID: "acc_3", Name: "错误号", Status: "error"},
		"acc_2": {ID: "acc_2", Name: "等待号", Status: "pending"},
		"acc_1": {ID: "acc_1", Name: "活跃号", Status: "active"},
		"acc_4": {ID: "acc_4", Name: "活跃零号", Status: "active"},
	}
	m.mu.Unlock()

	sums := m.ListSummaries()
	var got []string
	for _, s := range sums {
		got = append(got, s.ID)
	}
	want := []string{"acc_1", "acc_4", "acc_2", "acc_3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("排序错误: 得到 %v,期望 %v", got, want)
	}
}

// TestAddAccountWithInputNoNetwork 验证无 Cookie 的添加不访问网络。
func TestAddAccountWithInputNoNetwork(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Status != "pending" {
		t.Fatalf("无 Cookie 添加期望 pending,得到 %s", sum.Status)
	}
	if sum.ID == "" || !strings.HasPrefix(sum.ID, "acc_") {
		t.Fatalf("ID 格式错误: %q", sum.ID)
	}
}

// TestUpdateProxyInvalid 验证非法代理报固定文案且不泄露 URL。
func TestUpdateProxyInvalid(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.AddAccountWithInput(AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.UpdateProxy(sum.ID, "ftp://user:proxy-secret@host:21")
	if err == nil {
		t.Fatal("非法代理应当报错")
	}
	if strings.Contains(err.Error(), "proxy-secret") || strings.Contains(err.Error(), "ftp://") {
		t.Fatalf("错误不应泄露代理内容: %v", err)
	}
}
