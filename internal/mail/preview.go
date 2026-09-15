package mail

import (
	stdhtml "html"
	"regexp"
	"strings"
)

var (
	htmlTagRE        = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlNoiseBlockRE = regexp.MustCompile(`(?is)<(style|script|head|title|noscript)\b[^>]*>.*?</(style|script|head|title|noscript)\s*>`)
	// unclosedNoiseBlockRE 匹配"有开标签但到结尾都没闭合"的 noise 块
	// (截断邮件的常见形态), 只删开标签, 保留后续文本。
	unclosedNoiseBlockRE = regexp.MustCompile(`(?is)<(style|script|head|title|noscript)\b[^>]*>[^<]*$`)
	htmlCommentRE        = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlBreakRE          = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlBlockEndRE       = regexp.MustCompile(`(?i)</(p|div|tr|h[1-6])\s*>`)
	htmlListItemRE       = regexp.MustCompile(`(?i)<li\b[^>]*>`)

	cssSignalRE      = regexp.MustCompile(`(?i)(@font-face|@media|@import|@supports|@keyframes|(^|[\s;{])(-webkit-|-moz-|-ms-|mso-)[\w-]*|(^|[\s;{])(font-family|text-size-adjust|border-collapse|mso-table-[\w-]+)\s*:)`)
	cssDeclarationRE = regexp.MustCompile(`(?i)(^|[;{])\s*[-a-z_][\w-]*\s*:\s*[^;{}]+`)
)

// sanitizePreview converts an email preview to readable text and removes
// presentation-only HTML/CSS that mail clients commonly include.
func sanitizePreview(raw string) string {
	text := stripHTML(raw)
	return sanitizePlainPreview(text)
}

func sanitizePlainPreview(raw string) string {
	text := normalizePreview(raw)
	if text == "" || !looksLikeCSS(text) {
		return text
	}

	// Some upstream APIs return the contents of a <style> block without the
	// surrounding tags. Remove balanced CSS rules while preserving any text
	// that follows the stylesheet.
	text = normalizePreview(stripCSSRules(text))
	if text == "" || looksLikeCSS(text) {
		return ""
	}
	return text
}

// stripHTML removes tags and invisible document sections while keeping common
// line breaks so the preview remains readable.
func stripHTML(raw string) string {
	raw = htmlNoiseBlockRE.ReplaceAllString(raw, "")
	raw = htmlCommentRE.ReplaceAllString(raw, "")
	// 截断的 HTML(摘要走 partial fetch)可能停在未闭合的 <style>/<head> 里:
	// 贪婪的块匹配会把开标签之后的全部正文一起吞掉, 摘要变空。
	// 未闭合时只删掉开标签本身, 让后续内容按普通文本处理;
	// 漏进来的 CSS 由 sanitizePlainPreview 的 looksLikeCSS 兜底清理。
	raw = unclosedNoiseBlockRE.ReplaceAllString(raw, " ")
	raw = htmlBreakRE.ReplaceAllString(raw, "\n")
	raw = htmlBlockEndRE.ReplaceAllString(raw, "\n")
	raw = htmlListItemRE.ReplaceAllString(raw, "\n- ")
	raw = htmlTagRE.ReplaceAllString(raw, "")
	return normalizePreview(stdhtml.UnescapeString(raw))
}

func normalizePreview(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func looksLikeCSS(text string) bool {
	braces := strings.Count(text, "{")
	semicolons := strings.Count(text, ";")
	signals := len(cssSignalRE.FindAllStringIndex(text, -1))
	declarations := len(cssDeclarationRE.FindAllStringIndex(text, -1))
	trimmed := strings.TrimSpace(text)
	selectorStart := strings.HasPrefix(trimmed, ".") ||
		strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "@")

	if braces == 0 {
		return signals >= 2 && semicolons >= 3
	}
	if selectorStart && declarations >= 1 {
		return true
	}
	return (signals >= 2 && semicolons >= 2) ||
		(braces >= 3 && declarations >= 3 && semicolons >= 3)
}

func stripCSSRules(text string) string {
	for {
		open := strings.IndexByte(text, '{')
		if open < 0 {
			return text
		}
		close := matchingBrace(text, open)
		if close < 0 {
			return text
		}

		start := open
		for start > 0 {
			if text[start-1] == '}' || text[start-1] == '\n' || text[start-1] == '\r' {
				break
			}
			start--
		}
		text = text[:start] + text[close+1:]
	}
}

func matchingBrace(text string, open int) int {
	depth := 0
	var quote byte
	escaped := false
	for i := open; i < len(text); i++ {
		ch := text[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
