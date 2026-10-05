package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// newE2EServerWithBackend 起一个内存 IMAP 服务器(含 Junk 文件夹),
// 返回已登录的 Client 与后端(便于直接投放邮件)。
func newE2EServerWithBackend(t *testing.T) (*Client, *memory.Backend) {
	t.Helper()
	cert := genSelfSignedCert(t)
	be := memory.New()
	// memory.New() 预置了一封 2016 年的 INBOX 邮件; 清掉以保证测试可控。
	if user, err := be.Login(nil, "username", "password"); err == nil {
		if mb, err := user.GetMailbox("INBOX"); err == nil {
			if mm, ok := mb.(*memory.Mailbox); ok {
				mm.Messages = nil
			}
		}
	}
	ensureJunk(t, be)

	srv := server.New(be)
	srv.AllowInsecureAuth = true
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	conn, err := tls.Dial("tcp", "127.0.0.1:"+portStr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Login("username", "password"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Logout() })
	return &Client{cli: cli}, be
}

// addMessageTo 往指定文件夹写入一封邮件(测试辅助)。
func addMessageTo(t *testing.T, be *memory.Backend, folder, from, to, subject string, when time.Time) {
	t.Helper()
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	mbox, err := user.GetMailbox(folder)
	if err != nil {
		t.Fatalf("文件夹 %s 不存在: %v", folder, err)
	}
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%d@test>\r\n\r\n正文 %s",
		from, to, subject, when.Format(time.RFC1123Z), when.UnixNano(), subject)
	if err := mbox.CreateMessage([]string{}, when, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

// 默认内存后端只有 INBOX; 补一个 Junk 文件夹(模拟 iCloud 的垃圾邮件箱)。
func ensureJunk(t *testing.T, be *memory.Backend) {
	t.Helper()
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := user.CreateMailbox("Junk"); err != nil {
		t.Fatal(err)
	}
}

// HME 转发邮件常被 iCloud 判为垃圾邮件: 列表必须同时扫描 INBOX 与 Junk,
// 否则验证码邮件在收件箱里根本看不到。
func TestListInboxScansJunkFolder(t *testing.T) {
	c, be := newE2EServerWithBackend(t)

	now := time.Now()
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "收件箱邮件", now.Add(-2*time.Minute))
	addMessageTo(t, be, "Junk", "b@example.com", "alias@icloud.com", "垃圾邮件里的验证码", now.Add(-1*time.Minute))

	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("ListInboxPageRange: %v", err)
	}
	if total != 2 || len(msgs) != 2 {
		t.Fatalf("应合并两个文件夹共 2 封, got %d/total %d", len(msgs), total)
	}
	// 新→旧排序: Junk 里的更新, 排第一。
	if msgs[0].Subject != "垃圾邮件里的验证码" {
		t.Fatalf("排序应为新→旧, got 第一封 %q", msgs[0].Subject)
	}
	// Folder 字段标出来源, 供正文读取/删除定位。
	if msgs[0].Folder != "Junk" || msgs[1].Folder != "INBOX" {
		t.Fatalf("Folder 标注错误: %q / %q", msgs[0].Folder, msgs[1].Folder)
	}
}

// 没有 Junk 文件夹的账号不应报错(直接只读 INBOX)。
func TestListInboxWithoutJunkFolderStillWorks(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "普通邮件", time.Now())

	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("无 Junk 文件夹不应报错: %v", err)
	}
	if total != 1 || len(msgs) != 1 {
		t.Fatalf("应只读到 INBOX 的 1 封, got %d/total %d", len(msgs), total)
	}
}

// 跨文件夹分页: 总数与翻页窗口在合并后的列表上计算。
func TestListInboxAcrossFoldersPaginates(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	now := time.Now()
	// INBOX 3 封(较旧), Junk 2 封(较新)。
	for i := 0; i < 3; i++ {
		addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", fmt.Sprintf("收件箱 %d", i), now.Add(-time.Duration(10+i)*time.Minute))
	}
	for i := 0; i < 2; i++ {
		addMessageTo(t, be, "Junk", "b@example.com", "alias@icloud.com", fmt.Sprintf("垃圾 %d", i), now.Add(-time.Duration(i+1)*time.Minute))
	}

	page1, total, err := c.ListInboxPageRange(2, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(page1) != 2 {
		t.Fatalf("第一页 = %d 封/total %d, want 2/5", len(page1), total)
	}
	if page1[0].Folder != "Junk" || page1[1].Folder != "Junk" {
		t.Fatalf("最新的两封都应来自 Junk: %+v", page1)
	}

	page2, total2, err := c.ListInboxPageRange(2, 2, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 5 || len(page2) != 2 {
		t.Fatalf("第二页 = %d 封/total %d, want 2/5", len(page2), total2)
	}
	if page2[0].Folder != "INBOX" || page2[1].Folder != "INBOX" {
		t.Fatalf("第二页应跨到 INBOX: %+v", page2)
	}
}

// 按别名筛选同样必须覆盖 Junk。
func TestFindByRecipientScansJunk(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	now := time.Now()
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "收件箱", now.Add(-3*time.Minute))
	addMessageTo(t, be, "Junk", "b@example.com", "alias@icloud.com", "垃圾箱验证码", now.Add(-1*time.Minute))
	addMessageTo(t, be, "INBOX", "c@example.com", "other@icloud.com", "别人的邮件", now)

	msgs, total, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(msgs) != 2 {
		t.Fatalf("别名筛选应命中 INBOX+Junk 共 2 封, got %d/total %d", len(msgs), total)
	}
	if msgs[0].Subject != "垃圾箱验证码" {
		t.Fatalf("新→旧排序错误: %q", msgs[0].Subject)
	}
}

// 正文读取按 folder 定位: Junk 里的 UID 与 INBOX 不同, 不带 folder 会读错邮件。
func TestGetFullFromFolder(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "收件箱正文", time.Now())
	addMessageTo(t, be, "Junk", "b@example.com", "alias@icloud.com", "垃圾箱正文", time.Now())

	// 先在 Junk 中拿到 UID。
	msgs, _, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	var junkMsg Message
	for _, m := range msgs {
		if m.Folder == "Junk" {
			junkMsg = m
		}
	}
	if junkMsg.ID == "" {
		t.Fatal("未找到 Junk 中的邮件")
	}

	_, uid, err := ParseMessageID(junkMsg.ID)
	if err != nil {
		t.Fatalf("解析 %q: %v", junkMsg.ID, err)
	}
	full, err := c.GetFullFrom("Junk", uid)
	if err != nil {
		t.Fatalf("GetFullFrom(Junk): %v", err)
	}
	if !strings.Contains(full.Body, "垃圾箱正文") {
		t.Fatalf("正文应来自 Junk, got %q", full.Body)
	}
}

// 删除同样按 folder 定位。
func TestDeleteFromFolder(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	addMessageTo(t, be, "Junk", "b@example.com", "alias@icloud.com", "待删除", time.Now())

	msgs, _, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Folder != "Junk" {
		t.Fatalf("准备数据失败: %+v", msgs)
	}
	_, delUID, err := ParseMessageID(msgs[0].ID)
	if err != nil {
		t.Fatalf("解析 %q: %v", msgs[0].ID, err)
	}
	if err := c.DeleteFrom("Junk", delUID); err != nil {
		t.Fatalf("DeleteFrom(Junk): %v", err)
	}
	after, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(after) != 0 {
		t.Fatalf("删除后应为空, got %d/total %d", len(after), total)
	}
}

// GetFull 不带 folder 时默认 INBOX, 保持旧调用方兼容。
func TestGetFullDefaultsToInbox(t *testing.T) {
	c, be := newE2EServerWithBackend(t)
	addMessageTo(t, be, "INBOX", "a@example.com", "alias@icloud.com", "默认路径", time.Now())

	msgs, _, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	_, uid, err := ParseMessageID(msgs[0].ID)
	if err != nil {
		t.Fatalf("解析 %q: %v", msgs[0].ID, err)
	}
	full, err := c.GetFull(uid)
	if err != nil {
		t.Fatalf("GetFull: %v", err)
	}
	if !strings.Contains(full.Body, "默认路径") {
		t.Fatalf("默认应读 INBOX, got %q", full.Body)
	}
}
