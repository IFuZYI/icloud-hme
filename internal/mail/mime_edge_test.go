package mail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap"
)

// alternativeMessage 构造 multipart/alternative 邮件, htmlFirst 控制 HTML 部分在前还是在后。
func alternativeMessage(htmlFirst bool, plainPart, htmlPart []string) string {
	parts := [][]string{plainPart, htmlPart}
	if htmlFirst {
		parts[0], parts[1] = parts[1], parts[0]
	}
	var body []string
	for _, p := range parts {
		body = append(body, "--b")
		body = append(body, p...)
	}
	body = append(body, "--b--")
	return mimeMessage([]string{`Content-Type: multipart/alternative; boundary="b"`}, body...)
}

// 转发邮件(message/rfc822)里的验证码必须能读到, 否则整封邮件显示"无正文"。
func TestExtractBodyForwardedMessage(t *testing.T) {
	inner := mimeMessage([]string{
		"From: Apple <no_reply@email.apple.com>",
		"Subject: =?UTF-8?B?6aqM6K+B56CB?=",
		"Content-Type: text/plain; charset=UTF-8",
	}, "forwarded otp 111111")

	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m",
		"Content-Type: message/rfc822",
		"",
		strings.TrimRight(inner, "\r\n"),
		"--m--",
	)

	body := extractBody(strings.NewReader(raw))
	if !strings.Contains(body, "forwarded otp 111111") {
		t.Fatalf("body = %q, want forwarded inner text", body)
	}
}

// 多层嵌套的 message/rfc822 不能无限递归。
func TestExtractBodyDeeplyNestedMessageRFCCapped(t *testing.T) {
	body := "deep otp 999999"
	msg := mimeMessage([]string{"Content-Type: text/plain; charset=UTF-8"}, body)
	for i := 0; i < maxMIMEDepth+3; i++ {
		msg = mimeMessage([]string{`Content-Type: multipart/mixed; boundary="n"`},
			"--n",
			"Content-Type: message/rfc822",
			"",
			strings.TrimRight(msg, "\r\n"),
			"--n--",
		)
	}
	// 只要求不 panic / 不挂死; 深度受限时允许返回空
	got := extractBody(strings.NewReader(msg))
	t.Logf("deeply nested body = %q", got)
}

// 全附件 multipart 应视为无正文, 而不是把附件内容当正文。
func TestExtractBodyAllAttachmentsEmpty(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Disposition: attachment; filename=a.txt",
		"",
		"attachment only 123456",
		"--m--",
	)
	if body := extractBody(strings.NewReader(raw)); body != "" {
		t.Fatalf("body = %q, want empty for attachment-only mail", body)
	}
}

// quoted-printable 软换行不应把验证码拆断。
func TestExtractBodyQuotedPrintableSoftBreak(t *testing.T) {
	raw := mimeMessage([]string{
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
	}, "verification code is 12=\r\n3456 end")
	if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "123456") {
		t.Fatalf("body = %q, want soft-broken code rejoined", body)
	}
}

// 未知字符集名不应导致正文丢失。
func TestExtractBodyUnknownCharsetStillReadable(t *testing.T) {
	raw := mimeMessage([]string{"Content-Type: text/plain; charset=x-unknown-charset"}, "otp 222222")
	if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "222222") {
		t.Fatalf("body = %q, want body despite unknown charset", body)
	}
}

// HTML 带引号包裹的 charset 参数也要能解析。
func TestExtractBodyQuotedCharsetParameter(t *testing.T) {
	raw := mimeMessage([]string{
		`Content-Type: text/html; charset="utf-8"`,
		"Content-Transfer-Encoding: base64",
	}, b64("<p>code <b>333333</b></p>"))
	if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "333333") {
		t.Fatalf("body = %q, want html text", body)
	}
}

// 摘要必须有上限: 列表会为每封邮件解析完整正文, 超大邮件不能产出超大摘要。
func TestCapPreviewBoundsSize(t *testing.T) {
	huge := strings.Repeat("a", 5*previewLimit)
	if got := capPreview(huge); len(got) != previewLimit {
		t.Fatalf("capPreview len = %d, want %d", len(got), previewLimit)
	}

	// 多字节字符不能被截成半个 rune
	multi := strings.Repeat("验", previewLimit+10)
	got := capPreview(multi)
	if !utf8.ValidString(got) {
		t.Fatal("capPreview produced invalid UTF-8")
	}
	if n := utf8.RuneCountInString(got); n != previewLimit {
		t.Fatalf("capPreview runes = %d, want %d", n, previewLimit)
	}

	// 短摘要原样返回
	if short := capPreview("短摘要 123456"); short != "短摘要 123456" {
		t.Fatalf("capPreview(short) = %q, want unchanged", short)
	}
}

// 摘要路径必须应用上限, 而详情正文不截断。
func TestPreviewCappedButFullBodyNotTruncated(t *testing.T) {
	long := strings.Repeat("x", 3*previewLimit)
	raw := mimeMessage([]string{"Content-Type: text/plain; charset=UTF-8"}, long)

	// literal 一次性: 每条消息都要用新的 reader
	parse := func() *imap.Message {
		return parseFetchResponse(t,
			imap.RawString("UID"), imap.RawString("5"),
			imap.RawString("BODY[]"), newTestLiteral(raw),
		)
	}

	if got := len(toMessageWithBody(parse()).Preview); got != previewLimit {
		t.Fatalf("Preview len = %d, want capped at %d", got, previewLimit)
	}
	// 详情路径不截断
	if got := len(toFullMessage(parse()).Body); got != len(long) {
		t.Fatalf("Body len = %d, want full %d (detail view must not truncate)", got, len(long))
	}
}

// 摘要路径只读每个部分的前 previewPartLimit 字节; 详情路径不受影响。
func TestPreviewPartByteCap(t *testing.T) {
	big := strings.Repeat("x", previewPartLimit+50)
	raw := mimeMessage([]string{"Content-Type: text/plain; charset=UTF-8"}, big)

	// literal 一次性: 每条消息都要用新的 reader
	parse := func() *imap.Message {
		return parseFetchResponse(t,
			imap.RawString("UID"), imap.RawString("5"),
			imap.RawString("BODY[]"), newTestLiteral(raw),
		)
	}

	// 摘要路径: 读到 previewPartLimit 字节即止(随后被 capPreview 截到 previewLimit)
	if got := len(toMessageWithBody(parse()).Preview); got != previewLimit {
		t.Fatalf("Preview len = %d, want capped at %d", got, previewLimit)
	}
	// 详情路径: 完整正文
	if got := len(toFullMessage(parse()).Body); got != len(big) {
		t.Fatalf("Body len = %d, want full %d (detail view must not truncate)", got, len(big))
	}
}

// plain 已存在时跳过 HTML 解析不得改变结果: 无论 part 顺序, text/plain 都优先。
func TestExtractBodyPlainWinsRegardlessOfPartOrder(t *testing.T) {
	plainPart := []string{"Content-Type: text/plain; charset=UTF-8", "", "plain 123456"}
	htmlPart := []string{"Content-Type: text/html; charset=UTF-8", "Content-Transfer-Encoding: base64", "", b64("<p>html 999999</p>")}

	for _, htmlFirst := range []bool{false, true} {
		body := extractBody(strings.NewReader(alternativeMessage(htmlFirst, plainPart, htmlPart)))
		if !strings.Contains(body, "plain 123456") {
			t.Fatalf("htmlFirst=%v: body = %q, want plain part", htmlFirst, body)
		}
		if strings.Contains(body, "999999") {
			t.Fatalf("htmlFirst=%v: body = %q, must not contain HTML text", htmlFirst, body)
		}
	}
}

// bigHTMLMessage 构造 multipart/alternative, HTML 部分约 1MB。
func bigHTMLMessage(htmlFirst bool) string {
	big := "<html><head><style>body{font-family:Söhne}</style></head><body>" +
		strings.Repeat("<p>filler content for the html part</p>", 26000) +
		"<p>code 123456</p></body></html>"
	plainPart := []string{"Content-Type: text/plain; charset=UTF-8", "", "Your code is 123456."}
	htmlPart := []string{"Content-Type: text/html; charset=UTF-8", "", big}
	return alternativeMessage(htmlFirst, plainPart, htmlPart)
}

// 性能回归护栏: 有 text/plain 时不得为 HTML 部分付出 sanitizePreview 的代价。
// 实测(1MB HTML): plain 在前 ≈1ms, plain 在后 ≈1ms; 若退化为解析 HTML 则 ≈150ms。
func BenchmarkExtractBodyPlainBeforeBigHTML(b *testing.B) {
	raw := bigHTMLMessage(false)
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "123456") {
			b.Fatalf("body = %q", body)
		}
	}
}

func BenchmarkExtractBodyBigHTMLBeforePlain(b *testing.B) {
	raw := bigHTMLMessage(true)
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "123456") {
			b.Fatalf("body = %q", body)
		}
	}
}

// HTML-only 仍必须转换(此时才值得付解析成本)。
// 摘要路径每个 MIME 部分最多读 previewPartLimit(128KB): 更长的 HTML 会被截断,
// 这是设计内的行为(摘要最终只保留 previewLimit=2000 字符), 因此这里只断言
// 前缀正确且不含 MIME 框架, 不断言邮件尾部内容。
func BenchmarkExtractBodyHTMLOnlyBig(b *testing.B) {
	big := strings.Repeat("<p>filler content for the html part</p>", 26000) + "<p>code 123456</p>"
	raw := mimeMessage([]string{"Content-Type: text/html; charset=UTF-8"}, big)
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		body := extractBody(strings.NewReader(raw))
		if !strings.Contains(body, "filler content for the html part") {
			b.Fatalf("body = %q", body)
		}
		if strings.Contains(body, "<p>") || strings.Contains(body, "boundary") {
			b.Fatalf("body = %q, still contains HTML/MIME framing", body)
		}
	}
}
