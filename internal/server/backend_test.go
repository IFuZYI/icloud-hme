package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// fakeBackend 是测试用内存 Backend,记录调用,不访问网络。
type fakeBackend struct {
	accounts []account.Summary
	aliases  []hme.Alias
	inbox    InboxResult
	created  *hme.CreateResult

	addedInput   account.AddAccountInput
	updatedID    string
	updatedInput account.UpdateAccountInput
	proxyID      string
	proxyValue   string
	cookiesID    string
	cookiesValue string
	appPwdID     string
	appPwdEmail  string
	loginID      string
	loginErr     error
	removedID    string
	removedOK    bool

	aliasActID     string
	aliasActActive bool
	aliasActErr    error
	aliasActResult *bool
	aliasDeleteID  string
	aliasDeleteErr error
	aliasActIDs    []string
	aliasDeleteIDs []string
	aliasCallTimes []time.Time
	listInboxQuery InboxQuery
	reloadCount    int
}

func (f *fakeBackend) ListAccounts() []account.Summary { return f.accounts }

func (f *fakeBackend) AddAccount(in account.AddAccountInput) (account.Summary, error) {
	f.addedInput = in
	return account.Summary{ID: "acc_new", Name: in.Name, Status: "pending"}, nil
}

func (f *fakeBackend) UpdateAccount(id string, in account.UpdateAccountInput) (account.Summary, error) {
	f.updatedID, f.updatedInput = id, in
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) UpdateProxy(id, proxy string) (account.Summary, error) {
	f.proxyID, f.proxyValue = id, proxy
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 代理更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) UpdateCookies(id, cookies string) (account.Summary, error) {
	f.cookiesID, f.cookiesValue = id, cookies
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: cookie 更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) SetAppPassword(id, email, appPassword string) (account.Summary, error) {
	f.appPwdID, f.appPwdEmail = id, email
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 密码设置失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) SetMailbox(id string, config account.MailboxConfig) (account.Summary, error) {
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 收件邮箱设置失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) LoginAccount(id, password, otp string) (account.Summary, error) {
	f.loginID = id
	if f.loginErr != nil {
		return account.Summary{}, f.loginErr
	}
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 登录失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) RemoveAccount(id string) bool {
	f.removedID = id
	return f.removedOK
}

func (f *fakeBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	return f.created, nil
}

func (f *fakeBackend) ListAliases(accountID string) ([]hme.Alias, error) {
	return f.aliases, nil
}

func (f *fakeBackend) SetAliasActive(accountID, anonymousID string, active bool) (bool, error) {
	f.aliasActID, f.aliasActActive = anonymousID, active
	f.aliasActIDs = append(f.aliasActIDs, anonymousID)
	if f.aliasActResult != nil {
		return *f.aliasActResult, f.aliasActErr
	}
	return true, f.aliasActErr
}

func (f *fakeBackend) DeleteAlias(accountID, anonymousID string) error {
	f.aliasDeleteID = anonymousID
	f.aliasDeleteIDs = append(f.aliasDeleteIDs, anonymousID)
	f.aliasCallTimes = append(f.aliasCallTimes, time.Now())
	return f.aliasDeleteErr
}

func (f *fakeBackend) ListInbox(q InboxQuery) (InboxResult, error) {
	msgs, total := slicePage(f.inbox.Messages, q.Offset, q.Limit)
	return InboxResult{
		AccountID: q.AccountID,
		Alias:     q.Alias,
		Count:     len(msgs),
		Messages:  msgs,
		Method:    f.inbox.Method,
		Total:     total,
		Offset:    q.Offset,
		Warning:   f.inbox.Warning,
	}, nil
}

func (f *fakeBackend) FetchPreviews(accountID string, uids []uint32) (map[string]string, error) {
	previews := map[string]string{}
	for _, uid := range uids {
		previews[fmt.Sprintf("%d", uid)] = fmt.Sprintf("预览 %d", uid)
	}
	return previews, nil
}

func (f *fakeBackend) GetMessage(accountID string, uid uint32) (*mail.FullMessage, error) {
	return &mail.FullMessage{Message: mail.Message{ID: fmt.Sprint(uid)}}, nil
}

func (f *fakeBackend) DeleteMessage(accountID string, uid uint32) error { return nil }

func (f *fakeBackend) Reload() error {
	f.reloadCount++
	return nil
}

// newTestServer 构造带固定密码与 fake backend 的测试 Server。
func newTestServer(f *fakeBackend) (*Server, *httptest.Server) {
	cfg := Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
		SessionTTL:    12 * time.Hour,
	}
	s := newWithBackend(f, cfg)
	ts := httptest.NewServer(s.Handler())
	return s, ts
}

// login 登录测试服务并返回 session Cookie 与 CSRF。
func login(t *testing.T, ts *httptest.Server, password string) (sessionCookie, csrf string) {
	t.Helper()
	body := fmt.Sprintf(`{"password":%q}`, password)
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "hme_session" {
			return c.Value, out.Data.CSRFToken
		}
	}
	t.Fatalf("响应未设置 hme_session Cookie (status=%d)", resp.StatusCode)
	return "", ""
}

// authedReq 构造带会话 Cookie 与 CSRF 头的请求。
func authedReq(t *testing.T, ts *httptest.Server, method, path, body string) *http.Request {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func do(t *testing.T, req *http.Request) (int, string, []*http.Cookie) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Cookies()
}

// 渐进式收件箱: 分页参数透传 + previews 端点批量补摘要。
func TestInboxPaginationAndPreviews(t *testing.T) {
	f := &fakeBackend{
		accounts: []account.Summary{{ID: "acc_1", Name: "主号"}},
		inbox: InboxResult{
			Method: "imap",
			Messages: []mail.Message{
				{ID: "1", Subject: "m1"}, {ID: "2", Subject: "m2"}, {ID: "3", Subject: "m3"},
			},
		},
	}
	_, ts := newTestServer(f)
	cookie, csrf := login(t, ts, "admin-pass-2026-strong")

	// 第一页: offset=0 limit=2 → m3,m2(新→旧) + total=3
	req := authedReq(t, ts, "GET", "/api/inbox?account_id=acc_1&limit=2&offset=0&days=7", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ := do(t, req)
	if code != 200 {
		t.Fatalf("listInbox status = %d, body = %s", code, raw)
	}
	var listResp struct {
		Data InboxResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Data.Total != 3 || listResp.Data.Offset != 0 {
		t.Fatalf("total/offset = %d/%d, want 3/0", listResp.Data.Total, listResp.Data.Offset)
	}
	if len(listResp.Data.Messages) != 2 {
		t.Fatalf("page size = %d, want 2", len(listResp.Data.Messages))
	}

	// 第二页: offset=2 → m1
	req = authedReq(t, ts, "GET", "/api/inbox?account_id=acc_1&limit=2&offset=2&days=7", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("listInbox p2 status = %d", code)
	}
	if err := json.Unmarshal([]byte(raw), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Data.Messages) != 1 || listResp.Data.Messages[0].ID != "3" {
		t.Fatalf("page2 = %+v, want [m3]", listResp.Data.Messages)
	}

	// previews 端点: 批量 id → 摘要映射
	req = authedReq(t, ts, "POST", "/api/inbox/previews?account_id=acc_1", `{"ids":["1","2"]}`)
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, raw, _ = do(t, req)
	if code != 200 {
		t.Fatalf("previews status = %d, body = %s", code, raw)
	}
	var prevResp struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &prevResp); err != nil {
		t.Fatal(err)
	}
	if prevResp.Data["1"] == "" || prevResp.Data["2"] == "" {
		t.Fatalf("previews = %v, want ids 1,2 filled", prevResp.Data)
	}

	// 参数校验: 非法 offset 拒绝
	req = authedReq(t, ts, "GET", "/api/inbox?account_id=acc_1&offset=-1", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	code, _, _ = do(t, req)
	if code != 400 {
		t.Fatalf("offset=-1 status = %d, want 400", code)
	}
}
