import { describe, expect, it } from 'vitest'

import { displayLabelForUrl, splitBodyLinks } from './linkify'

describe('splitBodyLinks', () => {
  it('把「锚文本 <URL>」折叠成一个带文案的链接', () => {
    expect(splitBodyLinks('Confirm email <https://example.com/c?t=1>')).toEqual([
      { type: 'link', value: 'https://example.com/c?t=1', label: 'Confirm email' },
    ])
  })

  it('页脚分隔符留在文本里，不并入链接文案', () => {
    const segments = splitBodyLinks(
      'Help <https://a.example/h>  ·  Privacy <https://a.example/p>',
    )
    expect(segments).toEqual([
      { type: 'link', value: 'https://a.example/h', label: 'Help' },
      { type: 'text', value: '  ·  ' },
      { type: 'link', value: 'https://a.example/p', label: 'Privacy' },
    ])
  })

  it('裸 URL 不带文案，由 displayLabelForUrl 生成短标签', () => {
    const segments = splitBodyLinks('或复制链接：https://example.com/verify?token=ABC')
    expect(segments[0]).toEqual({ type: 'text', value: '或复制链接：' })
    expect(segments[1]).toEqual({
      type: 'link',
      value: 'https://example.com/verify?token=ABC',
    })
    expect(displayLabelForUrl('https://example.com/verify?token=ABC')).toBe(
      'example.com/verify',
    )
  })

  it('剥离句末标点并保留为文本', () => {
    expect(splitBodyLinks('见 https://example.com/x.')).toEqual([
      { type: 'text', value: '见 ' },
      { type: 'link', value: 'https://example.com/x' },
      { type: 'text', value: '.' },
    ])
  })

  it('识别多个「锚文本 <URL>」', () => {
    const segments = splitBodyLinks(
      'Confirm <https://a.example/c> · Help <https://a.example/h>',
    )
    const links = segments
      .filter((s) => s.type === 'link')
      .map((s) => [s.label, s.value])
    expect(links).toEqual([
      ['Confirm', 'https://a.example/c'],
      ['Help', 'https://a.example/h'],
    ])
  })

  it('忽略非 http(s) 协议', () => {
    expect(splitBodyLinks('mailto:help@example.com')).toEqual([
      { type: 'text', value: 'mailto:help@example.com' },
    ])
  })

  it('空正文返回空数组', () => {
    expect(splitBodyLinks('')).toEqual([])
  })

  it('中文全角右括号与句号不并入链接', () => {
    expect(splitBodyLinks('（https://example.com/x）。')).toEqual([
      { type: 'text', value: '（' },
      { type: 'link', value: 'https://example.com/x' },
      { type: 'text', value: '）。' },
    ])
  })

  it('长追踪链接的裸 URL 文案被截断', () => {
    const url = `https://u56196570.ct.sendgrid.net/ls/click?upn=${'a'.repeat(200)}`
    const label = displayLabelForUrl(url)
    expect(label.length).toBeLessThanOrEqual(56)
    expect(label.startsWith('u56196570.ct.sendgrid.net/ls/click')).toBe(true)
  })

  it('真实 ZeroTwo 邮件片段折叠为可点击文案', () => {
    const body =
      'Confirm 26_rice_chicks@icloud.com to finish setting up your ZeroTwo account.\n\n' +
      'Confirm email <https://u56196570.ct.sendgrid.net/ls/click?upn=abc>\n\n' +
      'Help <https://u56196570.ct.sendgrid.net/ls/click?upn=h>  ·  Privacy <https://u56196570.ct.sendgrid.net/ls/click?upn=p>'
    const segments = splitBodyLinks(body)
    const links = segments.filter((s) => s.type === 'link')
    expect(links.map((s) => s.label)).toEqual(['Confirm email', 'Help', 'Privacy'])
    expect(links.every((s) => s.value.startsWith('https://'))).toBe(true)
  })
})

describe('splitBodyLinks edge cases', () => {
  it('保留锚点前的正文（跨行时锚文本只取本行）', () => {
    const segments = splitBodyLinks('开头文字\nConfirm email <https://x.example/c>')
    expect(segments[0]).toEqual({ type: 'text', value: '开头文字\n' })
    expect(segments[1]).toEqual({
      type: 'link',
      value: 'https://x.example/c',
      label: 'Confirm email',
    })
  })

  it('大写 scheme 也能折叠', () => {
    const segments = splitBodyLinks('Go <HTTPS://X.EXAMPLE/A>')
    expect(segments[0]).toEqual({
      type: 'link',
      value: 'HTTPS://X.EXAMPLE/A',
      label: 'Go',
    })
  })

  it('ASCII 右括号不并入裸 URL', () => {
    expect(splitBodyLinks('(https://example.com/x)')).toEqual([
      { type: 'text', value: '(' },
      { type: 'link', value: 'https://example.com/x' },
      { type: 'text', value: ')' },
    ])
  })

  it('长路径确实触发截断（不是查询串被丢弃）', () => {
    const url = `https://example.com/${'a'.repeat(100)}`
    const label = displayLabelForUrl(url)
    expect(label.length).toBe(56)
    expect(label.endsWith('…')).toBe(true)
  })

  it('大正文线性处理，不因长散文卡死', () => {
    const text = `${'lorem ipsum '.repeat(20000)}<https://example.com/x>`
    const started = Date.now()
    const segments = splitBodyLinks(text)
    const elapsed = Date.now() - started
    expect(segments.some((s) => s.type === 'link' && s.value === 'https://example.com/x')).toBe(true)
    expect(elapsed).toBeLessThan(500)
  })
})

  it('成对括号属于 URL，不成对的才剥离', () => {
    const wiki = splitBodyLinks('https://en.wikipedia.org/wiki/Function_(mathematics)')
    expect(wiki[0]).toEqual({
      type: 'link',
      value: 'https://en.wikipedia.org/wiki/Function_(mathematics)',
    })
    const prose = splitBodyLinks('(see https://example.com/x)')
    expect(prose[1]).toEqual({ type: 'link', value: 'https://example.com/x' })
    expect(prose[2]).toEqual({ type: 'text', value: ')' })
  })
