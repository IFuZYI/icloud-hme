package mail

import (
	"net"
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// ====================================================================
// UID 快照: 翻页/重复查询不重复全量 SEARCH
// ====================================================================
//
// 背景: 翻页(load more)在服务端缓存键中因 offset 不同而 miss, 每次都
// 重新对全部候选夹做 UID SEARCH。对 163 等外部收件邮箱, 高频扫箱是
// 风控的典型触发信号(见 internal/server/cache.go 的同类说明)。
//
// 契约: 同一 (folder, 日期区间) 的 UID 集合在短 TTL 内复用; 翻页第二次
// 调用不再发 SEARCH; TTL 过期后重新 SEARCH; 删除邮件/显式失效后重新 SEARCH。

// newSnapshotTestClient 起一个进程内 IMAP 服务器并写入 n 封邮件, 返回
// 已连接的 Client 与服务器(命令计数断言用)。
//
// 时间随 UID 递增(UID 1 最旧): 与真实 IMAP 一致——UID 按到达顺序分配,
// 「UID 升序 ≈ 时间升序」是生产分页逻辑的前提。
func newSnapshotTestClient(t *testing.T, n int) (*Client, *mailtest.Server) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	base := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := 0; i < n; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com",
			"邮件", base.Add(time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)
	return c, srv
}

// 翻页(第二页)必须复用第一页的 UID 快照: 不再重复 SEARCH。
// 修复前本测试失败(RED): 每次调用都重新 SEARCH 全部候选夹。
func TestListInboxPageRangeReusesUIDSnapshot(t *testing.T) {
	c, srv := newSnapshotTestClient(t, 30)

	page1, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("第一页失败: %v", err)
	}
	if len(page1) != 10 || total != 30 {
		t.Fatalf("第一页 = %d 封/total %d, want 10/30", len(page1), total)
	}
	searches1 := srv.SearchCount()

	page2, total2, err := c.ListInboxPageRange(10, 10, DateRange{})
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	if len(page2) != 10 || total2 != 30 {
		t.Fatalf("第二页 = %d 封/total %d, want 10/30", len(page2), total2)
	}
	if got := srv.SearchCount(); got != searches1 {
		t.Fatalf("翻页应复用 UID 快照(不重复 SEARCH), 实际 SEARCH %d → %d", searches1, got)
	}
	// 分页内容仍必须正确(快照不改变结果)。
	if page1[0].ID == page2[0].ID {
		t.Fatalf("两页首封不应相同: %q", page1[0].ID)
	}
}

// 快照过期后必须重新 SEARCH(新邮件不能长期不可见)。
func TestListInboxSnapshotExpires(t *testing.T) {
	prev := ListSnapshotTTL
	ListSnapshotTTL = 20 * time.Millisecond
	t.Cleanup(func() { ListSnapshotTTL = prev })

	c, srv := newSnapshotTestClient(t, 5)
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	searches1 := srv.SearchCount()

	time.Sleep(60 * time.Millisecond)
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SearchCount(); got == searches1 {
		t.Fatal("快照过期后应重新 SEARCH")
	}
}

// 显式失效(刷新按钮路径)后必须重新 SEARCH。
func TestInvalidateListSnapshotsForcesSearch(t *testing.T) {
	c, srv := newSnapshotTestClient(t, 5)
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	searches1 := srv.SearchCount()

	c.InvalidateListSnapshots()
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SearchCount(); got == searches1 {
		t.Fatal("显式失效后应重新 SEARCH")
	}
}

// 删除邮件后快照失效: 已删邮件不得因快照残留继续出现在列表里。
func TestDeleteFromInvalidatesListSnapshot(t *testing.T) {
	c, _ := newSnapshotTestClient(t, 3)
	msgs, _, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("预热应 3 封, got %d", len(msgs))
	}
	_, uid, err := ParseMessageID(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFrom("INBOX", uid); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	after, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(after) != 2 {
		t.Fatalf("删除后应 2 封/total 2(快照未失效会让 total 残留 3), got %d/total %d", len(after), total)
	}
}

// 别名搜索路径同样复用快照(翻页不重复 SEARCH)。
func TestFindByRecipientReusesSnapshot(t *testing.T) {
	c, srv := newSnapshotTestClient(t, 30)
	// 全部邮件都发给该别名。
	msgs, total, err := c.FindByRecipientRange("u@163.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("别名查询失败: %v", err)
	}
	if len(msgs) != 10 || total != 30 {
		t.Fatalf("= %d 封/total %d, want 10/30", len(msgs), total)
	}
	searches1 := srv.SearchCount()

	if _, total2, err := c.FindByRecipientRange("u@163.com", 10, 10, DateRange{}); err != nil {
		t.Fatal(err)
	} else if total2 != 30 {
		t.Fatalf("第二页 total = %d, want 30", total2)
	}
	if got := srv.SearchCount(); got != searches1 {
		t.Fatalf("别名翻页应复用快照, SEARCH %d → %d", searches1, got)
	}
}

// ====================================================================
// UIDVALIDITY 变化: 邮箱被服务端重建后旧 UID 不再指向同一邮件
// ====================================================================
//
// 契约: 检测到某文件夹 UIDVALIDITY 变化时
//  1. 客户端快照立即作废(旧 UID 列表不再可用);
//  2. 暴露一次性变化标记(TakeUidValidityChange)供上层失效其读缓存;
//  3. 首次观测不算变化(避免正常首连误报)。
func TestUidValidityChangeDetectedAndInvalidatesSnapshot(t *testing.T) {
	prev := ListSnapshotTTL
	ListSnapshotTTL = 0 // 关闭快照, 每次调用都真实 SELECT
	t.Cleanup(func() { ListSnapshotTTL = prev })

	srv := mailtest.NewServer(t, mailtest.Options{UidValidity: 100})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))
	now := time.Now()
	for i := 0; i < 3; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", now.Add(-time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)

	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if c.TakeUidValidityChange() {
		t.Fatal("首次观测不应报告 UIDVALIDITY 变化")
	}

	srv.SetUidValidity(200)
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if !c.TakeUidValidityChange() {
		t.Fatal("UIDVALIDITY 变化未被检测到")
	}
	if c.TakeUidValidityChange() {
		t.Fatal("变化标记应是一次性的(读取后清除)")
	}
}

// UIDVALIDITY 变化必须清空列表快照: 变化后的下一次列表读取不得复用旧 UID 集合。
//
// 回归背景(独立审查存活变异 M3 的反证): 既有测试
// TestUidValidityChangeDetectedAndInvalidatesSnapshot 把 ListSnapshotTTL 置 0
// (快照关闭), 只覆盖「一次性变化标记」; 把 noteUidValidity 里的
// clearSnapshotsLocked() 拿掉, 全部测试仍能通过——「清空快照」这一步此前
// 没有测试保护。邮箱被服务端重建后旧 UID 全部作废, 若快照残留, 列表会继续
// 展示/操作已不存在的邮件。
//
// 契约: 观测到 UIDVALIDITY 变化后, 下一次列表读取必须重新 SELECT/SEARCH,
// 而不是命中旧快照。
//
// 计数口径: 列表读取的 SELECT 有两处——SEARCH 前的 SELECT(快照命中时跳过)与
// 拉信封前的 SELECT(每次调用必有, 见 fetchEnvelopesForCandidates)。因此
// 「快照命中」的那次调用 SELECT 增量仍为 1, 不能断言零增量; 判定依据是
// 「变化后的增量必须大于快照命中的增量」(重新 SEARCH + 重新探测缺失文件夹)。
func TestUidValidityChangeClearsListSnapshots(t *testing.T) {
	prev := ListSnapshotTTL
	ListSnapshotTTL = time.Minute // 快照开启: 本测试验证的正是「变化时清空」这一步
	t.Cleanup(func() { ListSnapshotTTL = prev })

	srv := mailtest.NewServer(t, mailtest.Options{UidValidity: 100})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))
	base := time.Now().Add(-3 * time.Minute)
	for i := 0; i < 3; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com", "邮件", base.Add(time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)

	// 第一次: 建立快照(SEARCH 一次)。
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	s1 := srv.SelectCount()
	searches1 := srv.SearchCount()

	// 第二次: 必须快照命中(不重复 SEARCH)。前提不成立时先排查快照路径,
	// 否则下面的「清空」断言失去意义。
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SearchCount(); got != searches1 {
		t.Fatalf("前提不成立: 第二次列表应命中快照(不重复 SEARCH), SEARCH %d → %d", searches1, got)
	}
	hitDelta := srv.SelectCount() - s1

	// 模拟「任一 SELECT 观测到新 UIDVALIDITY」(邮箱被服务端重建)。
	srv.SetUidValidity(200)
	if _, err := c.selectFolder("INBOX", true); err != nil {
		t.Fatal(err)
	}
	if !c.TakeUidValidityChange() {
		t.Fatal("UIDVALIDITY 变化未被检测到")
	}

	// 变化后的列表读取: 快照已被清空, 必须重新 SELECT/SEARCH;
	// 旧快照残留时这里只会剩拉信封的一次 SELECT(增量与快照命中相同)。
	s2 := srv.SelectCount()
	searches2 := srv.SearchCount()
	if _, _, err := c.ListInboxPageRange(10, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.SelectCount() - s2; got <= hitDelta {
		t.Fatalf("UIDVALIDITY 变化后快照未清空: 列表读取 SELECT 增量 %d, 快照命中增量 %d——"+
			"旧 UID 集合被复用(邮箱重建后必须重新 SELECT/SEARCH)", got, hitDelta)
	}
	if got := srv.SearchCount(); got <= searches2 {
		t.Fatalf("UIDVALIDITY 变化后未重新 SEARCH(旧快照残留): SEARCH %d → %d", searches2, got)
	}
}

// ====================================================================
// M16 反证: listScope 的日期区间必须量化到分钟
// ====================================================================
//
// 背景(独立审查存活变异): 去掉 listScope 里 Start/End 的 Truncate(time.Minute)
// 后, 全部既有测试仍通过。后果: days 回退路径由上层用 time.Now() 生成区间,
// 纳秒级漂移让同一逻辑查询每次生成新快照键——快照永不命中, 翻页/切筛选每次
// 都重复全量 SEARCH(对外部收件邮箱是高频扫箱, 风控信号; 与 server 层缓存键
// 的量化理由相同, 见 inboxCacheKey)。
//
// 契约: 同一分钟桶内的区间(纳秒/秒漂移 <1min)命中同一快照; 跨分钟桶则 miss。
// 观测点: IMAP SEARCH 命令计数。
func TestListSnapshotKeyQuantizesDateRange(t *testing.T) {
	c, srv := newSnapshotTestClient(t, 5)

	// 干净分钟边界 + 桶内偏移, 保证「同桶/跨桶」判定不受测试运行时刻影响。
	m := time.Now().Truncate(time.Minute)

	// 第一次查询: 建立快照(Start/End 都在 m 分钟桶内)。
	r1 := DateRange{Start: m.Add(5 * time.Second), End: m.Add(55 * time.Second)}
	if _, _, err := c.ListInboxPageRange(10, 0, r1); err != nil {
		t.Fatalf("第一次查询失败: %v", err)
	}
	searches1 := srv.SearchCount()
	if searches1 == 0 {
		t.Fatal("测量异常: 第一次查询应产生 SEARCH")
	}

	// 同一分钟桶、不同秒/纳秒(相差 <1min): 必须命中同一快照, 不重复 SEARCH。
	r2 := DateRange{Start: m.Add(9 * time.Second), End: m.Add(51 * time.Second)}
	if _, _, err := c.ListInboxPageRange(10, 0, r2); err != nil {
		t.Fatalf("第二次查询失败: %v", err)
	}
	if got := srv.SearchCount(); got != searches1 {
		t.Fatalf("同分钟桶的区间(纳秒漂移 <1min)应命中同一快照, SEARCH %d → %d——"+
			"listScope 未量化日期区间", searches1, got)
	}

	// 跨分钟桶(End 漂移 >1min): 必须 miss, 重新 SEARCH。
	r3 := DateRange{Start: m.Add(5 * time.Second), End: m.Add(61 * time.Second)}
	if _, _, err := c.ListInboxPageRange(10, 0, r3); err != nil {
		t.Fatalf("第三次查询失败: %v", err)
	}
	if got := srv.SearchCount(); got == searches1 {
		t.Fatalf("跨分钟桶的区间应 miss 快照并重新 SEARCH, SEARCH 保持 %d(未变)——"+
			"量化粒度过粗或未生效", searches1)
	}
}

// ====================================================================
// SC-2: IMAP 单次读写操作的 socket deadline
// ====================================================================
//
// 背景: 上层 withIMAPTimeout 超时只是「放弃等待」, 后台 goroutine 仍可能
// 阻塞在无 deadline 的 socket 读上, 长期持有连接池锁(pc.mu), 阻塞同账号
// 全部读。修复: 新建连接设置 client.Timeout(见 client.go imapSocketTimeout)。
//
// 契约:
//  1. Connect 成功后连接带 socket deadline;
//  2. 卡死的读在 deadline 内失败(而不是无界阻塞);
//  3. deadline 必须显著大于池保活周期——go-imap 的 deadline 每条命令重设且
//     命令结束后不重置, 它同时是「空闲连接存活上限」; 若小于保活周期,
//     空闲连接会在保活到达前被读超时判死, 每个空档都触发一次 TLS+LOGIN
//     (外部邮箱风控的第一触发信号, 池设计明确要避免)。
func TestConnectSetsSocketDeadlineAndBoundsStalledRead(t *testing.T) {
	prev := imapSocketTimeout
	imapSocketTimeout = 300 * time.Millisecond
	t.Cleanup(func() { imapSocketTimeout = prev })

	// 服务器放慢 SELECT 响应(2s), 远超 deadline(300ms)。
	srv := mailtest.NewServer(t, mailtest.Options{SelectDelay: 2 * time.Second})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Disconnect()

	if c.cli.Timeout != imapSocketTimeout {
		t.Fatalf("Connect 后应设置 socket deadline = %v, got %v", imapSocketTimeout, c.cli.Timeout)
	}

	// 卡死的 SELECT: 必须在 deadline 内以错误返回, 不得无界阻塞。
	start := time.Now()
	_, _, err := c.ListInboxPageRange(10, 0, DateRange{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("卡死的 SELECT 应因 socket deadline 报错")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("卡死的读取应在 deadline(%v)内失败, 实际耗时 %v——socket deadline 未生效", imapSocketTimeout, elapsed)
	}
}

// 不变式: socket deadline(若启用)必须显著大于池保活周期, 否则空闲连接会在
// 保活到达前被读超时判死(见 TestConnectSetsSocketDeadlineAndBoundsStalledRead
// 契约 3 与 client.go imapSocketTimeout 注释)。
func TestImapSocketTimeoutExceedsKeepalive(t *testing.T) {
	if imapSocketTimeout <= 0 {
		t.Skip("socket deadline 已禁用(0), 不变式不适用")
	}
	if imapSocketTimeout <= PoolKeepaliveInterval {
		t.Fatalf("imapSocketTimeout(%v)必须 > PoolKeepaliveInterval(%v): 否则空闲连接"+
			"在保活 NOOP 到达前即被读超时判死, 每个用户空档触发一次 TLS+LOGIN",
			imapSocketTimeout, PoolKeepaliveInterval)
	}
}

// 生产拨号路径: TLS 握手/服务器问候阶段同样受 socket deadline 约束。
//
// go-imap 在 DialTLS 返回前读取服务器问候, 无法事后设置 deadline; 修复用
// 带 Timeout 的 dialer(DialWithDialerTLS)覆盖该阶段。用「接受 TCP 但从不
// 响应 TLS 握手」的假服务器验证: 拨号必须在 deadline 附近失败返回, 不得
// 无界阻塞(无界阻塞会让上层 withIMAPTimeout 的孤儿 goroutine 卡在连接建立)。
func TestDialIMAPBoundsTLSHandshakePhase(t *testing.T) {
	prev := imapSocketTimeout
	imapSocketTimeout = 300 * time.Millisecond
	t.Cleanup(func() { imapSocketTimeout = prev })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// 接受连接但从不发送任何字节(模拟握手阶段卡死的服务器)。
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	start := time.Now()
	_, err = dialIMAP(ln.Addr().String())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("对不响应 TLS 握手的服务器, 拨号应报错")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("拨号应在 socket deadline(%v)附近失败, 实际耗时 %v——握手阶段未被 deadline 约束", imapSocketTimeout, elapsed)
	}
}
