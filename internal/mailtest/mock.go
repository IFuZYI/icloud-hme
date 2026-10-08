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
}

// Server 是进程内自签证书 IMAP 服务器。
type Server struct {
	Host    string
	Port    int
	Backend *memory.Backend // 供测试直接投放邮件/建文件夹
	idRecv  atomic.Bool
}

// ReceivedID 返回客户端是否发送过非空 ID 声明。
func (s *Server) ReceivedID() bool { return s.idRecv.Load() }

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
	// 统一用包装后端: memory 后端只认内置凭据(username/password),
	// 而测试客户端用真实风格凭据登录; 测试聚焦 ID 门禁与文件夹行为,
	// 凭据校验由真实 163 的 env 门控 e2e 测试覆盖。
	srv := server.New(wrapperBackend{inner: be, neteaseErr: opts.NetEaseFolderErr})
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
	case "SELECT", "EXAMINE":
		if e.opts.RequireID {
			readOnly := name == "EXAMINE"
			return func() server.Handler {
				h := &selectGate{}
				h.ReadOnly = readOnly
				return h
			}
		}
	}
	return nil
}

func (e *ext) NewConn(c server.Conn) server.Conn { return &conn{Conn: c} }

// conn 是连接包装: 记录本连接是否已完成 ID 声明(网易门禁按连接生效)。
type conn struct {
	server.Conn
	idSent bool
}

// selectGate 在未收到 ID 声明时拒绝 SELECT/EXAMINE, 复刻网易 Coremail 行为。
type selectGate struct {
	server.Select
}

func (h *selectGate) Handle(c server.Conn) error {
	if ic, ok := c.(*conn); ok && !ic.idSent {
		return errors.New("Unsafe Login. Please contact kefu@188.com for help")
	}
	return h.Select.Handle(c)
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
	inner      *memory.Backend
	neteaseErr bool
}

func (b wrapperBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	if username == "" || password == "" {
		return nil, errors.New("Bad username or password")
	}
	user, err := b.inner.Login(connInfo, "username", "password")
	if err != nil {
		return nil, err
	}
	return wrapperUser{User: user, neteaseErr: b.neteaseErr}, nil
}

type wrapperUser struct {
	backend.User
	neteaseErr bool
}

func (u wrapperUser) GetMailbox(name string) (backend.Mailbox, error) {
	mb, err := u.User.GetMailbox(name)
	if err != nil {
		if u.neteaseErr {
			return nil, errors.New("Folder not exist")
		}
		return nil, err
	}
	return mb, nil
}
