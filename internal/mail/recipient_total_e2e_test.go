package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// newMailboxE2EServer 起一个内存 IMAP 服务器并写入 n 封发给 recipient 的邮件,
// 返回已登录的 client 与端口。
func newMailboxE2EServer(t *testing.T, recipient string, n int) *Client {
	t.Helper()
	cert := genSelfSignedCert(t)

	be := memory.New()
	// 给默认用户加邮件: 用 backend.User 接口写入。
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	mbox, err := user.GetMailbox("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		raw := fmt.Sprintf("From: sender%d@example.com\r\nTo: %s\r\nSubject: 测试邮件 %d\r\nDate: %s\r\nMessage-ID: <%d@test>\r\n\r\n这是第 %d 封邮件正文",
			i, recipient, i, time.Now().Add(-time.Duration(i)*time.Minute).Format(time.RFC1123Z), i, i)
		if err := mbox.CreateMessage([]string{}, time.Now().Add(-time.Duration(i)*time.Minute), strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}

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
	return &Client{cli: cli}
}

// 按别名查询必须返回符合条件的总数(否则前端「加载更多」在别名路径上失效)。
// 历史 bug: FindByRecipientRange 的 total 恒为 0。
func TestFindByRecipientReturnsTotal(t *testing.T) {
	const recipient = "alias@icloud.com"
	c := newMailboxE2EServer(t, recipient, 5)

	msgs, total, err := c.FindByRecipientRange(recipient, 2, 0, DateRange{})
	if err != nil {
		t.Fatalf("FindByRecipientRange: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("本页应 2 封, got %d", len(msgs))
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5(别名路径的翻页依赖它)", total)
	}

	// 第二页: 剩 3 封, total 不变。
	msgs2, total2, err := c.FindByRecipientRange(recipient, 2, 2, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 2 || total2 != 5 {
		t.Fatalf("第二页 = %d 封/total %d, want 2/5", len(msgs2), total2)
	}

	// 越界页: 空列表但 total 仍正确。
	msgs3, total3, err := c.FindByRecipientRange(recipient, 2, 99, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs3) != 0 || total3 != 5 {
		t.Fatalf("越界页 = %d 封/total %d, want 0/5", len(msgs3), total3)
	}
}

// 无匹配时 total 为 0 且不报错。
func TestFindByRecipientNoMatch(t *testing.T) {
	c := newMailboxE2EServer(t, "alias@icloud.com", 3)
	msgs, total, err := c.FindByRecipientRange("nobody@icloud.com", 10, 0, DateRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 || total != 0 {
		t.Fatalf("无匹配 = %d 封/total %d, want 0/0", len(msgs), total)
	}
}

// 日期区间过滤生效: IMAP SEARCH 的日期条件是「日粒度、忽略时区」(RFC 3501),
// 因此断言用「昨天/明天」这类跨日边界, 与真实服务器语义一致。
func TestFindByRecipientTotalRespectsDateRange(t *testing.T) {
	const recipient = "alias@icloud.com"
	c := newMailboxE2EServer(t, recipient, 4)

	// 下界=昨天: 今天的 4 封全部命中。
	_, total, err := c.FindByRecipientRange(recipient, 10, 0, DateRange{Start: time.Now().AddDate(0, 0, -1)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("下界=昨天 total = %d, want 4", total)
	}

	// 下界=明天: 全部排除。
	_, total, err = c.FindByRecipientRange(recipient, 10, 0, DateRange{Start: time.Now().AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("下界=明天 total = %d, want 0", total)
	}
}

// 遍历接口按新→旧返回全部命中, 且 onMsg 返回 false 时立即停止。
func TestForEachByRecipientRangeIterates(t *testing.T) {
	const recipient = "alias@icloud.com"
	c := newMailboxE2EServer(t, recipient, 3)
	var got []string
	err := c.forEachByRecipientRange(recipient, 10, 0, DateRange{}, func(m Message) bool {
		got = append(got, m.ID)
		return true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("遍历到 %d 封, want 3", len(got))
	}

	// 提前停止: 只取第一封。
	var first []string
	err = c.forEachByRecipientRange(recipient, 10, 0, DateRange{}, func(m Message) bool {
		first = append(first, m.ID)
		return false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("提前停止应只遍历 1 封, got %d", len(first))
	}
}

var _ = backend.User(nil)
