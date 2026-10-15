import type { AliasTask } from '../api/types'

/**
 * 冻结任务卡片展示顺序。
 *
 * 背景: 列表每 2 秒轮询刷新, 若直接按服务端返回数组渲染, 任何服务端顺序
 * 波动都会让卡片「闪现换位」(用户实测反馈)。展示顺序必须只由用户可见的
 * 事件改变:
 *  - 已有任务保持上一次的相对顺序(服务端重排不影响展示);
 *  - 新任务追加到末尾(不插队、不挤动已有卡片);
 *  - 已删除任务移除, 其余任务相对顺序不变。
 *
 * @param prevIds 上一次渲染的展示顺序
 * @param incoming 本次服务端返回的任务列表
 * @returns 本次应渲染的任务 ID 顺序
 */
export function freezeTaskOrder(prevIds: string[], incoming: AliasTask[]): string[] {
  const present = new Set(incoming.map((t) => t.id))
  const seen = new Set<string>()
  const kept: string[] = []
  for (const id of prevIds) {
    if (present.has(id) && !seen.has(id)) {
      kept.push(id)
      seen.add(id)
    }
  }
  const fresh: string[] = []
  for (const t of incoming) {
    if (!seen.has(t.id)) {
      fresh.push(t.id)
      seen.add(t.id)
    }
  }
  return [...kept, ...fresh]
}
