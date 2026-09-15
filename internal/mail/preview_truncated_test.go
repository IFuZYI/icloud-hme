package mail

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap"
)

// 用 Apple 服务器真实回显的字节流复现摘要解析链路。
// 场景: HTML 邮件正文是 quoted-printable 编码, 前 16KB 全是 <style>,
// 截断处没有可见文本 → 摘要应显示截断占位说明而不是空白/QP 原文。
func TestPreviewAssembleAppleBytes(t *testing.T) {
	// HEADER.FIELDS literal(含尾部空行, Apple 实测形态)
	rawHeader := "Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"MIME-Version: 1.0\r\n\r\n"
	// TEXT<0> literal: QP 编码的 HTML 开头, 全是样式, 无可见文本
	rawText := "<!DOCTYPE html>\r\n<html dir=3D\"ltr\">\r\n<head>\r\n" +
		"<style type=3D\"text/css\">\r\n@media screen { .onboarding { font-size: 28px; } }\r\n"

	// literal 长度达到 previewPartLimit 才视为截断; 构造等长的 QP 文本
	full := rawText + strings.Repeat("a", previewPartLimit-len(rawText))

	m := toMessageWithBody(parseAppleMsg(rawHeader, full))
	t.Logf("Preview = %q", m.Preview)
	if m.Preview == "" {
		t.Errorf("Preview 空, want 截断占位说明")
	}
	if strings.Contains(m.Preview, "=3D") || strings.Contains(m.Preview, "=20") {
		t.Errorf("Preview = %q, 漏出 quoted-printable 原文", m.Preview)
	}
}

// 截断在样式区但 QP 头存在时, 解码后不应出现 =3D/=20 转义序列。
func TestPreviewDecodesQuotedPrintable(t *testing.T) {
	rawHeader := "Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n"
	rawText := "<p>Your code is 123456=C2=A0now</p>"

	m := toMessageWithBody(parseAppleMsg(rawHeader, rawText))
	t.Logf("Preview = %q", m.Preview)
	if !strings.Contains(m.Preview, "123456") {
		t.Errorf("Preview = %q, want 含验证码", m.Preview)
	}
	if strings.Contains(m.Preview, "=C2") {
		t.Errorf("Preview = %q, QP 未解码", m.Preview)
	}
}

func parseAppleMsg(header, text string) *imap.Message {
	msg := &imap.Message{}
	fields := []interface{}{
		imap.RawString("UID"), imap.RawString("168"),
		imap.RawString("BODY[HEADER.FIELDS (Content-Type Content-Transfer-Encoding MIME-Version)]"), newTestLiteral(header),
		imap.RawString("BODY[TEXT]<0>"), newTestLiteral(text),
	}
	_ = msg.Parse(fields)
	return msg
}
