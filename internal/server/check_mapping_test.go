package server

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/hmetest"
)

// TestCheckAccountMapsUpstreamStatus 验证 /check 的 HTTP 映射与落库状态:
// 401/421 → 401 + expired; 403/5xx → 502 + error。
func TestCheckAccountMapsUpstreamStatus(t *testing.T) {
	cases := []struct {
		upstream   int
		wantHTTP   int
		wantStatus string
	}{
		{http.StatusUnauthorized, http.StatusUnauthorized, account.StatusExpired},
		{http.StatusMisdirectedRequest, http.StatusUnauthorized, account.StatusExpired},
		{http.StatusForbidden, http.StatusBadGateway, account.StatusError},
		{http.StatusInternalServerError, http.StatusBadGateway, account.StatusError},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("upstream_%d", tc.upstream), func(t *testing.T) {
			mock := hmetest.New(t)
			mock.HMEListStatus = tc.upstream
			restore := hme.OverrideEndpointsForTest(mock.URL, mock.URL+"/setup/ws/1")
			defer restore()

			mgr, err := account.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.UpdateCookies(sum.ID, map[string]string{"X-APPLE-WEBAUTH-TOKEN": "v=1:t=x"}); err != nil {
				t.Fatalf("设置 Cookie 失败: %v", err)
			}

			b := &managerBackend{mgr: mgr}
			_, err = b.CheckAccount(sum.ID)
			var be *BackendError
			if !errors.As(err, &be) {
				t.Fatalf("期望 BackendError, 得到 %v", err)
			}
			if be.Status != tc.wantHTTP {
				t.Fatalf("HTTP 状态 = %d, 期望 %d", be.Status, tc.wantHTTP)
			}
			acc, ok := mgr.GetAccount(sum.ID)
			if !ok {
				t.Fatal("账号丢失")
			}
			if acc.Status != tc.wantStatus {
				t.Fatalf("账号状态 = %q, 期望 %q", acc.Status, tc.wantStatus)
			}
		})
	}
}
