// Package mail 实现 iCloud 邮件 IMAP 读取客户端。
//
// 通过 Apple 应用专用密码连接 imap.mail.me.com:993,
// 拉取隐私邮箱别名收到的邮件。对应原 Python 项目 icloud_mail.py。
package mail

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
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
	username string
	password string
	server   string
	port     int
	cli      *client.Client
}

// NewClient 创建 IMAP 客户端。需在调用其它方法前先 Connect。
func NewClient(appleID, appPassword string) *Client {
	return NewClientWithServer(appleID, appPassword, IMAPServer, IMAPPort)
}

// NewClientWithServer creates an IMAP client for a custom server.
func NewClientWithServer(username, password, server string, port int) *Client {
	return &Client{username: username, password: password, server: server, port: port}
}

// Connect 连接并登录 IMAP 服务器。已连接且存活时直接复用。
func (c *Client) Connect() error {
	if c.cli != nil {
		if err := c.cli.Noop(); err == nil {
			return nil
		}
		c.forceClose()
	}
	addr := fmt.Sprintf("%s:%d", c.server, c.port)
	cli, err := client.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("IMAP 连接失败: %w", err)
	}
	if err := cli.Login(c.username, c.password); err != nil {
		_ = cli.Logout()
		return fmt.Errorf("IMAP 登录失败 — 请检查邮箱账号、授权码和服务器地址: %w", err)
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

// envelopeFetchItems 只拉信封(无正文): 单封 ~500B, 168 封也只要几秒,
// 是渐进式加载第一阶段「速览列表」的取数方式。
var envelopeFetchItems = []imap.FetchItem{
	imap.FetchUid,
	imap.FetchEnvelope,
	imap.FetchInternalDate,
}

// inboxWindow 计算收件箱(新→旧)分页窗口对应的邮件序号集合。
//
// 返回 (from, to, ok): [from, to] 为 IMAP 序号范围(含两端),
// 窗口越界时 ok=false。offset/limit 语义与 SQL 一致: 0/20 = 最新 20 封。
func inboxWindow(total, offset, limit int) (from, to uint32, ok bool) {
	if total <= 0 || limit <= 0 || offset >= total {
		return 0, 0, false
	}
	// IMAP 序号 1(最旧)..total(最新); 新→旧第 offset+1 封的序号
	hi := total - offset
	lo := hi - limit + 1
	if lo < 1 {
		lo = 1
	}
	return uint32(lo), uint32(hi), true
}

// ListInboxPage 拉取收件箱(新→旧)第 offset 页的邮件信封, 返回 (本页, 总数)。
//
// 渐进式加载第一阶段: 只拉 UID/ENVELOPE/INTERNALDATE(无正文),
// Preview 留空, 由 FetchPreviews 按需补齐——大收件箱秒级出列表,
// 正文摘要只在用户看到时才付费拉取。
// days 用于服务端 SEARCH 过滤(0 表示不限制); 返回按时间倒序排列。
func (c *Client) ListInboxPage(limit, offset, days int) ([]Message, int, error) {
	if c.cli == nil {
		return nil, 0, fmt.Errorf("未连接")
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return nil, 0, err
	}
	if mbox.Messages == 0 {
		return []Message{}, 0, nil
	}

	// days 过滤走服务端 SEARCH(按 INTERNALDATE), 拿到符合日期的 UID 列表
	// (升序)后在本地做新→旧分页窗口; 比「全拉信封再本地过滤」少拉很多封。
	uids, err := c.recentUIDs(days)
	if err != nil {
		return nil, 0, err
	}
	total := len(uids)
	if total == 0 {
		return []Message{}, 0, nil
	}

	// uids 升序; 新→旧第 offset+1 封 = uids[total-1-offset]
	hiIdx := total - 1 - offset
	if hiIdx < 0 {
		return []Message{}, total, nil
	}
	loIdx := hiIdx - limit + 1
	if loIdx < 0 {
		loIdx = 0
	}
	window := uids[loIdx : hiIdx+1]

	msgs, err := c.fetchEnvelopesByUID(window)
	if err != nil {
		return nil, 0, err
	}
	// fetchEnvelopesByUID 返回按 UID 升序; 翻转成新→旧
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, total, nil
}

// recentUIDs 返回收件箱(可选按近 N 天过滤)的全部 UID, 升序。
func (c *Client) recentUIDs(days int) ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	if days > 0 {
		criteria.Since = time.Now().AddDate(0, 0, -days)
	}
	return c.cli.UidSearch(criteria)
}

// fetchEnvelopesByUID 按 UID 列表拉取信封(无正文), 返回按 UID 升序。
func (c *Client) fetchEnvelopesByUID(uids []uint32) ([]Message, error) {
	if len(uids) == 0 {
		return []Message{}, nil
	}
	seqset := new(imap.SeqSet)
	for _, uid := range uids {
		seqset.AddNum(uid)
	}
	messages := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, envelopeFetchItems, messages)
	}()
	var out []Message
	for msg := range messages {
		out = append(out, toMessage(msg))
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return out, nil
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

// FetchPreviews 按 UID 批量拉取正文摘要(partial fetch), 返回 id→摘要。
//
// 渐进式加载第二阶段: 前端拿到信封列表后, 对可见邮件分批调用本接口
// 补齐 Preview。每封最多传 previewPartLimit 字节(见 previewTextSection)。
func (c *Client) FetchPreviews(uids []uint32) (map[string]string, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if len(uids) == 0 {
		return map[string]string{}, nil
	}
	if len(uids) > 20 {
		uids = uids[:20] // 单批上限, 防止一次拉太多又退化成慢请求
	}
	seqset := new(imap.SeqSet)
	for _, uid := range uids {
		seqset.AddNum(uid)
	}
	messages := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, previewFetchItems, messages)
	}()
	out := make(map[string]string, len(uids))
	for msg := range messages {
		if msg == nil {
			continue
		}
		m := toMessageWithBody(msg)
		if m.ID != "" {
			out[m.ID] = m.Preview
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return out, nil
}

// FindByRecipient 查找发给指定隐私邮箱别名的最近 limit 封邮件(新→旧),
// 返回 (本页, 符合条件的总数)。offset 用于「加载更多」翻页。
func (c *Client) FindByRecipient(recipient string, limit, offset, days int) ([]Message, int, error) {
	var out []Message
	total := 0
	err := c.ForEachByRecipient(recipient, limit, offset, days, func(m Message) bool {
		out = append(out, m)
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ForEachByRecipient 按新→旧遍历发给 recipient 的邮件(分页版)。
// onMsg 返回 false 时立即停止(用于 OTP 命中即返回)。
func (c *Client) ForEachByRecipient(recipient string, limit, offset, days int, onMsg func(Message) bool) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if onMsg == nil {
		return fmt.Errorf("onMsg 不能为空")
	}
	if limit <= 0 {
		limit = 5
	}
	if offset < 0 {
		offset = 0
	}

	if _, err := c.cli.Select("INBOX", true); err != nil {
		return err
	}

	// 服务端按 To + 日期搜索, 本地在 UID 列表上做新→旧分页
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("To", recipient)
	if days > 0 {
		criteria.Since = time.Now().AddDate(0, 0, -days)
	}
	uids, err := c.cli.UidSearch(criteria)
	if err != nil || len(uids) == 0 {
		// SEARCH 不可用(或无结果): 退化为扫最近信封本地过滤
		return c.forEachRecentMatching(recipient, limit, offset, days, onMsg)
	}
	n := len(uids)
	hiIdx := n - 1 - offset
	if hiIdx < 0 {
		return nil
	}
	loIdx := hiIdx - limit + 1
	if loIdx < 0 {
		loIdx = 0
	}
	// 新→旧遍历本页
	for i := hiIdx; i >= loIdx; i-- {
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

// forEachRecentMatching 拉取收件箱最近若干封(仅 envelope), 本地按 To 过滤后
// 再取正文, 支持新→旧 offset 翻页。SEARCH 不可用时的回退路径。
func (c *Client) forEachRecentMatching(recipient string, limit, offset, days int, onMsg func(Message) bool) error {
	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return err
	}
	if mbox.Messages == 0 {
		return nil
	}
	// 只扫最近 scan 封, 避免全箱
	scan := (offset + limit) * 4
	if scan < 20 {
		scan = 20
	}
	if scan > 120 {
		scan = 120
	}
	if scan > int(mbox.Messages) {
		scan = int(mbox.Messages)
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
		when := receivedDate(msg)
		if !withinDays(when, days) {
			continue
		}
		cands = append(cands, cand{uid: msg.Uid, date: when, to: to})
	}
	if err := <-done; err != nil {
		return err
	}
	// 新→旧
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].date.After(cands[j].date) })
	for idx, cd := range cands {
		if idx < offset {
			continue
		}
		if idx >= offset+limit {
			break
		}
		m, ferr := c.fetchOneUID(cd.uid)
		if ferr != nil {
			return ferr
		}
		if !onMsg(m) {
			return nil
		}
	}
	return nil
}

// peekBodySection 用 BODY.PEEK[] 取正文, 不会把邮件标记为已读。
var peekBodySection = &imap.BodySectionName{Peek: true}

// previewTextSection 是摘要路径的正文 section: BODY.PEEK[TEXT]<0.16KB>。
//
// 直接抓整封前 N 字节(BODY[]<0.N>)会被巨大的头部块吃光预算: 实测 OpenAI
// 邮件的 DKIM/DMARC/BIMI 签名头就占 16KB+, 正文一个字节都进不来, Preview
// 全空。BODY[TEXT] 直接从正文起始, 配合 partial 每封只要 ~1.3s。
var previewTextSection = &imap.BodySectionName{
	Peek:    true,
	Partial: []int{0, previewPartLimit},
	BodyPartName: imap.BodyPartName{
		Specifier: imap.TextSpecifier,
	},
}

// previewHeaderSection 只取正文所需的顶层头字段: Content-Type 决定
// multipart 边界 / text-html 判定, Content-Transfer-Encoding 决定正文
// 解码方式(OpenAI 等邮件是 quoted-printable, 缺了会漏出 =3D/=20 原文)。
var previewHeaderSection = &imap.BodySectionName{
	Peek: true,
	BodyPartName: imap.BodyPartName{
		Specifier: imap.HeaderSpecifier,
		Fields:    []string{"Content-Type", "Content-Transfer-Encoding", "MIME-Version"},
	},
}

// previewFetchItems 摘要路径的 FETCH 项(两个小 section, 见各自注释)。
var previewFetchItems = []imap.FetchItem{
	imap.FetchUid,
	imap.FetchEnvelope,
	imap.FetchInternalDate,
	previewHeaderSection.FetchItem(),
	previewTextSection.FetchItem(),
}

// fullFetchItems 完整路径的 FETCH 项(全量正文)。
var fullFetchItems = []imap.FetchItem{
	imap.FetchUid,
	imap.FetchEnvelope,
	imap.FetchInternalDate,
	peekBodySection.FetchItem(),
}

// fetchUID 按 UID 拉取单封原始消息(共享 seqset/管道/错误处理样板)。
func (c *Client) fetchUID(uid uint32) (*imap.Message, error) {
	return c.fetchUIDItems(uid, fullFetchItems)
}

// fetchUIDPreview 按 UID 拉取单封消息的前 previewPartLimit 字节(摘要用)。
func (c *Client) fetchUIDPreview(uid uint32) (*imap.Message, error) {
	return c.fetchUIDItems(uid, previewFetchItems)
}

func (c *Client) fetchUIDItems(uid uint32, items []imap.FetchItem) (*imap.Message, error) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

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
	return msg, nil
}

// fetchOneUID 拉取单封邮件(含 body preview), 使用 BODY.PEEK 不标已读。
func (c *Client) fetchOneUID(uid uint32) (Message, error) {
	msg, err := c.fetchUIDPreview(uid)
	if err != nil {
		return Message{}, err
	}
	return toMessageWithBody(msg), nil
}

// GetFull 获取单封邮件的完整内容(含正文)。
func (c *Client) GetFull(uid uint32) (*FullMessage, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if _, err := c.cli.Select("INBOX", true); err != nil {
		return nil, err
	}

	msg, err := c.fetchUID(uid)
	if err != nil {
		return nil, err
	}
	return toFullMessage(msg), nil
}

// toFullMessage 从已 fetch 的 IMAP 消息构造完整邮件(含正文)。
//
// 服务端响应的 section 名恒为 BODY[...](PEEK 只是请求修饰符, RFC 3501),
// 且 go-imap 的 GetBody 会把查询用的 section 归一化(Peek 一律置 false),
// 所以一次 GetBody(&BodySectionName{}) 即可命中 RFC822 / BODY.PEEK[] 两种请求的结果。
func toFullMessage(msg *imap.Message) *FullMessage {
	full := &FullMessage{Message: toMessage(msg)}
	if r := msg.GetBody(&imap.BodySectionName{}); r != nil {
		full.Body, full.ContentType = extractBodyWithType(r)
	}
	return full
}

// Delete 删除收件箱中指定 UID 的邮件。
func (c *Client) Delete(uid uint32) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if uid == 0 {
		return fmt.Errorf("邮件 UID 无效")
	}
	if _, err := c.cli.Select("INBOX", false); err != nil {
		return err
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.cli.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	return c.cli.Expunge(nil)
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
	}
	// Envelope 缺失发信时间时回退 INTERNALDATE, 否则前端日期列会是空白
	if d := messageDate(msg); !d.IsZero() {
		m.Date = d.Format(time.RFC3339)
	}
	if m.From != "" {
		m.From = decodeHeader(m.From)
	}
	if m.To != "" {
		m.To = decodeHeader(m.To)
	}
	return m
}

// messageDate 返回用于展示的邮件时间: 优先发件人声明的 Envelope.Date,
// 缺失时回退 INTERNALDATE。
func messageDate(msg *imap.Message) time.Time {
	if msg.Envelope != nil && !msg.Envelope.Date.IsZero() {
		return msg.Envelope.Date
	}
	return msg.InternalDate
}

// receivedDate 返回服务端收件时间(INTERNALDATE), 用于 "最近 N 天" 过滤与排序。
//
// 不能拿发件人声明的 Date 头做收件时间判断: 该字段由发件人填写, 常见落后于
// 实际到达时间(时区错误、补发、伪造), 用它过滤会把刚收到的邮件当成旧邮件丢掉。
func receivedDate(msg *imap.Message) time.Time {
	if !msg.InternalDate.IsZero() {
		return msg.InternalDate
	}
	return messageDate(msg)
}

// withinDays 判断收件时间是否落在近 days 天内(days<=0 或时间缺失视为满足,
// 宁可多显示也不误丢)。
func withinDays(when time.Time, days int) bool {
	if days <= 0 || when.IsZero() {
		return true
	}
	return time.Since(when) <= time.Duration(days)*24*time.Hour
}

// previewLimit 是列表摘要保留的最大字符数(按 rune 计)。
//
// 摘要只用于列表显示与 OTP 提取, 而 ListInbox 会为每封邮件解析完整正文;
// 不设上限时一封 2MB 的邮件就会产生同尺寸的摘要, limit=100 时响应可达数百 MB。
const previewLimit = 2000

// capPreview 按 rune 截断摘要, 避免截断 UTF-8 字符。
func capPreview(s string) string {
	if len(s) <= previewLimit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= previewLimit {
		return s
	}
	return strings.TrimSpace(string(runes[:previewLimit]))
}

// lookupPreviewBody 把摘要路径抓到的 头部+正文 两个 section 拼成一段
// 可被 MIME 解析器消费的字节流, 并返回正文是否在 previewPartLimit 处截断。
//
// 响应 section 名(PEEK 剥离后)为 HEADER.FIELDS (…) 与 TEXT<0>; 服务端对
// 未命中的 section 不回数据, 拼装时缺哪个就当空。
// 截断标记必须在读取 literal 之前采样: literal 是一次性的, 读完 Len() 归零。
func lookupPreviewBody(msg *imap.Message) (io.Reader, bool) {
	header := msg.GetBody(previewHeaderSection)
	text := msg.GetBody(previewTextSection)
	if header == nil && text == nil {
		// 旧服务器可能不支持细分 section: 退回整封(不带 Partial, 命中
		// 全量响应), 解析侧的 readCapped 仍会限制读取量。
		if r := msg.GetBody(peekBodySection); r != nil {
			return r, false
		}
		return msg.GetBody(&imap.BodySectionName{}), false
	}
	truncated := false
	if lit, ok := text.(imap.Literal); ok && lit.Len() >= previewPartLimit {
		truncated = true
	}
	var buf bytes.Buffer
	if header != nil {
		buf.ReadFrom(header)
	}
	buf.WriteString("\r\n")
	if text != nil {
		buf.ReadFrom(text)
	}
	return &buf, truncated
}

// previewTruncatedNotice 是「HTML 邮件截断后无可见文本」时的摘要占位:
// 摘要走 partial fetch, 大邮件(营销/验证邮件常见)前 16KB 可能全是
// <style>/字体声明, 截断处没有可见文本。此时给出占位说明而不是空白,
// 用户点击详情仍可读全文(详情路径是全量抓取, 不截断)。
const previewTruncatedNotice = "（HTML 邮件，摘要截断——点击查看全文）"

// toMessageWithBody 在 toMessage 基础上解析正文填充 Preview(供 OTP 提取)。
func toMessageWithBody(msg *imap.Message) Message {
	m := toMessage(msg)
	if r, truncated := lookupPreviewBody(msg); r != nil {
		if body := extractBody(r); body != "" {
			m.Preview = capPreview(strings.TrimSpace(body))
		} else if truncated {
			m.Preview = previewTruncatedNotice
		}
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

// extractBody 解析 MIME 正文并返回可读纯文本, 失败时返回空串。
//
// 摘要路径: 每个部分最多读 previewPartLimit 字节(见其注释)。
func extractBody(r io.Reader) string {
	body, _ := extractBodyDepth(r, 0, previewPartLimit)
	return body
}

// extractBodyWithType 解析 MIME 正文, 返回可读纯文本与顶层 Content-Type。
// 详情路径(GetFull)不限量, 正文必须完整。
//
// 支持 multipart 递归展开、message/rfc822 转发邮件、Content-Transfer-Encoding
// (base64 / quoted-printable)与字符集解码(UTF-8 / GBK / ISO-8859-1 等,
// 由 go-message/charset 注册)。优先 text/plain 部分; 只有 HTML 时转成纯文本;
// 附件一律忽略。
func extractBodyWithType(r io.Reader) (body, contentType string) {
	return extractBodyDepth(r, 0, 0)
}

// maxMIMEDepth 限制 message/rfc822 等嵌套解析深度, 防止构造的深层邮件拖垮解析。
const maxMIMEDepth = 5

// previewPartLimit 是摘要路径读取单个 MIME 部分的字节上限。
//
// 摘要最终只保留 previewLimit(2000)个字符, 读满之后的正文对摘要已无意义;
// 且 iCloud IMAP 按响应字节数限速(见 previewPartialSection 注释), 16KB 是
// 「覆盖正文开头」与「单封 ~1.3s」的实测平衡点。详情路径不受此限制。
const previewPartLimit = 16 << 10

// readCapped 读取整个 reader; maxBytes>0 时限制读取字节数。
func readCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes > 0 {
		r = io.LimitReader(r, maxBytes)
	}
	return io.ReadAll(r)
}

func extractBodyDepth(r io.Reader, depth int, maxBytes int64) (body, contentType string) {
	if r == nil || depth > maxMIMEDepth {
		return "", ""
	}
	mr, err := gomail.CreateReader(r)
	if err != nil && !message.IsUnknownCharset(err) {
		// 损坏的邮件不致命(详情页显示"无正文"), 但留一条日志便于定位
		log.Printf("mail: 解析 MIME 正文失败: %v", err)
		return "", ""
	}
	defer mr.Close()

	contentType = mr.Header.Get("Content-Type")
	// 只有多部分邮件才把 name= 当作附件: 单部分 text/plain; name=... 常被
	// 发送方当作正文, 若一律跳过会导致正文字段变空。
	topIsMultipart := strings.HasPrefix(strings.ToLower(contentType), "multipart/")

	// htmlRaw 延迟解析: multipart/alternative 的 HTML 部分通常是最大的一块
	// (实测 1MB HTML 的 sanitizePreview 约 147ms), 而列表会为每封邮件解析完整正文。
	// 先原样读入, 只有在没有 text/plain 时才真正转成纯文本, 顺序无关都一样省。
	var plain string
	var htmlRaw []byte
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil && !message.IsUnknownCharset(perr) {
			log.Printf("mail: 读取 MIME 部分失败: %v", perr)
			break
		}
		if part == nil {
			// 底层读取失败时 go-message 返回 nil part; 而字符集未知时它会同时给出
			// 可用的 part 与 error, 所以这里不能只靠 perr 判断。
			break
		}
		mt, attachment := classifyPart(part, topIsMultipart)
		if attachment {
			continue
		}
		switch mt {
		case "text/plain":
			if raw, rerr := readCapped(part.Body, maxBytes); rerr == nil {
				plain = sanitizePlainPreview(string(raw))
			}
		case "text/html":
			if htmlRaw == nil {
				if raw, rerr := readCapped(part.Body, maxBytes); rerr == nil {
					htmlRaw = raw
				}
			}
		case "message/rfc822":
			// 转发的邮件(验证码常以转发形式到达)要往里钻一层
			if nested, _ := extractBodyDepth(part.Body, depth+1, maxBytes); nested != "" {
				plain = nested
			}
		}
		// 已拿到纯文本正文, 后续部分(更大的 HTML、更多附件)不再需要
		if plain != "" {
			break
		}
	}
	if plain != "" {
		return plain, contentType
	}
	// 只有确实没有 text/plain 时才付出 HTML→纯文本的解析成本
	if htmlRaw != nil {
		return sanitizePreview(string(htmlRaw)), contentType
	}
	return "", contentType
}

// classifyPart 判断 MIME 部分的媒体类型与是否附件(Content-Type 只解析一次)。
//
// 媒体类型缺失时按 RFC 2045 视为 text/plain。
// 附件判定不能只看 mime.ParseMediaType 的结果: 现实中大量邮件写了
// "attachment; filename=报告 2026.txt"(未加引号却含空格)导致解析失败,
// 若此时放行, 附件内容就会被当成正文展示。因此解析失败时退化为前缀判断。
//
// 只有多部分邮件(topIsMultipart)才把仅有 name= 参数的部分视为附件:
// 单部分 "text/plain; name=..." 常被发送方用作正文载体, 不应丢弃。
func classifyPart(part *gomail.Part, topIsMultipart bool) (mediaType string, attachment bool) {
	rawCT := part.Header.Get("Content-Type")
	mt, params, cerr := mime.ParseMediaType(rawCT)
	if cerr != nil || mt == "" {
		mt = "text/plain"
	}
	mediaType = strings.ToLower(mt)

	rawDisp := strings.TrimSpace(part.Header.Get("Content-Disposition"))
	disp, _, derr := mime.ParseMediaType(rawDisp)
	if derr == nil {
		switch strings.ToLower(disp) {
		case "inline":
			// 明确声明 inline 的部分按正文处理
			return mediaType, false
		case "attachment":
			return mediaType, true
		}
	} else if strings.HasPrefix(strings.ToLower(rawDisp), "attachment") {
		return mediaType, true
	}
	// 没有(或无法解析)disposition, 但带文件名参数 → 传统附件写法
	if !topIsMultipart {
		return mediaType, false
	}
	if params["name"] != "" {
		return mediaType, true
	}
	// Content-Type 本身解析失败时退化为子串判断(未加引号的 name= 值含空格等)
	if cerr != nil && strings.Contains(strings.ToLower(rawCT), "name=") {
		return mediaType, true
	}
	return mediaType, false
}
