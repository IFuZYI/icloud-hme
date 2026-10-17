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
// 两级显式刷新: 「刷新」(force=1) 无条件回源; 「查询」(refresh=1) 受限
// ====================================================================
//
// 语义(用户确认):
//   - 「刷新」按钮(force=1): 强制刷新——每次点击都无条件真实回源(绕过
//     TTL 缓存 + 作废 IMAP 层列表快照), 不受任何节流约束;
//   - 「查询」按钮(refresh=1): 受限刷新——绕过 TTL 缓存回源, 但受刷新
//     节流地板约束: 地板(默认 5s)内的重复查询复用最近一次回源结果,
//     防连点打爆上游。
// 防重复访问另由「非刷新读取的短 TTL 缓存 + singleflight」承担。

// 「刷新」(force): 每次点击都真实回源。
func TestInboxForceFetchesUpstreamEveryClick(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 预热(普通读取, 填充响应缓存与 IMAP 快照)。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	warm := srv.SelectCount()

	// 第一次刷新: 回源。
	q.Force = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()
	if after1 == warm {
		t.Fatal("第一次刷新应回源")
	}

	// 紧接着再刷新: 不受任何节流/缓存拦下。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatalf("每次刷新都应真实回源, SELECT %d → %d(未变)", after1, got)
	}
}

// 「刷新」(force): 快照 TTL 内到达的新邮件立即可见(快照被作废)。
func TestInboxForceSeesNewMailWithinSnapshotTTL(t *testing.T) {
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

	// 快照 TTL 内到达新邮件(普通读取看不到, 刷新必须能看到)。
	srv.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "新邮件", time.Now().Add(time.Second))

	q.Force = true
	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Messages) != 2 || r2.Total != 2 {
		t.Fatalf("刷新后应看到 2 封(含新邮件), got %d 封/total %d", len(r2.Messages), r2.Total)
	}

	// 刷新结果同时更新响应缓存: 后续非刷新读取立即一致且不回源。
	base := srv.SelectCount()
	q.Force = false
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

// 「查询」(refresh): 首次回源(可见快照 TTL 内的新邮件); 地板内复用;
// 超过地板后重新回源。
func TestInboxRefreshThrottledByFloor(t *testing.T) {
	prev := inboxRefreshFloor
	inboxRefreshFloor = 300 * time.Millisecond
	t.Cleanup(func() { inboxRefreshFloor = prev })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "第一封", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 预热(普通读取)。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	warm := srv.SelectCount()

	// 第一次查询: 回源, 且看到快照 TTL 内到达的新邮件。
	srv.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "新邮件", time.Now().Add(time.Second))
	q.Refresh = true
	r1, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Messages) != 2 {
		t.Fatalf("查询应看到新邮件(2 封), got %d", len(r1.Messages))
	}
	after1 := srv.SelectCount()
	if after1 == warm {
		t.Fatal("第一次查询应回源")
	}

	// 地板内再次查询: 复用最近结果, 不回源。
	r2, err := b.ListInbox(q)
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("地板内重复查询不应回源, SELECT %d → %d", after1, got)
	}
	if len(r2.Messages) != 2 {
		t.Fatalf("地板内查询应复用最近结果(2 封), got %d", len(r2.Messages))
	}

	// 超过地板: 回源。
	time.Sleep(400 * time.Millisecond)
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatal("超过地板后查询应回源")
	}
}

// 「刷新」(force) 不受节流地板约束: 地板内仍无条件回源。
func TestInboxForceNotThrottledByFloor(t *testing.T) {
	prev := inboxRefreshFloor
	inboxRefreshFloor = 10 * time.Minute // 地板远大于测试时长
	t.Cleanup(func() { inboxRefreshFloor = prev })

	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 第一次查询: 回源并记录刷新时刻。
	q.Refresh = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	after1 := srv.SelectCount()

	// 地板内查询: 复用(不回源)。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got != after1 {
		t.Fatalf("地板内查询应复用结果, SELECT %d → %d", after1, got)
	}

	// 地板内刷新(force): 不受约束, 仍无条件回源。
	q.Refresh = false
	q.Force = true
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount(); got == after1 {
		t.Fatalf("强制刷新不受地板约束, SELECT %d → %d(未变)", after1, got)
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

// ====================================================================
// M2 反证: 强制刷新的 flight 键必须与普通读取分离
// ====================================================================
//
// 背景(独立审查存活变异): 把 ListInbox 的 flightKey 后缀分离去掉(force/
// refresh 与普通读取共用同一键)后, 全部既有测试仍通过。后果: 普通读取在飞
// 时点击「刷新」, 刷新请求被并入普通读取的 flight——它等待的是一次「可能
// 命中 IMAP 快照的旧读取」, 拿到的不是自己回源的最新结果, 「强制刷新每次
// 都真实回源」的语义被破坏。
//
// 契约: force 请求不等待在飞普通读取, 自己完成一次真实回源。
// 观测点: (1) force 在普通读取仍被阻塞时返回; (2) force 期间 SELECT 增加。
func TestInboxForceDoesNotShareFlightWithNormalRead(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "第一封", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	// 预热: 建立连接与缓存。
	if _, err := b.ListInbox(q); err != nil {
		t.Fatal(err)
	}
	// 清缓存: 普通读取必须回源。
	b.respCache().reset()
	srv.AddMessage(t, "INBOX", "b@example.com", "u@163.com", "第二封", time.Now().Add(time.Second))

	// 普通读取: 进入 fn 后阻塞(flight 条目已注册, 未占用连接池锁)。
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	inboxInsideFlightForTest = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() { inboxInsideFlightForTest = nil })

	var normalRes InboxResult
	var normalErr error
	normalDone := make(chan struct{})
	go func() {
		defer close(normalDone)
		normalRes, normalErr = b.ListInbox(q)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("普通读取未进入 fn 钩子")
	}

	// 在普通读取仍阻塞时发起强制刷新: 必须不等待、自己回源。
	base := srv.SelectCount()
	qForce := q
	qForce.Force = true
	var forceRes InboxResult
	var forceErr error
	forceDone := make(chan struct{})
	go func() {
		defer close(forceDone)
		forceRes, forceErr = b.ListInbox(qForce)
	}()
	select {
	case <-forceDone:
	case <-time.After(2 * time.Second):
		// 变异路径: force 被并入普通读取的 flight, 等其完成才返回。
		close(release)
		select {
		case <-forceDone:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("强制刷新被并入在飞普通读取的 flight(等待其完成)——flight 键未分离")
	}
	if forceErr != nil {
		t.Fatalf("强制刷新失败: %v", forceErr)
	}
	if got := srv.SelectCount() - base; got <= 0 {
		t.Fatal("强制刷新应自己回源(SELECT 增加), 实际未访问 IMAP——结果来自在飞普通读取")
	}
	if len(forceRes.Messages) != 2 {
		t.Fatalf("强制刷新应看到 2 封(含第二封), got %d", len(forceRes.Messages))
	}

	// 放行普通读取: 它必须复用刷新写入的缓存(不回源), 不得把旧数据当新结果。
	srv.AddMessage(t, "INBOX", "c@example.com", "u@163.com", "第三封", time.Now().Add(2*time.Second))
	afterForce := srv.SelectCount()
	close(release)
	select {
	case <-normalDone:
	case <-time.After(3 * time.Second):
		t.Fatal("放行后普通读取未返回")
	}
	if normalErr != nil {
		t.Fatalf("普通读取失败: %v", normalErr)
	}
	if got := srv.SelectCount(); got != afterForce {
		t.Fatalf("普通读取应命中刷新写入的缓存(不回源), SELECT %d → %d", afterForce, got)
	}
	if len(normalRes.Messages) != 2 {
		t.Fatalf("普通读取应返回缓存(2 封: 含第二封、不含第三封), got %d", len(normalRes.Messages))
	}
}

// ====================================================================
// M8 反证: singleflight 内的二次缓存检查
// ====================================================================
//
// 背景(独立审查存活变异): 删掉 ListInbox 的 fn 内「二次缓存检查」后, 全部
// 既有测试仍通过。该检查覆盖的场景: 请求通过 TTL 预检(未命中)后、回源前,
// 并发的显式刷新(或其它写入方)已完成并把结果写入缓存——此时必须直接复用
// 缓存, 不得再做一次多余回源(对外部收件邮箱是白扫一次箱)。
//
// 契约: 等待期间他人写入的缓存结果必须被本次读取复用(返回缓存值), 且不
// 触发额外回源。观测点: 返回值 = 手工写入的标记; SELECT 计数不变。
func TestInboxWaiterRecheckServesConcurrentlyWrittenCache(t *testing.T) {
	b, srv, accountID := newCacheTestBackend(t)
	srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", time.Now())

	q := InboxQuery{AccountID: accountID, Limit: 20}
	if _, err := b.ListInbox(q); err != nil { // 预热连接
		t.Fatal(err)
	}
	b.respCache().reset()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	inboxInsideFlightForTest = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() { inboxInsideFlightForTest = nil })

	// A: 领读者, 阻塞在 fn 内(二次缓存检查尚未执行)。
	var resA InboxResult
	var errA error
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		resA, errA = b.ListInbox(q)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("请求 A 未进入 fn 钩子")
	}

	// B: 同键请求, 作为等待者加入 A 的 flight(不得提前返回)。
	var resB InboxResult
	var errB error
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		resB, errB = b.ListInbox(q)
	}()
	select {
	case <-doneB:
		t.Fatal("请求 B 不应在 A 完成前返回(应作为等待者阻塞)")
	case <-time.After(150 * time.Millisecond):
	}

	// 「等待期间他人写缓存」: 手工写入标记值(模拟并发显式刷新已完成)。
	cacheKey := inboxCacheKey(q)
	marker := InboxResult{
		AccountID: accountID,
		Count:     1,
		Total:     42,
		Messages:  []mail.Message{{ID: "999", Subject: "来自并发的显式刷新"}},
		Method:    "imap",
	}
	b.respCache().set(cacheKey, marker, inboxCacheTTL)

	base := srv.SelectCount()
	close(release)

	// A 的二次检查必须命中缓存: 返回标记值, 不回源。
	select {
	case <-doneA:
	case <-time.After(3 * time.Second):
		t.Fatal("放行后请求 A 未返回")
	}
	if errA != nil {
		t.Fatalf("请求 A 失败: %v", errA)
	}
	if len(resA.Messages) != 1 || resA.Total != 42 || resA.Messages[0].ID != "999" {
		t.Fatalf("请求 A 应复用等待期间写入的缓存(Total=42/ID=999), got total=%d msgs=%+v", resA.Total, resA.Messages)
	}

	// B(等待者)共享 A 的结果。
	select {
	case <-doneB:
	case <-time.After(3 * time.Second):
		t.Fatal("请求 B 未返回")
	}
	if errB != nil {
		t.Fatalf("请求 B 失败: %v", errB)
	}
	if resB.Total != 42 {
		t.Fatalf("请求 B 应共享缓存结果(Total=42), got %d", resB.Total)
	}
	if got := srv.SelectCount(); got != base {
		t.Fatalf("二次缓存检查命中后不应回源, SELECT %d → %d", base, got)
	}
}
