package mail

import (
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
