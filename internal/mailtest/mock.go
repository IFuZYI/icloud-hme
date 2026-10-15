// Package mailtest 提供进程内 IMAP 服务器与拨号辅助, 供其它包的测试
// 通过 mail.OverrideDialForTest 注入, 覆盖真实服务器行为(网易 ID 门禁等)。
package mailtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// Options 控制模拟服务器行为。
type Options struct {
	// RequireID 网易行为: 未收到非空 ID 前所有 SELECT/EXAMINE 返回
	// errors.New("Unsafe Login. Please contact kefu@188.com for help")(措辞必须与真实一致)。
	RequireID bool
	// RejectID 声明 ID 能力但对 ID 命令回 NO(验证 best-effort 不炸)。
	RejectID bool
	// HideID 不声明 ID 能力(iCloud 行为); 仍注册 ID 处理以便记录「客户端是否发送了 ID」。
	HideID bool
	// NetEaseFolderErr 缺失文件夹返回 "Folder not exist"(163 实测措辞)
	// 而非 memory 后端默认的 "No such mailbox"。
	NetEaseFolderErr bool
	// JunkFolder 非空时创建该名字的文件夹(如「垃圾邮件」)。
	JunkFolder string
	// EmptyHeaderSearch 复刻网易 Coremail 的 SEARCH 缺陷: 对 header 类条件
	// (TO/FROM/SUBJECT/HEADER/TEXT/BODY)静默返回空结果且不报错(163 实测,
	// 原始协议确认)。ALL/SINCE/BEFORE/UID 等非 header 条件不受影响。
	EmptyHeaderSearch bool
	// SelectDelay 人为放慢每个 SELECT/EXAMINE 响应, 用于并发回源合并测试:
	// 保证多个并发请求在第一个回源完成前真正重叠(否则时序上无法区分
	// 「合并回源」与「恰好串行」)。
	SelectDelay time.Duration
	// DovecotFolderErr 缺失文件夹返回 "Mailbox doesn't exist: <名>"
	// (Dovecot 实测措辞; iCloud/网易之外第三种常见形态)。
	DovecotFolderErr bool
	// UidValidity 非零时覆盖各文件夹 STATUS/SELECT 报告的 UIDVALIDITY
	// (测试「邮箱被服务端重建」的检测: 变化后旧 UID 不再指向同一邮件)。
	UidValidity uint32
}

// Server 是进程内自签证书 IMAP 服务器。
type Server struct {
	Host    string
	Port    int
	Backend *memory.Backend // 供测试直接投放邮件/建文件夹
	idRecv  atomic.Bool
	// validity 覆盖 UIDVALIDITY(见 Options.UidValidity); 测试运行中可变。
	validity atomic.Uint32
	// connCount 统计接受过的连接数; selCount 统计收到的 SELECT/EXAMINE 命令数;
	// searchCount 统计 SEARCH(含 UID SEARCH) 命令数; fetchCount 统计 UID FETCH
	// 命令数; fetchSeqCount 统计非 UID 的 FETCH(回退扫描路径); noopCount 统计
	// NOOP 数。
	// 用于断言「连接复用」(不应反复 TLS+LOGIN) 与「读信缓存」(不应重复扫箱)。
	connCount     atomic.Int64
	selCount      atomic.Int64
	searchCount   atomic.Int64
	fetchCount    atomic.Int64
	fetchSeqCount atomic.Int64
	noopCount     atomic.Int64
}

// ReceivedID 返回客户端是否发送过非空 ID 声明。
func (s *Server) ReceivedID() bool { return s.idRecv.Load() }

// SetUidValidity 运行中修改服务器报告的 UIDVALIDITY(测试邮箱重建场景)。
func (s *Server) SetUidValidity(v uint32) { s.validity.Store(v) }

// ConnCount 返回服务器接受过的连接总数。
func (s *Server) ConnCount() int64 { return s.connCount.Load() }

// SelectCount 返回服务器收到的 SELECT/EXAMINE 命令总数(含被拒绝的)。
func (s *Server) SelectCount() int64 { return s.selCount.Load() }

// NoopCount 返回服务器收到的 NOOP 命令总数(连接保活断言用)。
func (s *Server) NoopCount() int64 { return s.noopCount.Load() }

// SearchCount 返回服务器收到的 SEARCH/UID SEARCH 命令总数
// (UID 快照断言用: 翻页/增量校验不应重复全量 SEARCH)。
func (s *Server) SearchCount() int64 { return s.searchCount.Load() }

// FetchCount 返回服务器收到的 UID FETCH 命令总数
// (回退扫描缓存断言用: 重复查询不应重复扫箱)。
func (s *Server) FetchCount() int64 { return s.fetchCount.Load() }

// FetchSeqCount 返回服务器收到的非 UID FETCH(序列号)命令总数,
// 即回退扫描路径的扫箱次数(UID 快照断言用)。
func (s *Server) FetchSeqCount() int64 { return s.fetchSeqCount.Load() }

// NewServer 启动一台进程内 IMAP 服务器, 测试结束自动关闭。
func NewServer(t *testing.T, opts Options) *Server {
	t.Helper()
	be := memory.New()
	// memory.New() 预置了一封 2016 年的 INBOX 邮件; 清掉以保证测试可控。
	user, err := be.Login(nil, "username", "password")
	if err != nil {
		t.Fatal(err)
	}
	if mb, err := user.GetMailbox("INBOX"); err == nil {
		if mm, ok := mb.(*memory.Mailbox); ok {
			mm.Messages = nil
		}
	}
	if opts.JunkFolder != "" {
		if err := user.CreateMailbox(opts.JunkFolder); err != nil {
			t.Fatalf("创建文件夹 %s: %v", opts.JunkFolder, err)
		}
	}

	s := &Server{Host: "127.0.0.1", Backend: be}
	s.validity.Store(opts.UidValidity)
	// 统一用包装后端: memory 后端只认内置凭据(username/password),
	// 而测试客户端用真实风格凭据登录; 测试聚焦 ID 门禁与文件夹行为,
	// 凭据校验由真实 163 的 env 门控 e2e 测试覆盖。
	srv := server.New(wrapperBackend{
		inner:             be,
		neteaseErr:        opts.NetEaseFolderErr,
		dovecotErr:        opts.DovecotFolderErr,
		emptyHeaderSearch: opts.EmptyHeaderSearch,
		validity:          &s.validity,
	})
	srv.AllowInsecureAuth = true
	srv.Enable(&ext{opts: opts, srv: s})

	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{genCert(t)}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	s.Port = port
	return s
}

// AddMessage 往指定文件夹投放一封测试邮件(参照 junk_folder_test.go 的写法)。
func (s *Server) AddMessage(t *testing.T, folder, from, to, subject string, when time.Time) {
	t.Helper()
	user, err := s.Backend.Login(nil, "username", "password")
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

// DialInsecure 拨号 TLS 并跳过证书校验(自签证书), 供 mail.OverrideDialForTest 注入。
func DialInsecure(addr string) (*client.Client, error) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	return client.New(conn)
}

// genCert 生成进程内自签证书(照抄 internal/mail/preview_e2e_test.go 的写法)。
func genCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// ---- 扩展(server.Extension + server.ConnExtension 双实现) ----

type ext struct {
	opts Options
	srv  *Server
}

func (e *ext) Capabilities(server.Conn) []string {
	if e.opts.HideID {
		return nil
	}
	return []string{"ID"}
}

func (e *ext) Command(name string) server.HandlerFactory {
	switch name {
	case "ID":
		return func() server.Handler { return &idHandler{ext: e} }
	case "NOOP":
		// 统计 NOOP(连接保活测试), 其余行为委托内置实现。
		return func() server.Handler { return &noopHandler{ext: e} }
	case "SEARCH":
		// 统计 SEARCH / UID SEARCH(UID 分发会经 extensions 查内层命令),
		// 其余行为委托内置实现。
		return func() server.Handler { return &searchHandler{ext: e} }
	case "FETCH":
		// 统计 FETCH / UID FETCH(回退扫描缓存断言), 其余行为委托内置实现。
		return func() server.Handler { return &fetchHandler{ext: e} }
	case "SELECT", "EXAMINE":
		// 始终包装 SELECT/EXAMINE: 统计命令数(断言读信缓存), 并按需执行
		// 网易式 ID 门禁(未声明 ID 时拒绝)。
		readOnly := name == "EXAMINE"
		return func() server.Handler {
			h := &selectHandler{ext: e}
			h.ReadOnly = readOnly
			return h
		}
	}
	return nil
}

func (e *ext) NewConn(c server.Conn) server.Conn {
	e.srv.connCount.Add(1)
	return &conn{Conn: c}
}

// conn 是连接包装: 记录本连接是否已完成 ID 声明(网易门禁按连接生效)。
type conn struct {
	server.Conn
	idSent bool
}

// selectHandler 统计收到的 SELECT/EXAMINE; RequireID 时在未收到 ID 声明前
// 拒绝之, 复刻网易 Coremail 行为。
type selectHandler struct {
	ext *ext
	server.Select
}

func (h *selectHandler) Handle(c server.Conn) error {
	if ic, ok := c.(*conn); ok && h.ext.opts.RequireID && !ic.idSent {
		return errors.New("Unsafe Login. Please contact kefu@188.com for help")
	}
	h.ext.srv.selCount.Add(1)
	// SelectDelay 人为放慢响应, 让并发回源合并测试的请求窗口真实重叠。
	if d := h.ext.opts.SelectDelay; d > 0 {
		time.Sleep(d)
	}
	return h.Select.Handle(c)
}

// noopHandler 统计 NOOP 命令数(连接保活测试), 其余行为委托内置实现。
type noopHandler struct {
	ext *ext
	server.Noop
}

func (h *noopHandler) Handle(c server.Conn) error {
	h.ext.srv.noopCount.Add(1)
	return h.Noop.Handle(c)
}

// searchHandler 统计 SEARCH / UID SEARCH 命令数(UID 快照测试),
// 其余行为委托内置实现。UID SEARCH 经 UID 分发到本 handler 的 UidHandle。
type searchHandler struct {
	ext *ext
	server.Search
}

func (h *searchHandler) Handle(c server.Conn) error {
	h.ext.srv.searchCount.Add(1)
	return h.Search.Handle(c)
}

func (h *searchHandler) UidHandle(c server.Conn) error {
	h.ext.srv.searchCount.Add(1)
	return h.Search.UidHandle(c)
}

// fetchHandler 统计 FETCH(非 UID, 回退扫描)与 UID FETCH 命令数,
// 其余行为委托内置实现。
type fetchHandler struct {
	ext *ext
	server.Fetch
}

func (h *fetchHandler) Handle(c server.Conn) error {
	h.ext.srv.fetchSeqCount.Add(1)
	return h.Fetch.Handle(c)
}

func (h *fetchHandler) UidHandle(c server.Conn) error {
	h.ext.srv.fetchCount.Add(1)
	return h.Fetch.UidHandle(c)
}

// idHandler 记录并(可选)接受 ID 命令。
type idHandler struct {
	ext      *ext
	nonEmpty bool
}

func (h *idHandler) Parse(fields []interface{}) error {
	if len(fields) < 1 {
		return nil
	}
	if list, ok := fields[0].([]interface{}); ok && len(list) > 0 {
		h.nonEmpty = true
	}
	return nil
}

func (h *idHandler) Handle(c server.Conn) error {
	if !h.nonEmpty {
		return nil
	}
	h.ext.srv.idRecv.Store(true)
	if h.ext.opts.RejectID {
		return errors.New("ID not supported")
	}
	if ic, ok := c.(*conn); ok {
		ic.idSent = true
	}
	// 真实服务器会回一条 untagged ID 响应; 客户端应忽略它。
	_ = c.WriteResp(imap.NewUntaggedResp([]interface{}{
		imap.RawString("ID"),
		[]interface{}{"name", "mailtest", "vendor", "mailtest"},
	}))
	return nil
}

// wrapperBackend/wrapperUser 包装 memory 后端, 使测试可以用真实风格凭据
// (如 user@163.com / auth-code)登录, 并可选把缺失文件夹错误换成网易措辞。
//
// memory 后端只认内置凭据(username/password); 而本包面向「协议行为」测试
// (ID 门禁、文件夹措辞), 凭据校验由真实 163 的 env 门控 e2e 测试覆盖,
// 因此这里放行任意非空凭据。
type wrapperBackend struct {
	inner             *memory.Backend
	neteaseErr        bool
	dovecotErr        bool
	emptyHeaderSearch bool
	// validity 非 nil 时覆盖各邮箱报告的 UIDVALIDITY(测试邮箱重建)。
	validity *atomic.Uint32
}

func (b wrapperBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	if username == "" || password == "" {
		return nil, errors.New("Bad username or password")
	}
	user, err := b.inner.Login(connInfo, "username", "password")
	if err != nil {
		return nil, err
	}
	return wrapperUser{User: user, neteaseErr: b.neteaseErr, dovecotErr: b.dovecotErr, emptyHeaderSearch: b.emptyHeaderSearch, validity: b.validity}, nil
}

type wrapperUser struct {
	backend.User
	neteaseErr        bool
	dovecotErr        bool
	emptyHeaderSearch bool
	validity          *atomic.Uint32
}

func (u wrapperUser) GetMailbox(name string) (backend.Mailbox, error) {
	mb, err := u.User.GetMailbox(name)
	if err != nil {
		if u.neteaseErr {
			return nil, errors.New("Folder not exist")
		}
		if u.dovecotErr {
			return nil, errors.New("Mailbox doesn't exist: " + name)
		}
		return nil, err
	}
	if u.validity != nil && u.validity.Load() != 0 {
		mb = validityMailbox{Mailbox: mb, validity: u.validity}
	}
	if u.emptyHeaderSearch {
		return wrapperMailbox{Mailbox: mb}, nil
	}
	return mb, nil
}

// validityMailbox 覆盖 STATUS 的 UIDVALIDITY(测试邮箱重建场景)。
type validityMailbox struct {
	backend.Mailbox
	validity *atomic.Uint32
}

func (m validityMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	status, err := m.Mailbox.Status(items)
	if err != nil {
		return nil, err
	}
	if v := m.validity.Load(); v != 0 {
		status.UidValidity = v
	}
	return status, nil
}

// wrapperMailbox 复刻网易 Coremail 的 SEARCH 缺陷: header 类条件静默返回空。
type wrapperMailbox struct {
	backend.Mailbox
}

func (m wrapperMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	if len(criteria.Header) > 0 || len(criteria.Text) > 0 || len(criteria.Body) > 0 {
		return []uint32{}, nil
	}
	return m.Mailbox.SearchMessages(uid, criteria)
}
