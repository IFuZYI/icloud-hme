package server

import (
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/mail"
	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 并发回源合并(singleflight): 同一查询的并发请求只回源一次
// ====================================================================
//
// 背景: TTL 过期瞬间的并发请求(多标签页/前端重试)会各自回源, 对 163 等
// 外部收件邮箱形成 N 倍扫箱——风控的典型触发信号(见 cache.go 顶部说明)。
//
// 契约: 同一缓存键的并发 ListInbox 只触发一次上游读取, 所有调用方共享结果。
// 修复前本测试失败(RED): 8 个并发请求各自完成一次完整读取。
func TestListInboxConcurrentRequestsCoalesceToOneFetch(t *testing.T) {
	// 关闭 IMAP 层列表快照: 断言的是服务端层的合并, 快照会掩盖重复回源。
	prevSnap := mail.ListSnapshotTTL
	mail.ListSnapshotTTL = 0
	t.Cleanup(func() { mail.ListSnapshotTTL = prevSnap })

	srv := mailtest.NewServer(t, mailtest.Options{SelectDelay: 40 * time.Millisecond})
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

	b := &managerBackend{mgr: mgr}
	q := InboxQuery{AccountID: sum.ID, Limit: 20}

	// 预热: 建立连接与缓存。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	// 量出「单次回源」的 SELECT 数。
	base := srv.SelectCount()
	b.respCache().reset()
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	deltaOne := srv.SelectCount() - base
	if deltaOne == 0 {
		t.Fatal("单次回源应产生 SELECT, 测量异常")
	}

	// 并发 8 个请求: 应合并为 1 次回源。
	b.respCache().reset()
	base = srv.SelectCount()

	const n = 8
	var wg sync.WaitGroup
	results := make([]InboxResult, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 尽量同时发起, 保证窗口重叠
			results[i], errs[i] = b.ListInbox(q)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", i, errs[i])
		}
		if results[i].Total != 1 || len(results[i].Messages) != 1 {
			t.Fatalf("第 %d 个并发结果 = %d 封/total %d, want 1/1", i, len(results[i].Messages), results[i].Total)
		}
	}
	if got := srv.SelectCount() - base; got != deltaOne {
		t.Fatalf("并发请求应合并为 1 次回源(SELECT 增加 %d), 实际增加 %d", deltaOne, got)
	}
}

// ====================================================================
// 显式刷新(refresh=1): 每次点击都真实回源
// ====================================================================
//
// 背景: refresh=1 由前端「查询」按钮显式携带——用户点击「查询」的语义
// 就是「取新邮件」, 任何缓存/节流都可能让刚到的新邮件不可见, 因此显式
// 刷新必须无条件真实回源(绕过 TTL 缓存 + 作废 IMAP 层列表快照)。
// 防重复访问由「非刷新读取的短 TTL 缓存 + singleflight」承担。
//
// 契约: 连续刷新每次都回源; 快照 TTL(30s)内到达的新邮件刷新后立即可见。
func TestInboxEveryRefreshFetchesUpstream(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 预热(普通读取, 填充响应缓存与 IMAP 快照)。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	warm := srv.SelectCount()

	// 第一次刷新: 回源。
	q.Refresh = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	afterRefresh1 := srv.SelectCount()
	if afterRefresh1 == warm {
		t.Fatal("第一次刷新应回源")
	}

	// 紧接着再刷新: 不得被任何节流/缓存拦下。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == afterRefresh1 {
		t.Fatalf("每次刷新都应真实回源, SELECT %d → %d(未变)", afterRefresh1, got)
	}
}

// 刷新必须看到快照 TTL 内到达的新邮件。
//
// 背景: IMAP 层列表快照(默认 30s)服务翻页类读取; 但「查询」若命中旧
// 快照, 新邮件要等快照过期才可见——与「查询=取新邮件」的语义矛盾。
func TestInboxRefreshSeesNewMailWithinSnapshotTTL(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "第一封", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	r1, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Messages) != 1 {
		t.Fatalf("预热应 1 封, got %d", len(r1.Messages))
	}

	// 快照 TTL 内到达新邮件。
	srv.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "新邮件", time.Now().Add(time.Second))

	// 点击「查询」: 新邮件必须立即可见。
	q.Refresh = true
	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Messages) != 2 || r2.Total != 2 {
		t.Fatalf("刷新后应看到 2 封(含新邮件), got %d 封/total %d", len(r2.Messages), r2.Total)
	}

	// 刷新结果同时更新响应缓存: 后续非刷新读取立即一致且不回源。
	base := srv.SelectCount()
	q.Refresh = false
	r3, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r3.Messages) != 2 {
		t.Fatalf("刷新后缓存应为最新(2 封), got %d", len(r3.Messages))
	}
	if got := srv.SelectCount(); got != base {
		t.Fatalf("刷新后的非刷新读取应命中刚写入的缓存, SELECT %d → %d", base, got)
	}
}

// 刷新结果不得被并发的旧读取覆盖。
//
// 竞态: 非刷新读取 N 从旧快照取到旧数据(未含新邮件), 停在写缓存前; 显式
// 刷新 R 完成并写入最新数据; N 随后完成——若 N 仍写缓存, 后续读取会在
// TTL 内看到旧数据。契约: 刷新完成晚于 N 的发起时刻时, N 的写入被跳过。
func TestInboxStaleReadDoesNotClobberRefreshResult(t *testing.T) {
	prevTTL := inboxCacheTTL
	inboxCacheTTL = 50 * time.Millisecond
	t.Cleanup(func() { inboxCacheTTL = prevTTL })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "旧邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil { // 预热: 缓存 + 快照 = [旧邮件]
		t.Fatal(err)
	}
	// 新邮件到达(在快照 TTL 内: 非刷新读取看不到, 只有刷新能看到)。
	srv.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "新邮件", time.Now().Add(time.Second))
	time.Sleep(60 * time.Millisecond) // 缓存过期, N 必须回源(走旧快照)
	inboxCacheTTL = prevTTL           // 恢复长 TTL: 刷新写入的缓存要在最终读取时仍有效

	// N: 非刷新读取, 拿到旧数据后停在写缓存前。
	entered := make(chan struct{})
	release := make(chan struct{})
	inboxBeforeCacheWriteForTest = func() { close(entered); <-release }
	t.Cleanup(func() { inboxBeforeCacheWriteForTest = nil })
	var nErr error
	nDone := make(chan struct{})
	go func() { _, nErr = b.ListInbox(q); close(nDone) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("非刷新读取未到达写缓存钩子")
	}

	// R: 显式刷新, 看到新邮件并写入缓存。
	q.Refresh = true
	rRes, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(rRes.Messages) != 2 {
		t.Fatalf("刷新应看到 2 封, got %d", len(rRes.Messages))
	}

	// 释放 N: 其旧数据不得覆盖刷新结果。
	close(release)
	<-nDone
	if nErr != nil {
		t.Fatal(nErr)
	}

	// 后续非刷新读取: 必须看到刷新后的 2 封, 且来自缓存(不回源)。
	base := srv.SelectCount()
	q.Refresh = false
	final, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Messages) != 2 {
		t.Fatalf("刷新结果被旧读取覆盖: 应 2 封, got %d", len(final.Messages))
	}
	if got := srv.SelectCount(); got != base {
		t.Fatalf("最终读取应命中刷新写入的缓存, SELECT %d → %d", base, got)
	}
}

// 非刷新读取不受影响: TTL 内命中缓存(导航类读取不打上游), 过期后回源。
func TestInboxNonRefreshReadServedFromCache(t *testing.T) {
	prevTTL := inboxCacheTTL
	inboxCacheTTL = 200 * time.Millisecond
	t.Cleanup(func() { inboxCacheTTL = prevTTL })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()

	// TTL 内: 命中缓存, 不访问 IMAP。
	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("TTL 内非刷新读取应命中缓存, SELECT %d → %d", after1, got)
	}
	if len(r2.Messages) != 1 {
		t.Fatalf("TTL 内应返回缓存结果(1 封), got %d", len(r2.Messages))
	}

	// TTL 过期: 回源。
	time.Sleep(250 * time.Millisecond)
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("TTL 过期后普通读取应回源")
	}
}

// ====================================================================
// UIDVALIDITY 变化联动: 邮箱被重建后失效服务端读缓存
// ====================================================================
//
// 背景: preview 缓存以 folder:uid 为键; 邮箱被服务端重建(UIDVALIDITY
// 变化)后旧 UID 可能指向别的邮件——继续命中缓存会展示错误内容。
//
// 契约: 检测到 UIDVALIDITY 变化时, 该账号的收件箱/摘要缓存立即失效。
func TestUidValidityChangeInvalidatesServerReadCaches(t *testing.T) {
	prevTTL := inboxCacheTTL
	inboxCacheTTL = 20 * time.Millisecond
	t.Cleanup(func() { inboxCacheTTL = prevTTL })

	srv := mailtest.NewServer(t, mailtest.Options{UidValidity: 100})
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
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "验证码 123456", time.Now())

	b := &managerBackend{mgr: mgr}
	r, err := b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 {
		t.Fatalf("预热应 1 封, got %d", len(r.Messages))
	}
	id := r.Messages[0].ID

	// 预热摘要缓存, 并确认第二次命中缓存(不回源)。
	if _, err := b.FetchPreviews(sum.ID, []string{id}); err != nil {
		t.Fatal(err)
	}
	afterPreview1 := srv.SelectCount()
	if _, err := b.FetchPreviews(sum.ID, []string{id}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != afterPreview1 {
		t.Fatalf("摘要第二次应命中缓存, SELECT %d → %d", afterPreview1, got)
	}

	// 邮箱被服务端重建: UIDVALIDITY 变化。用 refresh 绕过服务端缓存并
	// 强制一次真实 SELECT 以触发检测。
	time.Sleep(60 * time.Millisecond) // 让 inbox 缓存过期(刷新地板内也须能检测)
	srv.SetUidValidity(200)
	if _, err := b.ListInbox(InboxQuery{AccountID: sum.ID, Limit: 20, Refresh: true}); err != nil {
		t.Fatal(err)
	}

	// 摘要缓存必须已被失效: 再次请求要回源。
	if _, err := b.FetchPreviews(sum.ID, []string{id}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == afterPreview1 {
		t.Fatal("UIDVALIDITY 变化后摘要缓存应失效并回源")
	}
}
