// Package mail 实现 iCloud 邮件 IMAP 读取客户端。
//
// 通过 Apple 应用专用密码连接 imap.mail.me.com:993,
// 拉取隐私邮箱别名收到的邮件。对应原 Python 项目 icloud_mail.py。
package mail

import (
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/charset"
)

const (
	IMAPServer = "imap.mail.me.com"
	IMAPPort   = 993
)

// Message 是一封邮件的摘要信息。
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Preview string `json:"preview"`
}

// FullMessage 是一封邮件的完整内容(含正文)。
type FullMessage struct {
	Message
	Body        string `json:"body"`
	ContentType string `json:"content_type"`
}

// Client 是 iCloud 邮件 IMAP 客户端。
type Client struct {
	appleID     string
	appPassword string
	cli         *client.Client
}

// NewClient 创建 IMAP 客户端。需在调用其它方法前先 Connect。
func NewClient(appleID, appPassword string) *Client {
	return &Client{appleID: appleID, appPassword: appPassword}
}

// Connect 连接并登录 IMAP 服务器。已连接且存活时直接复用。
func (c *Client) Connect() error {
	if c.cli != nil {
		if err := c.cli.Noop(); err == nil {
			return nil
		}
		c.forceClose()
	}
	addr := fmt.Sprintf("%s:%d", IMAPServer, IMAPPort)
	cli, err := client.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("IMAP 连接失败: %w", err)
	}
	if err := cli.Login(c.appleID, c.appPassword); err != nil {
		_ = cli.Logout()
		return fmt.Errorf("IMAP 登录失败 — 请检查: 1) 应用专用密码是否正确 2) Apple ID: %s — %w", c.appleID, err)
	}
	c.cli = cli
	return nil
}

// Ping 探测连接是否仍可用(NOOP)。
func (c *Client) Ping() error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	return c.cli.Noop()
}

// Disconnect 登出并关闭连接。
func (c *Client) Disconnect() {
	if c.cli != nil {
		_ = c.cli.Logout()
		c.cli = nil
	}
}

// forceClose 不发 LOGOUT, 直接掐断(坏连接/池丢弃时用)。
func (c *Client) forceClose() {
	if c.cli != nil {
		_ = c.cli.Terminate()
		c.cli = nil
	}
}

// InboxCount 返回收件箱邮件总数。
func (c *Client) InboxCount() (int, error) {
	if c.cli == nil {
		return 0, fmt.Errorf("未连接")
	}
	mbox, err := c.cli.Select("INBOX", false)
	if err != nil {
		return 0, err
	}
	return int(mbox.Messages), nil
}

// ListInbox 拉取收件箱最近 limit 封邮件摘要。
//
// days 用于过滤只看近 N 天的邮件(0 表示不限制)。
// 返回按时间倒序排列。
func (c *Client) ListInbox(limit int, days int) ([]Message, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if limit <= 0 {
		limit = 50
	}

	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return nil, err
	}
	total := int(mbox.Messages)
	if total == 0 {
		return []Message{}, nil
	}

	// 计算起始序号(只取最近 limit 封)
	from := uint32(1)
	if uint32(limit) < mbox.Messages {
		from = mbox.Messages - uint32(limit) + 1
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// 拉取完整正文,以便填充 Preview(OTP 验证码在正文中); PEEK 不标已读
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchEnvelope,
		imap.FetchInternalDate,
		section.FetchItem(),
	}

	messages := make(chan *imap.Message, limit)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.Fetch(seqset, items, messages)
	}()

	var out []Message
	for msg := range messages {
		m := toMessageWithBody(msg)
		// days 过滤
		if days > 0 {
			if t, err := time.Parse(time.RFC1123Z, m.Date); err == nil {
				if time.Since(t) > time.Duration(days)*24*time.Hour {
					continue
				}
			}
		}
		out = append(out, m)
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return out, nil
}

// FindByRecipient 查找发给指定隐私邮箱别名的最近 limit 封邮件(新→旧)。
//
// 先尝试 IMAP TO 搜索; 失败则只扫收件箱最近若干封本地过滤。
func (c *Client) FindByRecipient(recipient string, limit int, days int) ([]Message, error) {
	var out []Message
	err := c.ForEachByRecipient(recipient, limit, days, func(m Message) bool {
		out = append(out, m)
		return true // 收满 limit 为止
	})
	return out, err
}

// ForEachByRecipient 按新→旧遍历发给 recipient 的最近 limit 封邮件。
// onMsg 返回 false 时立即停止(用于 OTP 命中即返回)。
func (c *Client) ForEachByRecipient(recipient string, limit int, days int, onMsg func(Message) bool) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if onMsg == nil {
		return fmt.Errorf("onMsg 不能为空")
	}
	if limit <= 0 {
		limit = 5
	}

	if _, err := c.cli.Select("INBOX", true); err != nil {
		return err
	}

	// 1) 服务端按 To 搜索
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("To", recipient)
	if days > 0 {
		criteria.Since = time.Now().AddDate(0, 0, -days)
	}
	uids, err := c.cli.UidSearch(criteria)
	if err == nil && len(uids) > 0 {
		// UID 升序 → 取最后 limit 个(最新) → 倒序遍历
		uids = newestUIDs(uids, limit)
		for i := len(uids) - 1; i >= 0; i-- {
			m, ferr := c.fetchOneUID(uids[i])
			if ferr != nil {
				return ferr
			}
			if !onMsg(m) {
				return nil
			}
		}
		return nil
	}

	// 2) fallback: 只扫最近 N 封信封, 命中 To 再拉 body
	return c.forEachRecentMatching(recipient, limit, days, onMsg)
}

// newestUIDs 保留 UID 列表中最新的 limit 个(假定 UID 升序)。
func newestUIDs(uids []uint32, limit int) []uint32 {
	if limit <= 0 || len(uids) <= limit {
		return uids
	}
	return uids[len(uids)-limit:]
}

// forEachRecentMatching 拉取收件箱最近 scan 封(仅 envelope), 本地按 To 过滤后再取 body。
func (c *Client) forEachRecentMatching(recipient string, limit int, days int, onMsg func(Message) bool) error {
	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return err
	}
	total := int(mbox.Messages)
	if total == 0 {
		return nil
	}
	// 只扫最近 scan 封, 避免全箱
	scan := limit * 4
	if scan < 20 {
		scan = 20
	}
	if scan > 80 {
		scan = 80
	}
	if scan > total {
		scan = total
	}
	from := mbox.Messages - uint32(scan) + 1
	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// 仅 envelope + date, 不拉 body
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	messages := make(chan *imap.Message, scan)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.Fetch(seqset, items, messages)
	}()

	type cand struct {
		uid  uint32
		date time.Time
		to   string
	}
	var cands []cand
	recipient = strings.ToLower(recipient)
	for msg := range messages {
		if msg == nil || msg.Envelope == nil {
			continue
		}
		to := ""
		if len(msg.Envelope.To) > 0 {
			parts := make([]string, 0, len(msg.Envelope.To))
			for _, a := range msg.Envelope.To {
				parts = append(parts, a.Address())
			}
			to = strings.Join(parts, ", ")
		}
		if !strings.Contains(strings.ToLower(to), recipient) {
			continue
		}
		if days > 0 && !msg.Envelope.Date.IsZero() {
			if time.Since(msg.Envelope.Date) > time.Duration(days)*24*time.Hour {
				continue
			}
		}
		cands = append(cands, cand{uid: msg.Uid, date: msg.Envelope.Date, to: to})
	}
	if err := <-done; err != nil {
		return err
	}
	// 新→旧
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].date.After(cands[i].date) {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	n := 0
	for _, cd := range cands {
		if n >= limit {
			break
		}
		m, ferr := c.fetchOneUID(cd.uid)
		if ferr != nil {
			return ferr
		}
		n++
		if !onMsg(m) {
			return nil
		}
	}
	return nil
}

// fetchOneUID 拉取单封邮件(含 body preview), 使用 BODY.PEEK 不标已读。
func (c *Client) fetchOneUID(uid uint32) (Message, error) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, section.FetchItem()}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, items, messages)
	}()
	msg := <-messages
	if err := <-done; err != nil {
		return Message{}, err
	}
	if msg == nil {
		return Message{}, fmt.Errorf("邮件不存在 (uid=%d)", uid)
	}
	return toMessageWithBody(msg), nil
}

func (c *Client) fetchByUIDs(uids []uint32, limit int) ([]Message, error) {
	if len(uids) == 0 {
		return []Message{}, nil
	}
	uids = newestUIDs(uids, limit)
	var out []Message
	// 新→旧
	for i := len(uids) - 1; i >= 0; i-- {
		m, err := c.fetchOneUID(uids[i])
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

// GetFull 获取单封邮件的完整内容(含正文)。
func (c *Client) GetFull(uid uint32) (*FullMessage, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if _, err := c.cli.Select("INBOX", true); err != nil {
		return nil, err
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchRFC822}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, items, messages)
	}()

	msg := <-messages
	if err := <-done; err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, fmt.Errorf("邮件不存在 (uid=%d)", uid)
	}

	full := &FullMessage{Message: toMessage(msg)}
	// 解析正文
	if r := msg.GetBody(&imap.BodySectionName{}); r != nil {
		if em, err := mail.ReadMessage(r); err == nil {
			body, _ := readBody(em)
			full.Body = body
			full.ContentType = em.Header.Get("Content-Type")
		}
	}
	return full, nil
}

// ---- 解析工具 ----

func toMessage(msg *imap.Message) Message {
	m := Message{}
	if msg.Uid > 0 {
		m.ID = fmt.Sprintf("%d", msg.Uid)
	}
	if msg.Envelope != nil {
		if len(msg.Envelope.From) > 0 {
			m.From = msg.Envelope.From[0].Address()
		}
		if len(msg.Envelope.To) > 0 {
			addrs := make([]string, 0, len(msg.Envelope.To))
			for _, a := range msg.Envelope.To {
				addrs = append(addrs, a.Address())
			}
			m.To = strings.Join(addrs, ", ")
		}
		m.Subject = decodeHeader(msg.Envelope.Subject)
		if !msg.Envelope.Date.IsZero() {
			m.Date = msg.Envelope.Date.Format(time.RFC3339)
		}
	}
	if m.From != "" {
		m.From = decodeHeader(m.From)
	}
	if m.To != "" {
		m.To = decodeHeader(m.To)
	}
	return m
}

// toMessageWithBody 在 toMessage 基础上解析正文填充 Preview(供 OTP 提取)。
func toMessageWithBody(msg *imap.Message) Message {
	m := toMessage(msg)
	// Fetch 可能用 BODY[] 或 BODY.PEEK[], 两种 section 都试
	for _, section := range []*imap.BodySectionName{{Peek: true}, {}} {
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		em, err := mail.ReadMessage(r)
		if err != nil {
			continue
		}
		body, err := readBody(em)
		if err != nil {
			continue
		}
		m.Preview = strings.TrimSpace(body)
		break
	}
	return m
}

// decodeHeader 解码 RFC 2047 编码的邮件头(如 =?UTF-8?B?xxx?=)。
func decodeHeader(s string) string {
	if s == "" {
		return ""
	}
	dec := mime.WordDecoder{CharsetReader: charset.Reader}
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// readBody 读取邮件正文,优先 text/plain,其次从 HTML 提取纯文本。
func readBody(msg *mail.Message) (string, error) {
	ct := msg.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/html") {
		raw, _ := io.ReadAll(msg.Body)
		// quoted-printable 解码
		if strings.Contains(msg.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
			r := quotedprintable.NewReader(strings.NewReader(string(raw)))
			raw, _ = io.ReadAll(r)
		}
		return sanitizePreview(string(raw)), nil
	}
	// 默认当 text/plain
	raw, err := io.ReadAll(msg.Body)
	if err != nil {
		return "", err
	}
	if strings.Contains(msg.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		r := quotedprintable.NewReader(strings.NewReader(string(raw)))
		raw, _ = io.ReadAll(r)
	}
	return sanitizePlainPreview(string(raw)), nil
}
