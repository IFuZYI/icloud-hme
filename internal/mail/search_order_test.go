package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// ====================================================================
// SEARCH 结果顺序容错: iCloud 的 SEARCH 返回「分段乱序」, 不能假设升序
// ====================================================================
//
// 回归背景(用户实测): iCloud imap.mail.me.com 的 UID SEARCH 返回顺序不是
// 升序, 而是分段乱序——诊断实测 SINCE/BEFORE 搜索返回
// [2048..2189] + [1108..2047](新段在前、旧段在后, 一次逆序)。
// 旧实现直接把数组「尾部」当最新取尾 N 个, 实际取到的是分段点附近的旧邮件,
// 导致收件箱看不到最新几天的邮件(Web API 路径按时间返回, 所以用户对比发现
// 「Web 能取到最新, IMAP 不能」)。
//
// 修复契约: 所有「取尾部 = 最新」的分页逻辑必须先对 SEARCH 结果按 UID 升序
// 排序。以下测试用分段乱序的假服务器钉住该契约。

// outOfOrderMailbox 让 INBOX 的 SEARCH 结果按「分段乱序」返回:
// [后半段..., 前半段...](iCloud 实测形态)。
type outOfOrderMailbox struct {
	*memory.Mailbox
}

func (m *outOfOrderMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	ids, err := m.Mailbox.SearchMessages(uid, criteria)
	if err != nil || !uid || len(ids) < 2 {
		return ids, err
	}
	mid := len(ids) / 2
	out := make([]uint32, 0, len(ids))
	out = append(out, ids[mid:]...)
	out = append(out, ids[:mid]...)
	return out, nil
}

// outOfOrderUser 把 INBOX 换成乱序包装, 其余文件夹委托原实现。
type outOfOrderUser struct {
	*memory.User
	inbox *outOfOrderMailbox
}

func (u *outOfOrderUser) GetMailbox(name string) (backend.Mailbox, error) {
	if strings.EqualFold(name, "INBOX") {
		return u.inbox, nil
	}
	return u.User.GetMailbox(name)
}

// outOfOrderBackend 是注入乱序邮箱的 memory 后端包装。
type outOfOrderBackend struct {
	*memory.Backend
	user *outOfOrderUser
}

func (b *outOfOrderBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	if username != "username" || password != "password" {
		return nil, backend.ErrInvalidCredentials
	}
	return b.user, nil
}

// newOutOfOrderSearchServer 起一个「SEARCH 返回分段乱序」的内存 IMAP 服务器,
// 写入 n 封邮件(UID 1..n, 时间随 UID 递增; 主题「邮件 00」..「邮件 n-1」)。
// 返回已登录的 Client。
//
// 注: InsecureSkipVerify 仅用于本进程内刚生成的自签证书测试服务器
// (127.0.0.1 上的内存后端, 不经过任何真实网络), 与 junk_folder_test.go /
// recipient_total_e2e_test.go 的既有测试基建同一模式。
func newOutOfOrderSearchServer(t *testing.T, n int) *Client {
	t.Helper()
	cert := genSelfSignedCert(t)

	be := memory.New()
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	mbox, err := user.GetMailbox("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	memBox, ok := mbox.(*memory.Mailbox)
	if !ok {
		t.Fatalf("memory mailbox 类型异常: %T", mbox)
	}
	memBox.Messages = nil // 清掉预置邮件, 保证 UID 从 1 开始
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		when := base.Add(time.Duration(i) * time.Hour)
		raw := fmt.Sprintf("From: sender%d@example.com\r\nTo: alias@icloud.com\r\nSubject: 邮件 %02d\r\nDate: %s\r\nMessage-ID: <%d@test>\r\n\r\nbody %d",
			i, i, when.Format(time.RFC1123Z), i, i)
		if err := memBox.CreateMessage([]string{}, when, strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}

	wrapped := &outOfOrderMailbox{Mailbox: memBox}
	wUser := &outOfOrderUser{User: user.(*memory.User), inbox: wrapped}
	wBe := &outOfOrderBackend{Backend: be, user: wUser}

	srv := server.New(wBe)
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
	return &Client{cli: cli}
}

// 列表路径: 乱序 SEARCH 结果下必须仍返回真正最新的 N 封。
func TestListInboxPageRangeHandlesOutOfOrderSearchResults(t *testing.T) {
	const n = 30
	c := newOutOfOrderSearchServer(t, n)

	msgs, total, err := c.ListInboxPageRange(10, 0, DateRange{})
	if err != nil {
		t.Fatalf("ListInboxPageRange: %v", err)
	}
	if total != n {
		t.Fatalf("total = %d, want %d", total, n)
	}
	if len(msgs) != 10 {
		t.Fatalf("本页 = %d 封, want 10", len(msgs))
	}
	// 最新 10 封 = UID 21..30(主题「邮件 20」..「邮件 29」), 新→旧排序。
	if msgs[0].Subject != "邮件 29" {
		t.Fatalf("第一封 = %q, want「邮件 29」(乱序 SEARCH 结果必须先排序再取尾)", msgs[0].Subject)
	}
	if msgs[9].Subject != "邮件 20" {
		t.Fatalf("最后一封 = %q, want「邮件 20」", msgs[9].Subject)
	}
}

// 第二页: offset=10 应取到次新的 10 封, 而不是更旧的。
func TestListInboxPageRangeSecondPageWithOutOfOrderResults(t *testing.T) {
	const n = 30
	c := newOutOfOrderSearchServer(t, n)

	msgs, total, err := c.ListInboxPageRange(10, 10, DateRange{})
	if err != nil {
		t.Fatalf("ListInboxPageRange: %v", err)
	}
	if total != n || len(msgs) != 10 {
		t.Fatalf("第二页 = %d 封/total %d, want 10/%d", len(msgs), total, n)
	}
	if msgs[0].Subject != "邮件 19" {
		t.Fatalf("第二页第一封 = %q, want「邮件 19」", msgs[0].Subject)
	}
	if msgs[9].Subject != "邮件 10" {
		t.Fatalf("第二页最后一封 = %q, want「邮件 10」", msgs[9].Subject)
	}
}

// 别名筛选路径: 同样的取尾分页逻辑, 必须同样免疫乱序。
func TestFindByRecipientRangeHandlesOutOfOrderSearchResults(t *testing.T) {
	const n = 30
	c := newOutOfOrderSearchServer(t, n)

	msgs, total, err := c.FindByRecipientRange("alias@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatalf("FindByRecipientRange: %v", err)
	}
	if total != n {
		t.Fatalf("total = %d, want %d", total, n)
	}
	if len(msgs) != 10 {
		t.Fatalf("本页 = %d 封, want 10", len(msgs))
	}
	if msgs[0].Subject != "邮件 29" {
		t.Fatalf("第一封 = %q, want「邮件 29」(乱序 SEARCH 结果必须先排序再取尾)", msgs[0].Subject)
	}
	if msgs[9].Subject != "邮件 20" {
		t.Fatalf("最后一封 = %q, want「邮件 20」", msgs[9].Subject)
	}
}
