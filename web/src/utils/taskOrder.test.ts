import { describe, expect, it } from 'vitest'
import { freezeTaskOrder } from './taskOrder'
import type { AliasTask } from '../api/types'

/** 构造完整 AliasTask 测试替身(仅 id 参与排序断言)。 */
function mk(id: string): AliasTask {
  return {
    id,
    enabled: true,
    account_id: 'acc_1',
    mode: 'auto',
    interval_minutes: 60,
    batch_count: 1,
    daily_limit: 7,
    label_mode: 'library',
    max_total: 33,
    created_count: 0,
    daily_count: 0,
    last_success: 0,
  }
}

// 冻结展示顺序: 已有任务保持上一次的相对顺序(服务端乱序/重排不得引起
// 「闪现换位」), 新任务追加到末尾, 已删除任务被移除且不影响其余顺序。
describe('freezeTaskOrder 冻结任务卡片展示顺序', () => {
  it('服务端乱序返回时已有任务保持上次顺序', () => {
    const prev = ['a', 'b', 'c']
    const incoming = [mk('c'), mk('a'), mk('b')]
    expect(freezeTaskOrder(prev, incoming)).toEqual(['a', 'b', 'c'])
  })

  it('新任务追加到末尾, 已有任务不动', () => {
    const prev = ['a', 'b']
    const incoming = [mk('b'), mk('new'), mk('a')]
    expect(freezeTaskOrder(prev, incoming)).toEqual(['a', 'b', 'new'])
  })

  it('任务被删除后其余任务保持相对顺序', () => {
    const prev = ['a', 'b', 'c']
    const incoming = [mk('c'), mk('b')]
    expect(freezeTaskOrder(prev, incoming)).toEqual(['b', 'c'])
  })

  it('首次加载(无历史顺序)按服务端顺序展示', () => {
    expect(freezeTaskOrder([], [mk('b'), mk('a')])).toEqual(['b', 'a'])
  })

  it('多个新任务按服务端顺序追加到末尾', () => {
    const prev = ['a']
    const incoming = [mk('n2'), mk('a'), mk('n1')]
    expect(freezeTaskOrder(prev, incoming)).toEqual(['a', 'n2', 'n1'])
  })
})
