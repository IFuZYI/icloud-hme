import { describe, expect, it } from 'vitest'
import { theme as antdTheme } from 'antd'
import { buildAntdTheme } from './antdTheme'

// 复现 dogfood QA 发现的暗色模式缺陷: antd 主题硬编码为浅色,
// 暗色系统下日期选择器是黑页面上的一块白斑(实测 rgb(255,255,255))。
describe('antd 主题跟随系统色', () => {
  it('浅色模式: 使用设计系统浅色 token, 不启用暗色算法', () => {
    const theme = buildAntdTheme(false)
    expect(theme.token.colorBgContainer).toBe('#ffffff')
    expect(theme.token.colorText).toBe('#1d1d1f')
    expect(theme.token.colorBorder).toBe('#c7c7cc')
    expect(theme.algorithm).toBeUndefined()
  })

  it('暗色模式: 容器背景与文字对齐设计系统暗色 token', () => {
    const theme = buildAntdTheme(true)
    // --color-bg-raised: #1d1d1f / --color-text: #f5f5f7 / --color-border-strong: #48484a
    expect(theme.token.colorBgContainer).toBe('#1d1d1f')
    expect(theme.token.colorText).toBe('#f5f5f7')
    expect(theme.token.colorBorder).toBe('#48484a')
    // 使用 antd 暗色算法处理派生色(悬浮态/禁用态等)
    expect(theme.algorithm).toBe(antdTheme.darkAlgorithm)
  })

  it('两种模式都保持控件高度与圆角一致', () => {
    for (const dark of [false, true]) {
      const theme = buildAntdTheme(dark)
      expect(theme.token.controlHeight).toBe(40)
      expect(theme.token.borderRadius).toBe(11)
    }
  })
})
