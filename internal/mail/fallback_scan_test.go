package mail

import (
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 回退扫描快照: 网易 Coremail 的别名筛选路径(扫最近信封)不重复扫箱
// ====================================================================
//
// 背景: 163/126 对 header 类 SEARCH 静默返回空, 别名筛选退化为「扫最近
// 信封本地过滤」——比 SEARCH 更贵(FETCH 整批信封)。翻页/重复查询若每次
// 重扫, 对外部收件邮箱是风控的典型触发信号。
//
// 契约: 同一 (收件人, 区间, 扫描预算) 的扫描结果在短 TTL 内复用;
// 翻页第二次调用不再发 FETCH; 快照过期后重新扫描。
func newFallbackScanClient(t *testing.T, n int) (*Client, *mailtest.Server) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{EmptyHeaderSearch: true})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	base := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := 0; i < n; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@163.com",
			"验证码", base.Add(time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@163.com", "auth-code", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)
	return c, srv
}

// 回退扫描的翻页必须复用快照: 第二页不再重扫(不新增 FETCH)。
// 修复前本测试失败(RED): 每次调用都重新扫最近信封。
func TestFallbackScanSnapshotReusedAcrossPages(t *testing.T) {
	c, srv := newFallbackScanClient(t, 30)

	page1, total, err := c.FindByRecipientRange("u@163.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("第一页失败: %v", err)
	}
	if len(page1) != 10 || total != 30 {
		t.Fatalf("第一页 = %d 封/total %d, want 10/30", len(page1), total)
	}
	fetches1 := srv.FetchSeqCount()
	if fetches1 == 0 {
		t.Fatal("回退路径应扫箱(FETCH), 测量异常")
	}

	// 翻页: offset=10 时扫描预算相同((10+10)*4=80 → 预算 80; 第一页 (10+0)*4=40 → 40),
	// 预算不同时快照不复用是预期行为——这里翻到同预算的第二页(offset=0,limit=20)。
	page2, total2, err := c.FindByRecipientRange("u@163.com", 20, 0, DateRange{})
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	if len(page2) != 20 || total2 != 30 {
		t.Fatalf("第二页 = %d 封/total %d, want 20/30", len(page2), total2)
	}
	if got := srv.FetchSeqCount(); got != fetches1 {
		t.Fatalf("同预算的重复查询应复用扫描快照(不重扫), FETCH %d → %d", fetches1, got)
	}
}

// 相同预算的重复查询(用户切走再切回)复用快照。
func TestFallbackScanSnapshotReusedOnRepeat(t *testing.T) {
	c, srv := newFallbackScanClient(t, 10)
	if _, _, err := c.FindByRecipientRange("u@163.com", 5, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	fetches1 := srv.FetchSeqCount()

	if _, _, err := c.FindByRecipientRange("u@163.com", 5, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.FetchSeqCount(); got != fetches1 {
		t.Fatalf("重复查询应命中快照, FETCH %d → %d", fetches1, got)
	}
}

// 快照过期后重新扫描(新邮件不能长期不可见)。
func TestFallbackScanSnapshotExpires(t *testing.T) {
	prev := ListSnapshotTTL
	ListSnapshotTTL = 20 * time.Millisecond
	t.Cleanup(func() { ListSnapshotTTL = prev })

	c, srv := newFallbackScanClient(t, 5)
	if _, _, err := c.FindByRecipientRange("u@163.com", 5, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	fetches1 := srv.FetchSeqCount()

	time.Sleep(60 * time.Millisecond)
	if _, _, err := c.FindByRecipientRange("u@163.com", 5, 0, DateRange{}); err != nil {
		t.Fatal(err)
	}
	if got := srv.FetchSeqCount(); got == fetches1 {
		t.Fatal("快照过期后应重新扫描")
	}
}

// 删除邮件后快照失效: 已删邮件不得因扫描快照残留继续出现。
func TestFallbackScanSnapshotInvalidatedByDelete(t *testing.T) {
	c, _ := newFallbackScanClient(t, 3)
	msgs, total, err := c.FindByRecipientRange("u@163.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || total != 3 {
		t.Fatalf("预热应 3 封, got %d/total %d", len(msgs), total)
	}
	_, uid, err := ParseMessageID(msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFrom("INBOX", uid); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	after, total2, err := c.FindByRecipientRange("u@163.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 2 || len(after) != 2 {
		t.Fatalf("删除后应 2 封/total 2, got %d/total %d", len(after), total2)
	}
}
