import { useMemo } from 'react'

const requiredCookieNames = [
  'X-APPLE-WEBAUTH-TOKEN',
  'X-APPLE-WEBAUTH-USER',
]

const optionalCookieNames = [
  'X_APPLE_WEB_KB',
  'X-APPLE-WEBAUTH-HSA-TRUST',
  'X-APPLE-DS-WEB-SESSION-TOKEN',
]

type CookieParseResult = {
  cookies: Record<string, string>
  matchedNames: string[]
  missingRequired: string[]
  parseError?: string
}

function decodeQuotedValue(value: string): string {
  const trimmed = value.trim()
  if (trimmed.length >= 2 && trimmed.startsWith('"') && trimmed.endsWith('"')) {
    try {
      return JSON.parse(trimmed) as string
    } catch {
      return trimmed.slice(1, -1)
    }
  }
  return trimmed
}

/** 从 JSON、Cookie Header 或任意文本中提取 iCloud 关键 Cookie。 */
function parseICloudCookies(raw: string): CookieParseResult {
  const value = raw.trim()
  const cookies: Record<string, string> = {}
  let parseError = ''

  if (value.startsWith('{')) {
    try {
      const parsed = JSON.parse(value) as unknown
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        parseError = 'JSON 必须是 Cookie 键值对象'
      } else {
        for (const [name, cookieValue] of Object.entries(parsed as Record<string, unknown>)) {
          if (typeof cookieValue === 'string' && cookieValue.trim()) cookies[name] = cookieValue.trim()
        }
      }
    } catch {
      parseError = 'JSON 格式不完整，可继续粘贴或改用 Cookie Header'
    }
  }

  const names = [...requiredCookieNames, ...optionalCookieNames]
  for (const name of names) {
    if (cookies[name]) continue
    const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const match = raw.match(new RegExp(`(?:^|[;\\s,{])${escaped}\\s*[=:]\\s*("(?:\\\\.|[^"\\\\])*"|[^;\\s,}]+)`, 'i'))
    if (match?.[1]) cookies[name] = decodeQuotedValue(match[1])
  }

  const matchedNames = names.filter((name) => Boolean(cookies[name]))
  return {
    cookies,
    matchedNames,
    missingRequired: requiredCookieNames.filter((name) => !cookies[name]),
    ...(parseError ? { parseError } : {}),
  }
}

interface SmartCookieInputProps {
  id: string
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  required?: boolean
  label?: string
}

export default function SmartCookieInput({
  id,
  value,
  onChange,
  disabled = false,
  required = false,
  label = 'Cookie',
}: SmartCookieInputProps) {
  const result = useMemo(() => parseICloudCookies(value), [value])
  const hasInput = value.trim().length > 0
  const valid = hasInput && result.missingRequired.length === 0 && !result.parseError

  function formatJSON() {
    if (result.matchedNames.length === 0) return
    const picked = Object.fromEntries(result.matchedNames.map((name) => [name, result.cookies[name]]))
    onChange(JSON.stringify(picked, null, 2))
  }

  return (
    <div className="smart-cookie-input">
      <div className="smart-cookie-label-row">
        <label htmlFor={id}>{label}{required ? '（必填）' : '（可选）'}</label>
        {hasInput && (
          <button type="button" className="text-button" onClick={formatJSON} disabled={disabled || result.matchedNames.length === 0}>
            格式化为 JSON
          </button>
        )}
      </div>
      <textarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        spellCheck={false}
        disabled={disabled}
        placeholder={'粘贴 Cookie Header 或 JSON，例如：\nX-APPLE-WEBAUTH-TOKEN=…; X-APPLE-WEBAUTH-USER=…'}
      />
      {hasInput && (
        <div className={`smart-cookie-status${valid ? ' is-valid' : ' is-invalid'}`} role="status">
          {result.parseError
            ? `❌ ${result.parseError}`
            : valid
              ? `✅ Cookie 格式可用 · 已识别 ${result.matchedNames.length} 个关键 Cookie`
              : `❌ 缺少 ${result.missingRequired.join('、')}`}
        </div>
      )}
      <p className="hint">自动识别 TOKEN、USER、会话和设备信任字段；提交时仅发送当前文本，不会在浏览器持久化。</p>
    </div>
  )
}
