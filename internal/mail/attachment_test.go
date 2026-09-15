package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

// 附件检测不能"解析失败就放行": 未加引号却含空格的 filename 会让
// mime.ParseMediaType 报错, 此时若当成正文, 附件内容就会被展示出来。
func TestExtractBodyAttachmentWithUnparseableDisposition(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m", "Content-Type: text/plain; charset=UTF-8",
		"Content-Disposition: attachment; filename=report 2026.txt", "",
		"ATTACHMENT LEAK 111111",
		"--m", "Content-Type: text/plain; charset=UTF-8", "", "real body 222222", "--m--")

	body := extractBody(strings.NewReader(raw))
	if strings.Contains(body, "ATTACHMENT LEAK") {
		t.Fatalf("body = %q, attachment content leaked into body", body)
	}
	if !strings.Contains(body, "real body 222222") {
		t.Fatalf("body = %q, want the inline part", body)
	}
}

// 只有 name= 参数(无 Content-Disposition)是传统附件写法, 同样不能当正文。
func TestExtractBodyNameParamWithoutDisposition(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m", `Content-Type: text/plain; name="notes.txt"`, "",
		"ATTACHMENT LEAK 333333",
		"--m", "Content-Type: text/plain; charset=UTF-8", "", "real body 555555", "--m--")

	body := extractBody(strings.NewReader(raw))
	if strings.Contains(body, "ATTACHMENT LEAK") {
		t.Fatalf("body = %q, attachment content leaked into body", body)
	}
	if !strings.Contains(body, "real body 555555") {
		t.Fatalf("body = %q, want the inline part", body)
	}
}

// 单独一个 name= 附件且没有任何正文时, 正文应为空而不是附件内容。
func TestExtractBodyOnlyNamedAttachmentIsEmpty(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m", `Content-Type: text/plain; name="only.txt"`, "", "ATTACHMENT LEAK 666666", "--m--")
	if body := extractBody(strings.NewReader(raw)); body != "" {
		t.Fatalf("body = %q, want empty for attachment-only mail", body)
	}
}

// 明确声明 inline 的部分仍按正文处理(内联文本不是附件)。
func TestExtractBodyInlineWithFilenameIsKept(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m", "Content-Type: text/plain; charset=UTF-8",
		"Content-Disposition: inline; filename=logo.txt", "",
		"inline text 777777", "--m--")
	if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "inline text 777777") {
		t.Fatalf("body = %q, want inline text kept", body)
	}
}

// 收件时间过滤/排序必须用 INTERNALDATE, 而不是发件人可伪造的 Date 头:
// 否则一个时间写错的 Date 头会让刚到的邮件被 days 过滤丢掉。
func TestReceivedDateUsesInternalDateOverEnvelopeDate(t *testing.T) {
	stale := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	internal := time.Now().Add(-1 * time.Hour)
	msg := &imap.Message{
		InternalDate: internal,
		Envelope:     &imap.Envelope{Date: stale},
	}

	if got := receivedDate(msg); !got.Equal(internal) {
		t.Fatalf("receivedDate = %v, want INTERNALDATE %v", got, internal)
	}
	// 展示仍用发件人声明的时间
	if got := messageDate(msg); !got.Equal(stale) {
		t.Fatalf("messageDate = %v, want envelope date %v", got, stale)
	}

	// INTERNALDATE 缺失时回退 Envelope.Date, 以便过滤仍有依据
	noInternal := &imap.Message{Envelope: &imap.Envelope{Date: stale}}
	if got := receivedDate(noInternal); !got.Equal(stale) {
		t.Fatalf("receivedDate(fallback) = %v, want %v", got, stale)
	}
}

// days 过滤的语义回归: 刚到的邮件(INTERNALDATE=现在)即使 Date 头很旧也要保留。
func TestRecencyFilterKeepsFreshMailWithStaleDateHeader(t *testing.T) {
	const days = 7
	stale := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := &imap.Message{
		InternalDate: time.Now().Add(-time.Hour),
		Envelope:     &imap.Envelope{Date: stale},
	}
	when := receivedDate(msg)
	if when.IsZero() || time.Since(when) > time.Duration(days)*24*time.Hour {
		t.Fatalf("fresh mail filtered out: when = %v", when)
	}
	// 反过来: 真正老的信(INTERNALDATE 很旧)必须被过滤掉, 即使 Date 头是新的
	old := &imap.Message{
		InternalDate: time.Now().AddDate(0, 0, -30),
		Envelope:     &imap.Envelope{Date: time.Now()},
	}
	whenOld := receivedDate(old)
	if time.Since(whenOld) <= time.Duration(days)*24*time.Hour {
		t.Fatalf("old mail should be filtered: when = %v", whenOld)
	}
}

// 单部分 text/plain 带 name= 参数时不能当成附件, 否则正文会被清空。
func TestExtractBodySinglePartWithNameParamKept(t *testing.T) {
	raw := mimeMessage([]string{`Content-Type: text/plain; charset=UTF-8; name="message.txt"`},
		"single part body 888888")
	if body := extractBody(strings.NewReader(raw)); !strings.Contains(body, "single part body 888888") {
		t.Fatalf("body = %q, want single-part body kept despite name= param", body)
	}
}
