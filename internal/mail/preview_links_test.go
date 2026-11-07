package mail

import (
	"strings"
	"testing"
)

// TestSanitizePreviewPreservesAnchorURL 验证纯文本化不再吞掉按钮式链接:
// <a href="X">Y</a> 应展开为 "Y <X>", 且 &amp; 等实体解码。
func TestSanitizePreviewPreservesAnchorURL(t *testing.T) {
	raw := `<p>请点击下面的按钮完成验证：</p>` +
		`<a href="https://example.com/verify?token=ABC&amp;lang=zh">立即验证</a>`
	got := sanitizePreview(raw)

	if !strings.Contains(got, "立即验证") {
		t.Fatalf("锚文本丢失: %q", got)
	}
	if !strings.Contains(got, "https://example.com/verify?token=ABC&lang=zh") {
		t.Fatalf("URL 丢失或未解码: %q", got)
	}
}

// TestSanitizePreviewDropsUnsafeSchemes 验证非 http(s) 链接只保留文本。
func TestSanitizePreviewDropsUnsafeSchemes(t *testing.T) {
	cases := []string{
		`<a href="javascript:alert(1)">点我</a>`,
		`<a href="mailto:help@example.com">联系客服</a>`,
		`<a href="data:text/html;base64,PHNjcmlwdD4=">数据</a>`,
	}
	for _, raw := range cases {
		got := sanitizePreview(raw)
		for _, bad := range []string{"javascript:", "mailto:", "data:text/html"} {
			if strings.Contains(got, bad) {
				t.Fatalf("危险协议未被丢弃: %q -> %q", raw, got)
			}
		}
		if strings.TrimSpace(got) == "" {
			t.Fatalf("锚文本被误删: %q", raw)
		}
	}
}

// TestSanitizePreviewAnchorWithNestedTags 验证锚点内嵌标签时的文本提取。
func TestSanitizePreviewAnchorWithNestedTags(t *testing.T) {
	raw := `<a href="https://example.com/a"><span style="color:#fff">点此确认</span></a>`
	got := sanitizePreview(raw)
	if !strings.Contains(got, "点此确认") {
		t.Fatalf("锚文本丢失: %q", got)
	}
	if !strings.Contains(got, "https://example.com/a") {
		t.Fatalf("URL 丢失: %q", got)
	}
}

// TestSanitizePreviewAnchorWithoutHref 验证没有 href 的锚点只保留文本。
func TestSanitizePreviewAnchorWithoutHref(t *testing.T) {
	got := sanitizePreview(`<a name="top">回到顶部</a>`)
	if got != "回到顶部" {
		t.Fatalf("sanitizePreview() = %q", got)
	}
}

// TestSanitizePreviewAnchorTextEqualsHref 验证文本与 URL 相同时不重复。
func TestSanitizePreviewAnchorTextEqualsHref(t *testing.T) {
	got := sanitizePreview(`<a href="https://example.com/x">https://example.com/x</a>`)
	if got != "https://example.com/x" {
		t.Fatalf("sanitizePreview() = %q, 期望单份 URL", got)
	}
}

// TestSanitizePreviewZeroTwoStyleEmail 复现真实 HTML-only 邮件:
// 按钮文案在锚文本里, 地址只在 href 中, 纯文本化后必须能看到 URL。
func TestSanitizePreviewZeroTwoStyleEmail(t *testing.T) {
	raw := `<html><head><style>.btn{background:#000}</style></head><body>
<p>Confirm your email to finish setting up ZeroTwo.</p>
<table><tr><td><a class="btn" href="https://app.zerotwo.ai/confirm?t=abc123">Confirm your email</a></td></tr></table>
<p>Confirm 26_rice_chicks@icloud.com to finish setting up your ZeroTwo account.</p>
<table><tr><td><a href="https://app.zerotwo.ai/confirm?t=abc123">Confirm email</a></td></tr></table>
<p>Help &middot; <a href="https://zerotwo.ai/help">Help</a> &middot; <a href="https://zerotwo.ai/privacy">Privacy</a></p>
</body></html>`

	got := sanitizePreview(raw)
	if !strings.Contains(got, "https://app.zerotwo.ai/confirm?t=abc123") {
		t.Fatalf("确认链接丢失: %q", got)
	}
	if !strings.Contains(got, "https://zerotwo.ai/privacy") {
		t.Fatalf("页脚链接丢失: %q", got)
	}
	if strings.Contains(got, "background:#000") {
		t.Fatalf("样式泄漏进正文: %q", got)
	}
}

// TestSanitizePreviewDecodesEntitiesExactlyOnce 验证 URL 与锚文本只解码一次:
// 双重编码不应被解两次, 无分号实体(&not=)不应破坏查询串。
func TestSanitizePreviewDecodesEntitiesExactlyOnce(t *testing.T) {
	doubleEncoded := `<a href="https://x.example/?q=Tom&amp;amp;Jerry">Tom</a>`
	got := sanitizePreview(doubleEncoded)
	if !strings.Contains(got, "https://x.example/?q=Tom&amp;Jerry") {
		t.Fatalf("双重编码被解码两次: %q", got)
	}

	legacyEntity := `<a href="https://x.example/?a=1&not=2">go</a>`
	got = sanitizePreview(legacyEntity)
	if !strings.Contains(got, "https://x.example/?a=1&not=2") {
		t.Fatalf("查询串被实体解码破坏: %q", got)
	}
}

// TestSanitizePreviewEscapedAngleBracketDoesNotEatBody 验证锚文本里的 &lt;
// 不会在标签剥离阶段吞掉后续正文。
func TestSanitizePreviewEscapedAngleBracketDoesNotEatBody(t *testing.T) {
	raw := `<a href="https://x.example/a">a &lt; b</a> and then C > D`
	got := sanitizePreview(raw)
	if !strings.Contains(got, "a < b") {
		t.Fatalf("锚文本中的 &lt; 丢失: %q", got)
	}
	if !strings.Contains(got, "and then C > D") {
		t.Fatalf("锚文本后的正文被吞掉: %q", got)
	}
}

// TestSanitizePreviewIgnoresDataHref 验证 data-href 不会被当成真链接。
func TestSanitizePreviewIgnoresDataHref(t *testing.T) {
	raw := `<a data-href="javascript:void(0)" href="https://real.example/x">Go</a>`
	got := sanitizePreview(raw)
	if !strings.Contains(got, "https://real.example/x") {
		t.Fatalf("真实 href 被 data-href 抢占: %q", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Fatalf("data-href 被当成链接: %q", got)
	}
}

// TestSanitizePreviewUppercaseScheme 验证 scheme 大小写不敏感。
func TestSanitizePreviewUppercaseScheme(t *testing.T) {
	got := sanitizePreview(`<A HREF="HTTPS://X.EXAMPLE/A">Go</A>`)
	if !strings.Contains(got, "HTTPS://X.EXAMPLE/A") {
		t.Fatalf("大写 scheme 链接被丢弃: %q", got)
	}
}
