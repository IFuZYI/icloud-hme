export interface BodySegment {
  type: 'text' | 'link'
  /** text: 原样文本；link: 目标地址（href） */
  value: string
  /** link 的可见文案；缺省时用 displayLabelForUrl(value) 生成 */
  label?: string
}

/**
 * 后端把 `<a href="X">Y</a>` 纯文本化为 `Y <X>`。
 * 这里把它还原成「可见文案 = Y，跳转地址 = X」，长 URL 不再出现在正文里。
 *
 * 直接匹配 `<URL>`，再向左回看有限窗口取锚文本：避免 `[^<>]{0,200}?`
 * 在长空白/超长行上退化（实测 400KB 正文可达数秒主线程卡顿）。
 */
const ANCHOR_URL_RE = /<(https?:\/\/[^<>\s]+)>/gi

/** 裸 URL（正文里直接写出的地址，或锚文本就是 URL 本身）。 */
const BARE_URL_RE = /https?:\/\/[^\s<>"'）]+/gi

/** 句末标点不属于 URL 本体（右括号按配对情况单独处理）。 */
const TRAILING_PUNCTUATION = '.,;:!?。'

/** 页脚分隔符：`Help · Privacy · Terms` 里的 `·` 不应并入链接文案。 */
const SEPARATOR_RE = /[·•|–—]/

/** 锚文本回看窗口上限（字符）。 */
const MAX_LABEL_LOOKBACK = 160

/** 裸 URL 的展示文案上限。 */
const MAX_BARE_LABEL_LENGTH = 56

/**
 * splitBodyLinks 把纯文本邮件正文切成「文本 / 链接」片段。
 *
 * 规则：
 *  1. `锚文本 <URL>` → 一个 link 片段，label = 锚文本，正文里不显示 URL；
 *  2. 裸 URL → 一个 link 片段，label = 域名+路径（查询串只在 title 里）；
 *  3. 只接受 http/https（scheme 大小写不敏感）；
 *  4. 页脚分隔符（`·`/`•`/`|`/`–`/`—`）留在文本里，不进入链接文案。
 */
export function splitBodyLinks(text: string): BodySegment[] {
  if (!text) {
    return []
  }
  const segments: BodySegment[] = []
  let last = 0

  for (const match of text.matchAll(ANCHOR_URL_RE)) {
    const start = match.index ?? 0
    const href = match[1]
    const labelStart = Math.max(last, labelWindowStart(text, start, last))
    if (labelStart > last) {
      for (const segment of splitBareUrls(text.slice(last, labelStart))) {
        segments.push(segment)
      }
    }
    const rawLabel = text.slice(labelStart, start)
    const { lead, label } = splitLabel(rawLabel)
    if (lead) {
      segments.push({ type: 'text', value: lead })
    }
    if (label) {
      segments.push({ type: 'link', value: href, label })
    } else {
      if (rawLabel && !lead) {
        segments.push({ type: 'text', value: rawLabel })
      }
      segments.push({ type: 'link', value: href })
    }
    last = start + match[0].length
  }

  if (last < text.length) {
    for (const segment of splitBareUrls(text.slice(last))) {
      segments.push(segment)
    }
  }
  return segments
}

/** 裸 URL 的可见文案：`host + path`，去掉查询串，超长截断。 */
export function displayLabelForUrl(url: string, max = MAX_BARE_LABEL_LENGTH): string {
  let compact = url
  try {
    const parsed = new URL(url)
    compact = parsed.host + (parsed.pathname === '/' ? '' : parsed.pathname)
  } catch {
    // 非标准 URL：原样展示
  }
  if (compact.length <= max) {
    return compact
  }
  return `${compact.slice(0, max - 1)}…`
}

/**
 * labelWindowStart 返回锚文本的起点：从 `<URL>` 向左最多回看
 * MAX_LABEL_LOOKBACK 个字符，且不跨越换行、`<`、`>` 或上一个片段的结尾。
 */
function labelWindowStart(text: string, anchorStart: number, floor: number): number {
  const windowStart = Math.max(floor, anchorStart - MAX_LABEL_LOOKBACK)
  const window = text.slice(windowStart, anchorStart)
  const cut = Math.max(
    window.lastIndexOf('\n'),
    window.lastIndexOf('<'),
    window.lastIndexOf('>'),
  )
  return cut >= 0 ? windowStart + cut + 1 : windowStart
}

/**
 * splitLabel 把 `"  ·  Privacy"` 拆成前导分隔符（保留为文本）与链接文案 `"Privacy"`。
 * 没有分隔符时整段都是链接文案。
 */
function splitLabel(raw: string): { lead: string; label: string } {
  const trimmedEnd = raw.replace(/[ \t]+$/, '')
  const cut = lastSeparatorIndex(trimmedEnd)
  if (cut < 0) {
    return { lead: '', label: trimmedEnd.trim() }
  }
  // 保留分隔符后的原始空白，避免 `·  Privacy` 被压成 `· Privacy`。
  const rest = trimmedEnd.slice(cut + 1)
  const spaces = rest.match(/^[ \t]*/)?.[0] ?? ''
  const lead = trimmedEnd.slice(0, cut + 1) + spaces
  const label = rest.slice(spaces.length).trim()
  return { lead, label }
}

function lastSeparatorIndex(text: string): number {
  for (let i = text.length - 1; i >= 0; i -= 1) {
    if (SEPARATOR_RE.test(text[i])) {
      return i
    }
  }
  return -1
}

/** splitBareUrls 处理不含 `锚文本 <URL>` 结构的文本片段。 */
function splitBareUrls(text: string): BodySegment[] {
  if (!text) {
    return []
  }
  const segments: BodySegment[] = []
  let last = 0

  for (const match of text.matchAll(BARE_URL_RE)) {
    const start = match.index ?? 0
    if (start > last) {
      segments.push({ type: 'text', value: text.slice(last, start) })
    }
    const raw = match[0]
    const trimmed = trimUrlTrailing(raw)
    segments.push({ type: 'link', value: trimmed })
    const trailing = raw.slice(trimmed.length)
    if (trailing) {
      segments.push({ type: 'text', value: trailing })
    }
    last = start + raw.length
  }

  if (last < text.length) {
    segments.push({ type: 'text', value: text.slice(last) })
  }
  return segments
}

/**
 * trimUrlTrailing 去掉不属于 URL 的尾部标点。
 * 右括号只在「闭括号多于开括号」时剥离，保留
 * `…/Function_(mathematics)` 这类合法 URL。
 */
function trimUrlTrailing(raw: string): string {
  let end = raw.length
  while (end > 0 && TRAILING_PUNCTUATION.includes(raw[end - 1])) {
    end -= 1
  }
  // 一次性统计配对缺口, 再按缺口剥离: 每个右括号不再重扫整段(O(n²) → O(n))。
  const pairs: Array<[string, string]> = [
    [')', '('],
    [']', '['],
    ['}', '{'],
  ]
  for (const [close, open] of pairs) {
    const slice = raw.slice(0, end)
    let deficit = countChar(slice, close) - countChar(slice, open)
    while (deficit > 0 && end > 0 && raw[end - 1] === close) {
      end -= 1
      deficit -= 1
    }
  }
  return raw.slice(0, end)
}

function countChar(text: string, ch: string): number {
  let n = 0
  for (const c of text) {
    if (c === ch) {
      n += 1
    }
  }
  return n
}
