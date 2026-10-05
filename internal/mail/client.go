// Package mail 实现 iCloud 邮件 IMAP 读取客户端。
//
// 通过 Apple 应用专用密码连接 imap.mail.me.com:993,
// 拉取隐私邮箱别名收到的邮件。对应原 Python 项目 icloud_mail.py。
package mail

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"sort"
	"strconv"
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
	// Folder 是邮件所在文件夹(INBOX / Junk)。IMAP 的 UID 按文件夹生效,
	// 读正文/删除必须带上它才能定位到正确的邮件。
	Folder string `json:"folder,omitempty"`
}

// MailFolders 是读信时扫描的文件夹。
//
// HME 转发到 iCloud 邮箱的邮件常被 iCloud 判为垃圾邮件(实测如此),
// 只查 INBOX 会漏掉整箱验证码。扫描顺序无关紧要,结果按时间合并排序。
var MailFolders = []string{"INBOX", "Junk"}

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

// ListInboxPageRange 是 ListInboxPage 的日期区间版本(闭区间,零值表示不限)。
//
// 同时扫描 MailFolders(INBOX + Junk): 转发邮件常被 iCloud 判为垃圾邮件,
// 只查 INBOX 会漏掉整箱验证码。各文件夹先取符合日期的 UID 集合,
// 合并后在「全量」上做新→旧分页, 保证 total 与翻页窗口跨文件夹一致。
func (c *Client) ListInboxPageRange(limit, offset int, r DateRange) ([]Message, int, error) {
	if c.cli == nil {
		return nil, 0, fmt.Errorf("未连接")
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	// 各文件夹的 UID 集合(已按日期过滤), 记录 folder 以便读取时定位。
	type folderUIDs struct {
		folder string
		uids   []uint32
	}
	var perFolder []folderUIDs
	total := 0
	for _, folder := range MailFolders {
		uids, err := c.folderUIDsRange(folder, r)
		if err != nil {
			// 文件夹不存在(如无 Junk 的账号)不算错误, 跳过继续。
			if isNoSuchFolder(err) {
				continue
			}
			return nil, 0, err
		}
		if len(uids) == 0 {
			continue
		}
		perFolder = append(perFolder, folderUIDs{folder: folder, uids: uids})
		total += len(uids)
	}
	if total == 0 {
		return []Message{}, 0, nil
	}

	// 各文件夹内部 UID 升序 ≈ 时间升序; 先各自取「最新的若干」,
	// 合并排序后再切本页, 避免为翻页拉取全部邮件。
	need := offset + limit
	var all []folderUID
	for _, fu := range perFolder {
		// 该文件夹最新的 need 封(UID 升序 → 从尾部取)。
		start := len(fu.uids) - need
		if start < 0 {
			start = 0
		}
		for _, uid := range fu.uids[start:] {
			all = append(all, folderUID{folder: fu.folder, uid: uid})
		}
	}
	// 跨文件夹合并后拉信封, 再按时间新→旧排序。
	msgs, err := c.fetchEnvelopesForCandidates(all)
	if err != nil {
		return nil, 0, err
	}
	sort.SliceStable(msgs, func(i, j int) bool { return messageLess(msgs[i], msgs[j]) })
	if offset >= len(msgs) {
		return []Message{}, total, nil
	}
	end := offset + limit
	if end > len(msgs) {
		end = len(msgs)
	}
	return msgs[offset:end], total, nil
}

// messageTime 解析消息的显示时间(RFC3339 字符串)。跨文件夹合并排序必须用
// 绝对时间比较——RFC3339 字符串跨时区直接比大小会错序。
func messageTime(m Message) time.Time {
	t, err := time.Parse(time.RFC3339, m.Date)
	if err != nil {
		return time.Time{}
	}
	return t
}

// messageLess 是列表排序的比较函数: 时间新→旧; 同刻按文件夹/UID 稳定排序,
// 保证分页在两次请求之间一致(避免同刻邮件在页间重复或漏出)。
func messageLess(a, b Message) bool {
	ta, tb := messageTime(a), messageTime(b)
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	if a.Folder != b.Folder {
		return a.Folder < b.Folder
	}
	_, ua, ea := ParseMessageID(a.ID)
	_, ub, eb := ParseMessageID(b.ID)
	if ea == nil && eb == nil {
		return ua > ub
	}
	return a.ID > b.ID
}

// MessageRef 是邮件的文件夹限定引用: IMAP UID 按文件夹生效, 跨文件夹操作必须成对。
type MessageRef struct {
	Folder string
	UID    uint32
}

// CanonicalID 返回该引用的对外 ID(INBOX 省略文件夹前缀, 与列表返回的 id 一致)。
func (r MessageRef) CanonicalID() string { return messageID(r.Folder, r.UID) }

// messageID 构造对外可见的邮件 ID。
//
// INBOX 用纯 UID(与历史行为兼容), 其它文件夹用 "folder:uid"——
// IMAP UID 按文件夹生效, 跨文件夹会撞号, 必须带文件夹消歧,
// 否则打开/删除 Junk 邮件会误操作到 INBOX 里同 UID 的邮件。
func messageID(folder string, uid uint32) string {
	if folder == "" || folder == "INBOX" {
		return fmt.Sprintf("%d", uid)
	}
	return fmt.Sprintf("%s:%d", folder, uid)
}

// ParseMessageID 把对外 ID 解析回 (folder, uid)。
// 无冒号的 ID 视为 INBOX(兼容旧格式)。
func ParseMessageID(id string) (string, uint32, error) {
	folder := "INBOX"
	raw := id
	if i := strings.LastIndex(id, ":"); i >= 0 {
		folder = id[:i]
		raw = id[i+1:]
	}
	uid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("邮件 ID 无效: %q", id)
	}
	return folder, uint32(uid), nil
}

// isNoSuchFolder 判断错误是否为「文件夹不存在」。
func isNoSuchFolder(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such mailbox") || strings.Contains(msg, "does not exist")
}

// folderUIDsRange 返回指定文件夹中符合日期区间的 UID(升序)。
func (c *Client) folderUIDsRange(folder string, r DateRange) ([]uint32, error) {
	if _, err := c.cli.Select(folder, true); err != nil {
		return nil, err
	}
	return c.cli.UidSearch(dateRangeSearchCriteria(r))
}

// folderUID 是「文件夹 + UID」对: IMAP UID 按文件夹生效, 定位邮件必须成对。
type folderUID struct {
	folder string
	uid    uint32
}

// fetchEnvelopesForCandidates 按 (folder, uid) 列表拉取信封。
// 每个文件夹只 SELECT 一次, 并在结果上标注来源 folder + 文件夹限定的对外 ID。
func (c *Client) fetchEnvelopesForCandidates(cands []folderUID) ([]Message, error) {
	byFolder := map[string][]uint32{}
	for _, cd := range cands {
		byFolder[cd.folder] = append(byFolder[cd.folder], cd.uid)
	}
	var out []Message
	for folder, uids := range byFolder {
		if _, err := c.cli.Select(folder, true); err != nil {
			return nil, err
		}
		msgs, err := c.fetchEnvelopesByUID(uids)
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			stampFolder(&msgs[i], folder)
		}
		out = append(out, msgs...)
	}
	return out, nil
}

// stampFolder 标注消息来源文件夹, 并把 ID 重写为文件夹限定的形式。
func stampFolder(m *Message, folder string) {
	m.Folder = folder
	if uid, err := strconv.ParseUint(m.ID, 10, 32); err == nil {
		m.ID = messageID(folder, uint32(uid))
	}
}

// recentUIDsRange 返回收件箱在日期区间内的全部 UID, 升序。
func (c *Client) recentUIDsRange(r DateRange) ([]uint32, error) {
	return c.cli.UidSearch(dateRangeSearchCriteria(r))
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

// FetchPreviews 按 UID 批量拉取 INBOX 邮件的正文摘要(兼容旧调用方)。
//
// 渐进式加载第二阶段: 前端拿到信封列表后, 对可见邮件分批调用本接口
// 补齐 Preview。每封最多传 previewPartLimit 字节(见 previewTextSection)。
func (c *Client) FetchPreviews(uids []uint32) (map[string]string, error) {
	refs := make([]MessageRef, 0, len(uids))
	for _, uid := range uids {
		refs = append(refs, MessageRef{Folder: "INBOX", UID: uid})
	}
	return c.FetchPreviewsRefs(refs)
}

// FetchPreviewsRefs 按 (folder, uid) 批量拉取正文摘要(partial fetch),
// 返回 对外ID→摘要。每个文件夹只 SELECT 一次; UID 按文件夹生效, 必须成对传入。
func (c *Client) FetchPreviewsRefs(refs []MessageRef) (map[string]string, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	if len(refs) > 20 {
		refs = refs[:20] // 单批上限, 防止一次拉太多又退化成慢请求
	}
	byFolder := map[string][]uint32{}
	for _, r := range refs {
		folder := r.Folder
		if folder == "" {
			folder = "INBOX"
		}
		byFolder[folder] = append(byFolder[folder], r.UID)
	}
	out := make(map[string]string, len(refs))
	for folder, uids := range byFolder {
		if _, err := c.cli.Select(folder, true); err != nil {
			return nil, err
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
		for msg := range messages {
			if msg == nil {
				continue
			}
			m := toMessageWithBody(msg)
			stampFolder(&m, folder)
			if m.ID != "" {
				out[m.ID] = m.Preview
			}
		}
		if err := <-done; err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FindByRecipientRange 是 FindByRecipient 的日期区间版本。
//
// 返回 (本页, 符合条件的总数)。总数由 forEachByRecipientRange 内部统计
// (服务端 SEARCH 命中的 UID 数), 供前端「加载更多」判断是否还有下一页。
func (c *Client) FindByRecipientRange(recipient string, limit, offset int, r DateRange) ([]Message, int, error) {
	var out []Message
	total := 0
	err := c.forEachByRecipientRange(recipient, limit, offset, r, func(m Message) bool {
		out = append(out, m)
		return true
	}, &total)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// forEachByRecipientRange 是 ForEachByRecipient 的日期区间版本。
//
// 同时扫描 MailFolders(INBOX + Junk), 合并后按时间新→旧分页——转发邮件
// 常被 iCloud 判为垃圾邮件, 只查 INBOX 会漏掉验证码。
//
// 各文件夹先用服务端 SEARCH 拿符合条件的 UID 集合; SEARCH 不可用时
// 退化为扫最近信封本地过滤。本页邮件再按文件夹定位拉取完整摘要。
//
// totalOut 非 nil 时写回符合条件的邮件总数(不受本页 limit/offset 影响)。
func (c *Client) forEachByRecipientRange(recipient string, limit, offset int, r DateRange, onMsg func(Message) bool, totalOut *int) error {
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

	// 各文件夹按 To + 日期搜索(服务端 SEARCH)
	criteria := dateRangeSearchCriteria(r)
	criteria.Header.Add("To", recipient)
	var all []folderUID
	for _, folder := range MailFolders {
		if _, err := c.cli.Select(folder, true); err != nil {
			if isNoSuchFolder(err) {
				continue
			}
			return err
		}
		uids, err := c.cli.UidSearch(criteria)
		if err != nil {
			// SEARCH 不可用: 退化为扫最近信封本地过滤
			return c.forEachRecentMatchingRange(recipient, limit, offset, r, onMsg, totalOut)
		}
		for _, uid := range uids {
			all = append(all, folderUID{folder: folder, uid: uid})
		}
	}
	if totalOut != nil {
		// 各文件夹 SEARCH 命中数之和即符合条件总数(不受本页分页影响)。
		*totalOut = len(all)
	}
	if len(all) == 0 {
		return nil
	}

	// 跨文件夹合并需要真实时间排序: 只为「最新的 need 封」拉信封
	// (文件夹内 UID 升序 ≈ 时间升序, 各取尾部 need 封是全局前 need 的超集),
	// 排序后切出本页, 再按文件夹定位拉完整摘要。
	need := offset + limit
	if need > len(all) {
		need = len(all)
	}
	byFolder := map[string][]uint32{}
	for _, fu := range all {
		byFolder[fu.folder] = append(byFolder[fu.folder], fu.uid)
	}
	var candidates []folderUID
	for folder, uids := range byFolder {
		start := len(uids) - need
		if start < 0 {
			start = 0
		}
		for _, uid := range uids[start:] {
			candidates = append(candidates, folderUID{folder: folder, uid: uid})
		}
	}
	envs, err := c.fetchEnvelopesForCandidates(candidates)
	if err != nil {
		return err
	}
	sort.SliceStable(envs, func(i, j int) bool { return messageLess(envs[i], envs[j]) })
	if offset >= len(envs) {
		return nil
	}
	end := offset + limit
	if end > len(envs) {
		end = len(envs)
	}
	for _, env := range envs[offset:end] {
		folder, uid, perr := ParseMessageID(env.ID)
		if perr != nil {
			return perr
		}
		m, ferr := c.fetchOneUID(folder, uid)
		if ferr != nil {
			return ferr
		}
		if !onMsg(m) {
			return nil
		}
	}
	return nil
}

// forEachRecentMatchingRange 是 forEachRecentMatching 的日期区间版本。
// 扫描 MailFolders 各文件夹的最近信封, 合并排序后分页。
// totalOut 非 nil 时写回本地过滤命中的总数(仅统计扫描窗口内的, 这是回退路径的固有限制)。
func (c *Client) forEachRecentMatchingRange(recipient string, limit, offset int, r DateRange, onMsg func(Message) bool, totalOut *int) error {
	type cand struct {
		folder string
		uid    uint32
		date   time.Time
		to     string
	}
	var cands []cand
	recipient = strings.ToLower(recipient)

	// 只扫各文件夹最近 scan 封, 避免全箱
	scanBudget := (offset + limit) * 4
	if scanBudget < 20 {
		scanBudget = 20
	}
	if scanBudget > 120 {
		scanBudget = 120
	}

	for _, folder := range MailFolders {
		mbox, err := c.cli.Select(folder, true)
		if err != nil {
			if isNoSuchFolder(err) {
				continue
			}
			return err
		}
		if mbox.Messages == 0 {
			continue
		}
		scan := scanBudget
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
			if !r.within(when) {
				continue
			}
			cands = append(cands, cand{folder: folder, uid: msg.Uid, date: when, to: to})
		}
		if err := <-done; err != nil {
			return err
		}
	}
	if totalOut != nil {
		*totalOut = len(cands)
	}
	// 新→旧; 同刻按文件夹/UID 稳定排序, 保证翻页一致。
	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].date.Equal(cands[j].date) {
			return cands[i].date.After(cands[j].date)
		}
		if cands[i].folder != cands[j].folder {
			return cands[i].folder < cands[j].folder
		}
		return cands[i].uid > cands[j].uid
	})
	for idx, cd := range cands {
		if idx < offset {
			continue
		}
		if idx >= offset+limit {
			break
		}
		m, ferr := c.fetchOneUID(cd.folder, cd.uid)
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

// fetchOneUID 拉取指定文件夹内单封邮件(含 body preview), 使用 BODY.PEEK 不标已读。
// UID 按文件夹生效, 拉取前必须先 SELECT 对应文件夹。
func (c *Client) fetchOneUID(folder string, uid uint32) (Message, error) {
	if _, err := c.cli.Select(folder, true); err != nil {
		return Message{}, err
	}
	msg, err := c.fetchUIDPreview(uid)
	if err != nil {
		return Message{}, err
	}
	m := toMessageWithBody(msg)
	stampFolder(&m, folder)
	return m, nil
}

// GetFull 获取单封邮件的完整内容(含正文)。
// GetFull 读取 INBOX 中指定 UID 的完整邮件(兼容旧调用方)。
func (c *Client) GetFull(uid uint32) (*FullMessage, error) {
	return c.GetFullFrom("INBOX", uid)
}

// GetFullFrom 读取指定文件夹中 UID 的完整邮件。
// folder 为空时按 INBOX 处理; IMAP UID 按文件夹生效, 跨文件夹必须带 folder。
func (c *Client) GetFullFrom(folder string, uid uint32) (*FullMessage, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.cli.Select(folder, true); err != nil {
		return nil, err
	}

	msg, err := c.fetchUID(uid)
	if err != nil {
		return nil, err
	}
	return toFullMessage(msg), nil
}

// Delete 删除 INBOX 中指定 UID 的邮件(兼容旧调用方)。
func (c *Client) Delete(uid uint32) error {
	return c.DeleteFrom("INBOX", uid)
}

// DeleteFrom 删除指定文件夹中 UID 的邮件。
func (c *Client) DeleteFrom(folder string, uid uint32) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if uid == 0 {
		return fmt.Errorf("邮件 UID 无效")
	}
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.cli.Select(folder, false); err != nil {
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
		slog.Debug("解析 MIME 正文失败", "err", err.Error())
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
			slog.Debug("读取 MIME 部分失败", "err", perr.Error())
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
