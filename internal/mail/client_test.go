package mail

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

// mimeMessage 拼一封 CRLF 换行的原始邮件。
func mimeMessage(headers []string, body ...string) string {
	all := append(append([]string{}, headers...), "")
	all = append(all, body...)
	return strings.Join(all, "\r\n") + "\r\n"
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

const otpHTML = `<!DOCTYPE html><html><head><style>body{font-family:Söhne}</style></head>
<body><p>Your Apple&nbsp;ID code is <b>123456</b>.</p><p>Ignore this email if you did not request it.</p></body></html>`

// 真实邮件里最常见的 multipart/alternative(纯文本 + HTML 两种编码)必须解析成可读文本,
// 而不是把 MIME 边界和 base64 原文当成正文。
func TestExtractBodyMultipartAlternative(t *testing.T) {
	raw := mimeMessage([]string{
		"From: Apple <no_reply@email.apple.com>",
		"To: alias123@icloud.com",
		"Subject: =?UTF-8?B?6aqM6K+B56CB?=",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="b1"`,
	},
		"--b1",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Your Apple ID code is 123456.",
		"--b1",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
		"",
		b64(otpHTML),
		"--b1--",
	)

	body, contentType := extractBodyWithType(strings.NewReader(raw))
	if body != "Your Apple ID code is 123456." {
		t.Fatalf("body = %q, want plain-text part", body)
	}
	if !strings.HasPrefix(contentType, "multipart/alternative") {
		t.Fatalf("contentType = %q, want top-level Content-Type", contentType)
	}
	if strings.Contains(body, "b1") || strings.Contains(body, "PCFET0NUWVBF") {
		t.Fatalf("body still contains MIME framing: %q", body)
	}
}

// 只有 HTML 部分时转成纯文本, 且要保留验证码。
func TestExtractBodyHTMLOnly(t *testing.T) {
	raw := mimeMessage([]string{
		"From: a@example.com",
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
	}, otpHTML)

	body := extractBody(strings.NewReader(raw))
	if !strings.Contains(body, "123456") {
		t.Fatalf("body = %q, want OTP text", body)
	}
	if strings.Contains(body, "<b>") || strings.Contains(body, "font-family") {
		t.Fatalf("body still contains HTML/CSS: %q", body)
	}
}

// Content-Transfer-Encoding 值大小写不敏感(Quoted-Printable / BASE64)。
func TestExtractBodyTransferEncodingCase(t *testing.T) {
	raw := mimeMessage([]string{
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: Quoted-Printable",
	}, "=E9=AA=8C=E8=AF=81=E7=A0=81=EF=BC=9A123456")

	if body := extractBody(strings.NewReader(raw)); body != "验证码：123456" {
		t.Fatalf("body = %q, want decoded quoted-printable", body)
	}

	raw = mimeMessage([]string{
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: BASE64",
	}, b64("验证码：123456"))

	if body := extractBody(strings.NewReader(raw)); body != "验证码：123456" {
		t.Fatalf("body = %q, want decoded base64", body)
	}
}

// 非 UTF-8 字符集必须转成 UTF-8, 否则中文正文是乱码。
func TestExtractBodyCharsetConversion(t *testing.T) {
	gbk := []byte{0xD1, 0xE9, 0xD6, 0xA4, 0xC2, 0xEB, 0x31, 0x32, 0x33, 0x34} // "验证码1234"
	raw := mimeMessage([]string{
		"Content-Type: text/plain; charset=GBK",
		"Content-Transfer-Encoding: base64",
	}, base64.StdEncoding.EncodeToString(gbk))

	if body := extractBody(strings.NewReader(raw)); body != "验证码1234" {
		t.Fatalf("body = %q, want UTF-8 converted GBK text", body)
	}
}

// 无 Content-Type 时按 RFC 2045 视为 text/plain。
func TestExtractBodyDefaultsToPlainText(t *testing.T) {
	raw := mimeMessage([]string{"Subject: t"}, "hello 世界")
	if body := extractBody(strings.NewReader(raw)); body != "hello 世界" {
		t.Fatalf("body = %q, want raw plain text", body)
	}
}

// 附件不得被当成正文; 正文优先于附件出现时也要选正文。
func TestExtractBodySkipsAttachments(t *testing.T) {
	raw := mimeMessage([]string{
		`Content-Type: multipart/mixed; boundary="m1"`,
	},
		"--m1",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Disposition: attachment; filename=notes.txt",
		"",
		"attachment content 999999",
		"--m1",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"real body 123456",
		"--m1--",
	)

	body := extractBody(strings.NewReader(raw))
	if !strings.Contains(body, "real body 123456") || strings.Contains(body, "attachment content") {
		t.Fatalf("body = %q, want inline part only", body)
	}
}

// 嵌套 multipart 也要展开。
func TestExtractBodyNestedMultipart(t *testing.T) {
	raw := mimeMessage([]string{
		`Content-Type: multipart/mixed; boundary="outer"`,
	},
		"--outer",
		`Content-Type: multipart/alternative; boundary="inner"`,
		"",
		"--inner",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"nested otp 654321",
		"--inner--",
		"--outer",
		"Content-Type: application/pdf; name=x.pdf",
		"Content-Transfer-Encoding: base64",
		"",
		b64("%PDF-1.4 fake"),
		"--outer--",
	)

	if body := extractBody(strings.NewReader(raw)); body != "nested otp 654321" {
		t.Fatalf("body = %q, want nested plain text", body)
	}
}

// 损坏的正文不能 panic, 也不能返回 MIME 原文。
func TestExtractBodyBrokenInput(t *testing.T) {
	for _, raw := range []string{
		"",
		"not a message",
		`Content-Type: multipart/alternative; boundary="x"` + "\r\n\r\n--x\r\n",
		"Content-Type: text/plain; charset=unknown-charset\r\n\r\nbody",
	} {
		if body := extractBody(strings.NewReader(raw)); strings.Contains(body, "boundary") {
			t.Fatalf("extractBody(%q) = %q, leaked MIME framing", raw, body)
		}
	}
}

// Preview 使用解析后的正文, 而不是 MIME 原文。
func TestToMessageWithBodyFillsPreview(t *testing.T) {
	raw := mimeMessage([]string{
		`Content-Type: multipart/alternative; boundary="b1"`,
	},
		"--b1",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"preview text 123456",
		"--b1--",
	)

	// 服务端响应统一回 BODY[](Peek 在解析后的 section 名里为 false)
	section := &imap.BodySectionName{}
	msg := &imap.Message{
		Uid:  7,
		Body: map[*imap.BodySectionName]imap.Literal{section: strings.NewReader(raw)},
		Envelope: &imap.Envelope{
			Subject: "主题",
			Date:    time.Now().Add(-time.Hour),
		},
	}
	got := toMessageWithBody(msg)
	if got.Preview != "preview text 123456" {
		t.Fatalf("Preview = %q, want readable body", got.Preview)
	}
	if got.ID != "7" || got.Subject != "主题" {
		t.Fatalf("message summary lost fields: %+v", got)
	}
}

// Envelope 没有日期时回退 INTERNALDATE, 否则前端日期列空白、days 过滤失效。
func TestToMessageFallsBackToInternalDate(t *testing.T) {
	internal := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	msg := &imap.Message{
		Uid:          3,
		InternalDate: internal,
		Envelope:     &imap.Envelope{Subject: "s"},
	}
	got := toMessage(msg)
	if got.Date != internal.Format(time.RFC3339) {
		t.Fatalf("Date = %q, want INTERNALDATE %q", got.Date, internal.Format(time.RFC3339))
	}
	if !messageDate(msg).Equal(internal) {
		t.Fatalf("messageDate() = %v, want %v", messageDate(msg), internal)
	}
}

// messageDate 优先 Envelope.Date。
func TestMessageDatePrefersEnvelopeDate(t *testing.T) {
	sent := time.Now().Add(-24 * time.Hour)
	internal := time.Now()
	msg := &imap.Message{InternalDate: internal, Envelope: &imap.Envelope{Date: sent}}
	if !messageDate(msg).Equal(sent) {
		t.Fatalf("messageDate() = %v, want envelope date %v", messageDate(msg), sent)
	}
}

// testLiteral 实现 imap.Literal, 用于把原始邮件当作服务端返回的字面量。
type testLiteral struct{ r *strings.Reader }

func (l testLiteral) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l testLiteral) Len() int                   { return l.r.Len() }

func newTestLiteral(s string) testLiteral { return testLiteral{r: strings.NewReader(s)} }

// parseFetchResponse 用 go-imap 的响应解析器从 FETCH 字段构造消息。
// 注意: 字段值必须是原生 string(RawString 只用于字段名, Parse 内部按 string 断言)。
func parseFetchResponse(t *testing.T, fields ...interface{}) *imap.Message {
	t.Helper()
	msg := &imap.Message{}
	if err := msg.Parse(fields); err != nil {
		t.Fatalf("Parse(FETCH response): %v", err)
	}
	return msg
}

// 邮件详情走 RFC822 请求, 服务端回的 section 名是 "RFC822"。
// 这里用 go-imap 自己的响应解析器构造消息, 验证 GetFull 的取正文路径真的能拿到字节,
// 而不是静默返回空正文(用户就看不到验证码了)。
func TestToFullMessageFromRFC822Response(t *testing.T) {
	raw := mimeMessage([]string{
		"From: Apple <no_reply@email.apple.com>",
		"To: alias123@icloud.com",
		"Subject: =?UTF-8?B?6aqM6K+B56CB?=",
		"Date: Mon, 14 Sep 2026 10:00:00 +0800",
		`Content-Type: multipart/alternative; boundary="b1"`,
	},
		"--b1",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"verification code 123456",
		"--b1--",
	)

	// 服务端对 "UID RFC822 ENVELOPE INTERNALDATE" 的响应顺序
	msg := parseFetchResponse(t,
		imap.RawString("UID"), imap.RawString("42"),
		imap.RawString("RFC822"), newTestLiteral(raw),
		imap.RawString("INTERNALDATE"), "14-Sep-2026 10:00:00 +0800",
	)

	full := toFullMessage(msg)
	if full.ID != "42" {
		t.Fatalf("ID = %q, want 42", full.ID)
	}
	if full.Body != "verification code 123456" {
		t.Fatalf("Body = %q, want plain-text body from RFC822 literal", full.Body)
	}
	if !strings.HasPrefix(full.ContentType, "multipart/alternative") {
		t.Fatalf("ContentType = %q, want multipart/alternative", full.ContentType)
	}
	if full.Date == "" {
		t.Fatal("Date empty: should fall back to INTERNALDATE")
	}
}

// 用 BODY.PEEK[] 请求时, 服务端回的 section 名仍是 "BODY[]"(RFC 3501, PEEK 只是请求修饰符),
// 且查询侧的 Peek 会被归一化, 所以请求侧的 Peek 不影响取到正文。
func TestToFullMessageFromPeekRequestBodySection(t *testing.T) {
	raw := mimeMessage([]string{
		"Subject: plain",
		"Content-Type: text/plain; charset=UTF-8",
	}, "otp 654321")

	// 请求侧用 BODY.PEEK[], 响应侧按 RFC 回 BODY[]
	requestSection := &imap.BodySectionName{Peek: true}
	if got := string(requestSection.FetchItem()); got != "BODY.PEEK[]" {
		t.Fatalf("request fetch item = %q, want BODY.PEEK[]", got)
	}
	responseSection, err := imap.ParseBodySectionName(imap.FetchItem("BODY[]"))
	if err != nil {
		t.Fatalf("ParseBodySectionName: %v", err)
	}

	// 正文 literal 是一次性 reader, 每封邮件只能被解析一次, 所以每条断言各起一封。
	parseResponse := func() *imap.Message {
		return parseFetchResponse(t,
			imap.RawString("UID"), imap.RawString("7"),
			imap.RawString(responseSection.FetchItem()), newTestLiteral(raw),
		)
	}

	full := toFullMessage(parseResponse())
	if full.Body != "otp 654321" {
		t.Fatalf("Body = %q, want body from BODY[] literal", full.Body)
	}

	// 摘要路径同样要拿到正文
	preview := toMessageWithBody(parseResponse())
	if preview.Preview != "otp 654321" {
		t.Fatalf("Preview = %q, want body text", preview.Preview)
	}
}

// 正文 literal 只能读一次: 第二次读同一条消息拿不到内容(生产路径每封只解析一次)。
func TestBodyLiteralIsSingleUse(t *testing.T) {
	raw := mimeMessage([]string{"Content-Type: text/plain; charset=UTF-8"}, "once only")
	msg := parseFetchResponse(t,
		imap.RawString("UID"), imap.RawString("1"),
		imap.RawString("BODY[]"), newTestLiteral(raw),
	)
	if first := toFullMessage(msg); first.Body != "once only" {
		t.Fatalf("first read = %q, want body", first.Body)
	}
	if second := toFullMessage(msg); second.Body != "" {
		t.Fatalf("second read = %q, want empty (literal already consumed)", second.Body)
	}
}

// 没有任何正文 section 时, Body 为空但 ContentType 不应 panic; 摘要字段仍然可用。
func TestToFullMessageWithoutBodySection(t *testing.T) {
	msg := parseFetchResponse(t,
		imap.RawString("UID"), imap.RawString("9"),
		imap.RawString("INTERNALDATE"), "14-Sep-2026 10:00:00 +0800",
	)
	full := toFullMessage(msg)
	if full.Body != "" || full.ContentType != "" {
		t.Fatalf("Body/ContentType = %q/%q, want empty", full.Body, full.ContentType)
	}
	if full.ID != "9" {
		t.Fatalf("ID = %q, want 9", full.ID)
	}
}

// 回归: 查看邮件详情不得把邮件标记为已读。
// imap.FetchRFC822 等价于 BODY[](不带 PEEK), 会让服务端置 \Seen; 必须用 BODY.PEEK[]。
func TestFullMessageFetchUsesPeek(t *testing.T) {
	var bodyItem string
	for _, item := range fullFetchItems {
		if strings.Contains(string(item), "BODY") {
			bodyItem = string(item)
		}
		if item == imap.FetchRFC822 || item == imap.FetchRFC822Text {
			t.Fatalf("fullFetchItems 含 %q: 会标记邮件为已读", item)
		}
	}
	if bodyItem != "BODY.PEEK[]" {
		t.Fatalf("fullFetchItems 正文项 = %q, want BODY.PEEK[]", bodyItem)
	}
}

// 回归: 摘要路径必须用部分抓取(头字段 + TEXT<0.N> 两个小 section),
// 且同样不得标已读。全量下载是收件箱列表慢的主因(见 previewTextSection 注释)。
func TestPreviewFetchUsesPartialPeek(t *testing.T) {
	var bodyItems []string
	for _, item := range previewFetchItems {
		s := string(item)
		if strings.Contains(s, "BODY") {
			bodyItems = append(bodyItems, s)
		}
		if item == imap.FetchRFC822 || item == imap.FetchRFC822Text {
			t.Fatalf("previewFetchItems 含 %q: 会标记邮件为已读", item)
		}
	}
	wantText := fmt.Sprintf("BODY.PEEK[TEXT]<0.%d>", previewPartLimit)
	joined := strings.Join(bodyItems, " ")
	if !strings.Contains(joined, wantText) {
		t.Fatalf("previewFetchItems 正文项 = %q, want 含 %q", joined, wantText)
	}
	if !strings.Contains(joined, "BODY.PEEK[HEADER.FIELDS") {
		t.Fatalf("previewFetchItems 缺 HEADER.FIELDS section: %q", joined)
	}
}

// 回归: 收件箱摘要的 FETCH 项同样必须用 PEEK, 且 days 过滤依赖 INTERNALDATE 存在。
func TestInboxFetchItemsShape(t *testing.T) {
	if got := string(peekBodySection.FetchItem()); got != "BODY.PEEK[]" {
		t.Fatalf("peekBodySection.FetchItem() = %q, want BODY.PEEK[]", got)
	}
}
