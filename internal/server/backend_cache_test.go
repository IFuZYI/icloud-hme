package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/hmetest"
	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// newCacheTestBackend 建一个带真实 Manager 与进程内 IMAP 服务器的 managerBackend,
// 账号已配置 163 风格收件邮箱(读写都走 IMAP), 返回 backend 与 IMAP 服务器。
// 服务器计数(SELECT/连接)用于断言「缓存生效时不再访问 IMAP」。
func newCacheTestBackend(t *testing.T) (*managerBackend, *mailtest.Server, string) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163",
		Email:    "u@163.com",
		IMAPHost: srv.Host,
		IMAPPort: srv.Port,
		Password: "auth-code",
	}); err != nil {
		t.Fatalf("SetMailbox: %v", err)
	}
	return &managerBackend{mgr: mgr}, srv, sum.ID
}

// TestListInboxServedFromCacheAndRefreshBypass 验证收件箱信封列表在 TTL 内命中
// 服务端缓存, 不再重复访问 IMAP; refresh=1 绕过缓存强制回源。
// 高频重复读信是外部邮箱(163 等)风控/封禁的典型触发信号。
func TestListInboxServedFromCacheAndRefreshBypass(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "第一封", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	r1, err := b.ListInbox(q)
	if err != nil {
		t.Fatalf("第一次读信失败: %v", err)
	}
	if r1.Total != 1 {
		t.Fatalf("第一次应读到 1 封, got total=%d", r1.Total)
	}
	after1 := srv.SelectCount()

	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatalf("第二次读信失败: %v", err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("TTL 内第二次读信应命中缓存(IMAP SELECT 次数不变), 实际 %d → %d", after1, got)
	}
	if r2.Total != r1.Total || len(r2.Messages) != len(r1.Messages) {
		t.Fatalf("缓存结果应与首次一致: total %d/%d, msgs %d/%d", r2.Total, r1.Total, len(r2.Messages), len(r1.Messages))
	}

	// refresh=1 绕过缓存, 强制回源
	q.Refresh = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatalf("refresh 读信失败: %v", err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("refresh=1 应绕过缓存重新访问 IMAP")
	}
}

// TestListInboxCacheExpires 验证缓存过期后重新回源。
func TestListInboxCacheExpires(t *testing.T) {
	old := inboxCacheTTL
	inboxCacheTTL = 20 * time.Millisecond
	t.Cleanup(func() { inboxCacheTTL = old })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()
	time.Sleep(60 * time.Millisecond)
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("缓存过期后应重新访问 IMAP")
	}
}

// TestDeleteMessageInvalidatesInboxCache 验证删除邮件后收件箱缓存失效:
// 再次读取必须回源, 不能返回「还带着已删邮件」的缓存。
func TestDeleteMessageInvalidatesInboxCache(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "待删除", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	r1, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Messages) != 1 {
		t.Fatalf("预热应读到 1 封, got %d", len(r1.Messages))
	}
	id := r1.Messages[0].ID

	if err := b.DeleteMessage(accountID, id); err != nil {
		t.Fatalf("删除邮件失败: %v", err)
	}
	afterDelete := srv.SelectCount()

	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == afterDelete {
		t.Fatal("删除后缓存未失效: 再次读取没有回源")
	}
	if len(r2.Messages) != 0 {
		t.Fatalf("删除后应读到 0 封, got %d (缓存未失效)", len(r2.Messages))
	}
}

// TestListAliasesCacheAndWriteInvalidation 验证别名列表 TTL 缓存与写操作失效:
// 读取命中缓存不回源; 删除别名(写操作)后缓存失效, 下次读取回源。
func TestListAliasesCacheAndWriteInvalidation(t *testing.T) {
	mock := hmetest.New(t)
	t.Cleanup(hme.OverrideEndpointsForTest(mock.URL, mock.URL+"/setup/ws/1"))
	mock.HMEListBody = `{"success":true,"result":{"hmeEmails":[{"hme":"alpha@icloud.com","anonymousId":"anon_a","label":"Alpha","isActive":true}]}}`

	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateCookies(sum.ID, map[string]string{"X-APPLE-WEBAUTH-TOKEN": "v=1:t=x"}); err != nil {
		t.Fatalf("写入 Cookie 失败: %v", err)
	}

	b := &managerBackend{mgr: mgr}
	base := mock.HMEListCalls()

	a1, err := b.ListAliases(sum.ID)
	if err != nil {
		t.Fatalf("第一次列别名失败: %v", err)
	}
	if len(a1) != 1 || a1[0].Email != "alpha@icloud.com" {
		t.Fatalf("第一次应返回 1 个别名, got %+v", a1)
	}
	after1 := mock.HMEListCalls()
	if after1 != base+1 {
		t.Fatalf("第一次应回源 1 次, 实际 %d 次", after1-base)
	}

	if _, err := b.ListAliases(sum.ID); err != nil {
		t.Fatal(err)
	}
	if got := mock.HMEListCalls(); got != after1 {
		t.Fatalf("TTL 内第二次应命中缓存(不回源), 实际回源 %d 次", got-after1)
	}

	// 写操作(删除别名)后缓存失效, 下次读取必须回源
	if err := b.DeleteAlias(sum.ID, "anon_a"); err != nil {
		t.Fatalf("删除别名失败: %v", err)
	}
	if _, err := b.ListAliases(sum.ID); err != nil {
		t.Fatal(err)
	}
	if got := mock.HMEListCalls(); got != after1+1 {
		t.Fatalf("写操作后应回源 1 次, 实际 %d 次", got-after1)
	}
}

// TestFetchPreviewsServedFromCache 验证邮件摘要(渐进式加载第二阶段)命中缓存:
// 同一封邮件的摘要重复请求不再访问 IMAP。
func TestFetchPreviewsServedFromCache(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "验证码 123456", time.Now())

	r1, err := b.ListInbox(InboxQuery{AccountID: accountID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Messages) != 1 {
		t.Fatalf("应读到 1 封, got %d", len(r1.Messages))
	}
	id := r1.Messages[0].ID

	p1, err := b.FetchPreviews(accountID, []string{id})
	if err != nil {
		t.Fatalf("第一次拉摘要失败: %v", err)
	}
	after1 := srv.SelectCount()

	p2, err := b.FetchPreviews(accountID, []string{id})
	if err != nil {
		t.Fatalf("第二次拉摘要失败: %v", err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("摘要应命中缓存(IMAP SELECT 次数不变), 实际 %d → %d", after1, got)
	}
	if p2[id] != p1[id] {
		t.Fatalf("缓存摘要应与首次一致: %q vs %q", p2[id], p1[id])
	}
}

// TestInboxRefreshParamPassedToBackend 验证 HTTP 层的 refresh=1 透传到后端
// (前端「查询/刷新」按钮显式触发时携带, 绕过服务端缓存)。
func TestInboxRefreshParamPassedToBackend(t *testing.T) {
	f := &fakeBackend{
		accounts: []account.Summary{{ID: "acc_1", Name: "主号"}},
		inbox:    InboxResult{Method: "imap"},
	}
	_, ts := newTestServer(t, f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	status, body := aliasTaskRequest(t, ts.URL, session, csrf, http.MethodGet, "/api/inbox?account_id=acc_1&days=7", "")
	if status != http.StatusOK {
		t.Fatalf("无 refresh 参数: status = %d: %s", status, body)
	}
	if f.listInboxQuery.Refresh {
		t.Fatal("未带 refresh 的请求不应标记绕过缓存")
	}

	status, body = aliasTaskRequest(t, ts.URL, session, csrf, http.MethodGet, "/api/inbox?account_id=acc_1&days=7&refresh=1", "")
	if status != http.StatusOK {
		t.Fatalf("refresh=1: status = %d: %s", status, body)
	}
	if !f.listInboxQuery.Refresh {
		t.Fatal("refresh=1 应透传到后端(绕过缓存)")
	}
}

// TestSetMailboxInvalidatesReadCaches 验证「接入收件邮箱」后该账号的读缓存
// 立即失效: 换绑邮箱后若命中旧邮箱的缓存(信封/摘要), 用户会看到错误内容。
func TestSetMailboxInvalidatesReadCaches(t *testing.T) {
	srvA := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163", Email: "u@163.com", IMAPHost: srvA.Host, IMAPPort: srvA.Port, Password: "auth-code",
	}); err != nil {
		t.Fatal(err)
	}
	b := &managerBackend{mgr: mgr}

	srvA.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "旧邮箱邮件", time.Now())
	if _, err := b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 20}); err != nil {
		t.Fatal(err)
	}

	// 换绑到另一台服务器(新邮箱内容不同), 缓存必须立即失效。
	// 必须走 backend.SetMailbox(生产路径), 它负责失效读缓存。
	srvB := mailtest.NewServer(t, mailtest.Options{})
	srvB.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "新邮箱邮件", time.Now())
	if _, err := b.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163", Email: "u@163.com", IMAPHost: srvB.Host, IMAPPort: srvB.Port, Password: "auth-code",
	}); err != nil {
		t.Fatal(err)
	}
	// 注意: SetMailbox 自身会对新服务器做一次 InboxCount(SELECT), 因此
	// 基线取换绑之后的计数; 判定标准是「读取结果必须来自新邮箱」。
	base := srvB.SelectCount()
	r, err := b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if srvB.SelectCount() == base {
		t.Fatal("换绑后读取未访问新邮箱(仍命中旧缓存)")
	}
	if len(r.Messages) != 1 || r.Messages[0].Subject != "新邮箱邮件" {
		t.Fatalf("换绑后应读到新邮箱的邮件, got %+v", r.Messages)
	}
}

// TestMessageReadReusesMailboxConnection 验证读正文/删邮件(逐封操作)也复用
// 池内长连接: 本应用的核心场景是反复点开验证码邮件, 每次 TLS+LOGIN 同样会
// 被外部邮箱视为高频异常访问。修复前 GetMessage/DeleteMessage 各自新建连接(RED)。
func TestMessageReadReusesMailboxConnection(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "验证码 123456", time.Now())

	r, err := b.ListInbox(InboxQuery{AccountID: accountID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("应读到 1 封, got %d", len(r.Messages))
	}
	id := r.Messages[0].ID
	before := srv.ConnCount()

	// 点开邮件正文: 应复用 ListInbox 已建立的池内连接。
	msg, err := b.GetMessage(accountID, id)
	if err != nil {
		t.Fatalf("读取正文失败: %v", err)
	}
	if msg == nil || msg.ID == "" {
		t.Fatalf("正文为空: %+v", msg)
	}

	// 再读一次: 依然复用。
	if _, err := b.GetMessage(accountID, id); err != nil {
		t.Fatalf("第二次读取正文失败: %v", err)
	}

	// 删除邮件: 同样复用。
	if err := b.DeleteMessage(accountID, id); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if delta := srv.ConnCount() - before; delta != 0 {
		t.Fatalf("正文读取与删除应复用池内连接(新建 0 个), 实际新建 %d 个", delta)
	}
}

// TestReloadClearsReadCaches 验证配置重载后读缓存被清空:
// accounts.json 被手工修改(如换绑邮箱)后 reload, 旧缓存不得继续命中。
func TestReloadClearsReadCaches(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("预热阶段第二次应命中缓存, 实际 %d → %d", after1, got)
	}

	if err := b.Reload(); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("Reload 后应清空读缓存并回源")
	}
}

// TestInboxDaysFallbackCacheHit 验证 days 回退路径(不传 start/end)的缓存命中:
// handler 用 DateRangeFromDays 生成区间, 其 Start 基于 time.Now() 纳秒漂移——
// 缓存键必须量化, 否则同一逻辑查询每次都生成新键, 缓存永不命中(实测线上
// 连续三次同参请求每次都回源 163, 各 ~1.4s)。
func TestInboxDaysFallbackCacheHit(t *testing.T) {
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(mail.OverrideDialForTest(mailtest.DialInsecure))
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	sum, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "主号", ICloudEmail: "a@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetMailbox(sum.ID, account.MailboxConfig{
		Provider: "163", Email: "u@163.com", IMAPHost: srv.Host, IMAPPort: srv.Port, Password: "auth-code",
	}); err != nil {
		t.Fatal(err)
	}
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	s := newWithBackend(&managerBackend{mgr: mgr}, Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
		AutoTaskFile:  t.TempDir() + "/alias_task.json",
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	path := "/api/inbox?account_id=" + sum.ID + "&limit=20&days=7"
	fetch := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
		req.Header.Set("X-CSRF-Token", csrf)
		code, body, _ := do(t, req)
		if code != http.StatusOK {
			t.Fatalf("days=7 请求失败: %d %s", code, body)
		}
		return body
	}

	// 跨分钟边界会让量化键变化(缓存按设计失效), 极少数情况下重试一次。
	for attempt := 0; attempt < 3; attempt++ {
		minute := time.Now().Truncate(time.Minute)
		fetch()
		after1 := srv.SelectCount()
		body := fetch()
		if time.Now().Truncate(time.Minute) != minute {
			continue // 恰好跨分钟: 键变化属预期, 重试
		}
		if got := srv.SelectCount(); got != after1 {
			t.Fatalf("days=7 二次请求应命中缓存, IMAP SELECT %d → %d", after1, got)
		}
		if !strings.Contains(body, `"success":true`) {
			t.Fatalf("响应异常: %s", body)
		}
		return
	}
	t.Fatal("跨分钟重试 3 次仍未完成断言")
}
