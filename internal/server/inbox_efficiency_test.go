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
// refresh 节流地板: 连点刷新不每次都回源
// ====================================================================
//
// 背景: refresh=1 绕过 TTL 缓存; 用户连点「刷新」时每次点击都是一次
// 完整回源。地板(默认 5s)内的重复刷新复用刚拉到的结果。
//
// 契约: 首次刷新回源; 地板内的再次刷新不回源; 超过地板后刷新回源。
func TestInboxRefreshThrottledByFloor(t *testing.T) {
	prev := inboxRefreshFloor
	inboxRefreshFloor = 300 * time.Millisecond
	t.Cleanup(func() { inboxRefreshFloor = prev })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 预热(普通读取)。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	warm := srv.SelectCount()

	// 第一次刷新: 回源(与「上次刷新」间隔无限大)。
	q.Refresh = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	afterRefresh1 := srv.SelectCount()
	if afterRefresh1 == warm {
		t.Fatal("第一次刷新应回源")
	}

	// 地板内再次刷新: 复用, 不回源。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != afterRefresh1 {
		t.Fatalf("地板内重复刷新不应回源, SELECT %d → %d", afterRefresh1, got)
	}

	// 超过地板: 回源。
	time.Sleep(400 * time.Millisecond)
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == afterRefresh1 {
		t.Fatal("超过地板后刷新应回源")
	}
}

// 非刷新请求不受地板影响: 缓存过期后的普通读取必须回源(拿到新邮件)。
func TestInboxNonRefreshReadNotThrottled(t *testing.T) {
	prevFloor := inboxRefreshFloor
	inboxRefreshFloor = 10 * time.Minute // 地板远大于测试时长
	t.Cleanup(func() { inboxRefreshFloor = prevFloor })
	prevTTL := inboxCacheTTL
	inboxCacheTTL = 20 * time.Millisecond
	t.Cleanup(func() { inboxCacheTTL = prevTTL })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()
	time.Sleep(60 * time.Millisecond) // TTL 过期
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("普通读取不受刷新地板约束, TTL 过期后应回源")
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
