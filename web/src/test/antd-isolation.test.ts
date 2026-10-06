import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * antd 面板隔离层的守卫测试。
 *
 * antd 的日期面板经 portal 渲染到 body, 项目全局的元素级规则会一并命中
 * 面板内部的同名元素。曾发生的真实事故:
 * - 全局 `table { min-width: 920px }` 把日历网格撑到 920px(年份视图每列
 *   307px), 溢出 351px 的面板 → 右侧年份列(2021/2024/2027)被裁掉;
 * - 全局 `th, td` 的 padding/border 给日历格子加了框线、表头加了灰底;
 * - 全局 `tbody tr` 的入场动画作用到日历行;
 * - 全局 `button` 的 min-height:40px 把面板头部箭头按钮撑到 50px 高。
 *
 * 这些隔离规则一旦被误删, jsdom 无法测出布局后果, 所以在此断言其存在。
 */
const styles = readFileSync(resolve(__dirname, '../styles.css'), 'utf8')

/** 提取选择器块的声明体(简易解析, 只匹配顶层规则)。 */
function blockBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = css.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`))
  return match ? match[1] : ''
}

describe('antd 面板隔离层 (styles.css)', () => {
  it('复位面板内表格的 min-width, 防 920px 网格撑爆', () => {
    const body = blockBody(styles, '.ant-picker-dropdown table')
    expect(body).toContain('min-width: 0')
  })

  it('复位面板内 th/td 的 padding 与边框, 防日历格子被加框线', () => {
    const body = blockBody(styles, '.ant-picker-dropdown th,\n.ant-picker-dropdown td')
    expect(body).toContain('padding: 0')
    expect(body).toContain('border-bottom: 0')
  })

  it('复位面板内表格行的动画, 防日历行播放入场动效', () => {
    const body = blockBody(styles, '.ant-picker-dropdown tbody tr')
    expect(body).toContain('animation: none')
    expect(body).toContain('transition: none')
  })

  it('复位面板内按钮的 min-height, 防头部箭头被撑到 50px', () => {
    const body = blockBody(styles, '.ant-picker-dropdown button')
    expect(body).toContain('min-height: auto')
  })

  it('隔离层不得命中面板外的元素(选择器必须以 .ant-picker-dropdown 为前缀)', () => {
    // 反向守卫: 全局 table 规则保留 min-width: 920px (页面表格仍需要)
    const globalTable = blockBody(styles, 'table')
    expect(globalTable).toContain('min-width: 920px')
  })
})
