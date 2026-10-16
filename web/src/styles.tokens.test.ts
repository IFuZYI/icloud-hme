import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// ====================================================================
// CSS 设计 token 完整性
// ====================================================================
//
// 背景(dogfood QA 实测): 引用但未定义的 var(--token) 会让浏览器把整条
// 声明静默丢弃——`.purge-task-card` 的 background/radius 因此消失(实测
// computed 为 transparent/0px), 而布局探针与截图都发现不了。
//
// 契约: 所有 var(--token) 引用都必须在样式表中存在定义(至少一个作用域;
// 例如 --color-primary-button 只在暗色块定义、浅色走 fallback 是允许的)。

/** 定位样式表目录: 兼容从 web/ 或仓库根运行。 */
function stylesDir(): string {
  const candidates = [resolve(process.cwd(), 'src'), resolve(process.cwd(), 'web/src')]
  for (const c of candidates) {
    if (existsSync(resolve(c, 'styles.css'))) return c
  }
  throw new Error('未找到样式表目录')
}

const dir = stylesDir()
const cssFiles = readdirSync(dir).filter((f) => f.endsWith('.css'))

/** 去掉注释, 避免注释里的 `--token:` 文案被误判为定义。 */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

describe('CSS 设计 token 完整性', () => {
  it('存在可扫描的样式表', () => {
    expect(cssFiles.length).toBeGreaterThan(0)
  })

  it('所有 var(--token) 引用都有定义(防声明被静默丢弃)', () => {
    const defs = new Set<string>()
    const refs = new Map<string, string[]>()
    for (const file of cssFiles) {
      const css = stripComments(readFileSync(resolve(dir, file), 'utf8'))
      for (const m of css.matchAll(/(--[a-z0-9-]+)\s*:/g)) {
        defs.add(m[1])
      }
      for (const m of css.matchAll(/var\(\s*(--[a-z0-9-]+)/g)) {
        const list = refs.get(m[1]) ?? []
        if (!list.includes(file)) list.push(file)
        refs.set(m[1], list)
      }
    }
    const orphans = [...refs.entries()]
      .filter(([token]) => !defs.has(token))
      .map(([token, files]) => `${token} (${files.join(', ')})`)
      .sort()
    expect(orphans).toEqual([])
  })
})
