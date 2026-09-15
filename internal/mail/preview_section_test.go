package mail

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

// 复现: 服务端响应的两个 section(HEADER.FIELDS + TEXT<0>)能否被摘要路径
// 正确取回、拼装并解析出 Preview。literal 是一次性的, 每条断言各起一封。
func TestPreviewSectionLookup(t *testing.T) {
	rawHeader := "Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"MIME-Version: 1.0\r\n"
	rawText := "<!DOCTYPE html><html><body><p>code 123456</p></body></html>"

	// 代码实际发送的 FETCH 项
	t.Logf("header item: %s", previewHeaderSection.FetchItem())
	t.Logf("text item:   %s", previewTextSection.FetchItem())

	parse := func() *imap.Message {
		msg := &imap.Message{}
		// 服务器实际回显的响应 section 名(字段名原样大小写)
		fields := []interface{}{
			imap.RawString("UID"), imap.RawString("168"),
			imap.RawString("BODY[HEADER.FIELDS (Content-Type Content-Transfer-Encoding MIME-Version)]"), newTestLiteral(rawHeader),
			imap.RawString("BODY[TEXT]<0>"), newTestLiteral(rawText),
		}
		if err := msg.Parse(fields); err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return msg
	}

	// 路径 1: lookupPreviewBody 拼装 + extractBody 解析
	r, truncated := lookupPreviewBody(parse())
	if r == nil {
		t.Fatal("lookupPreviewBody returned nil")
	}
	if truncated {
		t.Error("short body flagged as truncated")
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	assembled := sb.String()
	if !strings.Contains(assembled, "Content-Type: text/html") {
		t.Errorf("assembled missing Content-Type header: %q", assembled)
	}
	if !strings.Contains(assembled, "123456") {
		t.Errorf("assembled missing body text: %q", assembled)
	}

	// 路径 2: 完整 toMessageWithBody(literal 只能读一次, 用新消息)
	m := toMessageWithBody(parse())
	if !strings.Contains(m.Preview, "123456") {
		t.Errorf("Preview = %q, want OTP text", m.Preview)
	}
}
