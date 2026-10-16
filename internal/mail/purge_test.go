package mail

import (
	"testing"
	"time"

	"icloud-hme/internal/mailtest"
)

// ====================================================================
// 批量删除原语: PurgeRange
// ====================================================================
//
// 契约(风控设计):
//   - 删除按「块」执行: 每块 1×UID STORE(+FLAGS \Deleted) + 1×EXPUNGE,
//     而不是逐封 2 次往返——N 封的成本从 2N 压到 ~2×(N/chunk)+SEARCH;
//   - 连续 UID 合并为 seqset 区间(协议层自动压缩);
//   - 返回各文件夹的删除计数, 供任务进度统计;
//   - 只处理指定时间区间内的邮件(SEARCH 按日期条件);
//   - 空结果不产生任何 STORE/EXPUNGE。

func newPurgeClient(t *testing.T, n int, folder string) (*Client, *mailtest.Server) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{JunkFolder: "Junk"})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	base := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := 0; i < n; i++ {
		srv.AddMessage(t, folder, "a@example.com", "u@icloud.com",
			"旧邮件", base.Add(time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@icloud.com", "app-password", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)
	return c, srv
}

// 全区间删除: 所有邮件被删, 且命令数受块大小约束。
func TestPurgeRangeDeletesAllInRange(t *testing.T) {
	c, _ := newPurgeClient(t, 12, "INBOX")

	// 区间覆盖全部(从很早到很晚)。
	r := DateRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(time.Hour),
	}
	got, err := c.PurgeRange("INBOX", r, 5) // chunk=5 → 12 封 = 3 块
	if err != nil {
		t.Fatalf("PurgeRange: %v", err)
	}
	if got.Deleted != 12 {
		t.Fatalf("删除数 = %d, want 12", got.Deleted)
	}

	// 验证邮件确实没了。
	uids, err := c.folderUIDsRange("INBOX", "", DateRange{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uids) != 0 {
		t.Fatalf("删除后应无邮件, 实际 %d", len(uids))
	}
}

// 只删区间内的: 区间外的邮件保留。
//
// 注意: IMAP SEARCH 是「日粒度」(RFC 3501, 见 daterange.go), 同一天内
// 无法按时间点区分——清理的自然粒度是「天」。本测试用跨天分布验证。
func TestPurgeRangeOnlyDeletesInRange(t *testing.T) {
	t.Helper()
	srv := mailtest.NewServer(t, mailtest.Options{})
	t.Cleanup(OverrideDialForTest(mailtest.DialInsecure))

	now := time.Now()
	// 5 封在 10 天前(旧), 5 封在 1 天前(新)——跨天分布。
	for i := 0; i < 5; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@icloud.com",
			"旧邮件", now.AddDate(0, 0, -10).Add(time.Duration(i)*time.Minute))
	}
	for i := 0; i < 5; i++ {
		srv.AddMessage(t, "INBOX", "a@example.com", "u@icloud.com",
			"新邮件", now.AddDate(0, 0, -1).Add(time.Duration(i)*time.Minute))
	}
	c := NewClientWithServer("u@icloud.com", "app-password", srv.Host, srv.Port)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Disconnect)

	// 区间: 30 天前 ~ 5 天前(按天粒度, 只覆盖 10 天前的旧邮件)。
	r := DateRange{
		Start: now.AddDate(0, 0, -30),
		End:   now.AddDate(0, 0, -5),
	}
	got, err := c.PurgeRange("INBOX", r, 500)
	if err != nil {
		t.Fatalf("PurgeRange: %v", err)
	}
	if got.Deleted != 5 {
		t.Fatalf("区间内删除数 = %d, want 5", got.Deleted)
	}
	uids, err := c.folderUIDsRange("INBOX", "", DateRange{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uids) != 5 {
		t.Fatalf("区间外应保留 5 封, 实际 %d", len(uids))
	}
}

// 块大小约束命令数: 12 封 chunk=5 → STORE 3 次、EXPUNGE 3 次(而非 12 次)。
func TestPurgeRangeChunksCommands(t *testing.T) {
	c, srv := newPurgeClient(t, 12, "INBOX")
	r := DateRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(time.Hour),
	}
	before := srv.StoreCount()
	if _, err := c.PurgeRange("INBOX", r, 5); err != nil {
		t.Fatalf("PurgeRange: %v", err)
	}
	stores := srv.StoreCount() - before
	if stores != 3 {
		t.Fatalf("STORE 次数 = %d, want 3 (12 封 / 块 5 = 3 块)", stores)
	}
	expunges := srv.ExpungeCount()
	if expunges != 3 {
		t.Fatalf("EXPUNGE 次数 = %d, want 3", expunges)
	}
}

// 空区间: 无 STORE/EXPUNGE(不打空操作)。
func TestPurgeRangeEmptyDoesNothing(t *testing.T) {
	c, srv := newPurgeClient(t, 3, "INBOX")
	r := DateRange{
		Start: time.Now().Add(-100 * time.Hour),
		End:   time.Now().Add(-99 * time.Hour), // 无邮件
	}
	beforeStore, beforeExpunge := srv.StoreCount(), srv.ExpungeCount()
	got, err := c.PurgeRange("INBOX", r, 500)
	if err != nil {
		t.Fatalf("PurgeRange: %v", err)
	}
	if got.Deleted != 0 {
		t.Fatalf("删除数 = %d, want 0", got.Deleted)
	}
	if srv.StoreCount() != beforeStore || srv.ExpungeCount() != beforeExpunge {
		t.Fatal("空区间不应产生 STORE/EXPUNGE")
	}
}

// 缺失文件夹(如 iCloud 无「垃圾邮件」): 视为 0 封, 不报错。
func TestPurgeRangeMissingFolderIsZero(t *testing.T) {
	c, _ := newPurgeClient(t, 3, "INBOX")
	r := DateRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(time.Hour),
	}
	got, err := c.PurgeRange("不存在的文件夹", r, 500)
	if err != nil {
		t.Fatalf("缺失文件夹不应报错: %v", err)
	}
	if got.Deleted != 0 {
		t.Fatalf("缺失文件夹删除数 = %d, want 0", got.Deleted)
	}
}

// 快照失效: 删除后列表快照被清, 后续查询回源(不残留已删 UID)。
func TestPurgeRangeInvalidatesSnapshot(t *testing.T) {
	c, _ := newPurgeClient(t, 5, "INBOX")
	r := DateRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(time.Hour),
	}
	// 先建立快照。
	if _, err := c.folderUIDsRange("INBOX", "", DateRange{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PurgeRange("INBOX", r, 500); err != nil {
		t.Fatal(err)
	}
	uids, err := c.folderUIDsRange("INBOX", "", DateRange{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uids) != 0 {
		t.Fatalf("删除后快照应失效, 实际仍见 %d 封", len(uids))
	}
}
