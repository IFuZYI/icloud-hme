/// <reference types="node" />

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

function readStyle(name: string): string {
  return readFileSync(fileURLToPath(new URL(name, import.meta.url)), 'utf8')
}

const styles = [readStyle('./styles.css'), readStyle('./task-ui.css'), readStyle('./aliases-ui.css')].join('\n')

describe('全局动效设计系统', () => {
  it('为页面、数据行、弹窗和浮层提供克制的 Apple 风格动效', () => {
    expect(styles).toContain('--ease-apple: cubic-bezier(0.32, 0.72, 0, 1)')
    expect(styles).toContain('animation: content-reveal')
    expect(styles).toContain('animation: row-enter')
    expect(styles).toContain('animation: dialog-spring')
    expect(styles).toContain('animation: popover-enter')
  })

  it('仅在支持悬停的设备启用抬升效果，并尊重减少动态效果偏好', () => {
    expect(styles).toContain('@media (hover: hover) and (pointer: fine)')
    expect(styles).toContain('@media (prefers-reduced-motion: reduce)')
    expect(styles).toMatch(/scroll-behavior:\s*auto\s*!important/)
  })

  it('使用克制的 Apple 中性色、单一蓝色强调与实色背景', () => {
    expect(styles).toContain('--color-primary: #0071e3')
    expect(styles).toContain('--color-bg: #f5f5f7')
    expect(styles).toContain('--color-text: #1d1d1f')
    expect(styles).toContain('--radius-pill: 999px')
    expect(styles).not.toContain('--gradient-brand')
    expect(styles).not.toMatch(/radial-gradient\(/)
  })

  it('为导航、按钮、卡片和加载状态提供连续且高性能的反馈', () => {
    expect(styles).toContain('animation: nav-indicator-in')
    expect(styles).toContain('animation: content-reveal')
    expect(styles).toContain('animation: skeleton-shimmer')
    expect(styles).toContain('will-change: transform, opacity')
    expect(styles).toMatch(/\.app-shell\s+nav\s+a:active[\s\S]*transform:\s*scale\(0\.96\)/)
  })

  it('为小屏幕提供紧凑导航与至少 44px 的触摸目标', () => {
    expect(styles).toContain('@media (max-width: 760px)')
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*min-height:\s*44px/)
    expect(styles).toMatch(/@media \(max-width: 760px\)[\s\S]*overflow-x:\s*auto/)
  })

  it('为品牌区域提供独立的层级、分隔与悬停反馈', () => {
    expect(styles).toContain('.brand-lockup')
    expect(styles).toContain('.brand-name')
    expect(styles).toContain('.brand-sub')
    expect(styles).toContain('.brand-lockup::after')
  })
})
