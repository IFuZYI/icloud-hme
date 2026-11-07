package server

import (
	"errors"
	"testing"

	"icloud-hme/internal/account"
)

// TestMarkSessionLossOnlyMarksRealSessionFailures 验证只有 401/421 才回写
// expired; 5xx 响应体里含 "session"/"401" 或 WAF 403 都不能误判。
func TestMarkSessionLossOnlyMarksRealSessionFailures(t *testing.T) {
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	b := &managerBackend{mgr: mgr}
	be := &BackendError{Status: 401, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}

	cases := []struct {
		msg  string
		want string
	}{
		{"HTTP 500: {\"error\":\"session store unavailable\"}", account.StatusPending},
		{"HTTP 502: <html>please enable cookies</html>", account.StatusPending},
		{"HTTP 403: forbidden", account.StatusPending},
		{"HTTP 401: ", account.StatusExpired},
	}
	for _, tc := range cases {
		if got := b.markSessionLoss(sum.ID, be, errors.New(tc.msg)); got == nil {
			t.Fatalf("markSessionLoss(%q) 返回 nil", tc.msg)
		}
		acc, ok := mgr.GetAccount(sum.ID)
		if !ok {
			t.Fatal("账号丢失")
		}
		if acc.Status != tc.want {
			t.Fatalf("cause=%q 状态 = %q, 期望 %q", tc.msg, acc.Status, tc.want)
		}
	}
}
