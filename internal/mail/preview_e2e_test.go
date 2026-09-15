package mail

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// 自签证书(测试进程内生成, 无需文件)。
func genSelfSignedCert(t *testing.T) tls.Certificate {
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

// 端到端: 本地 TLS IMAP 服务器 + 真实 client 协议栈, 验证 previewFetchItems
// 的请求→响应→GetBody 匹配→Preview 提取全链路。
// memory backend 自带 username/password 用户和一封 text/plain 邮件
// (正文 "Hi there :)")。
func TestPreviewE2EAgainstServer(t *testing.T) {
	cert := genSelfSignedCert(t)

	be := memory.New()
	srv := server.New(be)
	srv.AllowInsecureAuth = true
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	// 自签证书: 跳过校验只为走通协议链路
	conn, err := tls.Dial("tcp", "127.0.0.1:"+portStr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Login("username", "password"); err != nil {
		t.Fatalf("login: %v", err)
	}

	c := &Client{cli: cli}
	msgs, total, err := c.ListInboxPage(2, 0, 0)
	if err != nil {
		t.Fatalf("ListInboxPage: %v", err)
	}
	if len(msgs) != 1 || total != 1 {
		t.Fatalf("got %d msgs (total %d), want 1/1", len(msgs), total)
	}
	if msgs[0].Preview != "" {
		// 信封阶段不拉正文; 摘要应由 FetchPreviews 补齐
		t.Errorf("envelope stage Preview = %q, want empty", msgs[0].Preview)
	}
	// 信封阶段 Preview 应为空; 用 FetchPreviews(第二阶段)补齐
	previews, err := c.FetchPreviews([]uint32{6})
	if err != nil {
		t.Fatalf("FetchPreviews: %v", err)
	}
	if !strings.Contains(previews["6"], "Hi there") {
		t.Errorf("previews[6] = %q, want contains 'Hi there'", previews["6"])
	}
}
