import { useSyncExternalStore } from 'react'

/** 跟踪系统暗色模式偏好, 并在变化时触发重渲染。
 *
 *  antd 主题与项目 CSS 的暗色 token 是两套机制: CSS 走
 *  @media (prefers-color-scheme: dark), 而 antd 的 ConfigProvider
 *  需要 JS 显式传入暗色算法。此 hook 让两者保持同步。
 *
 *  用 useSyncExternalStore 而非 useState+useEffect: matchMedia 是外部数据源,
 *  订阅式读取可以避免 effect 内同步 setState 引发的级联渲染(lint 报错),
 *  也天然处理了「渲染与订阅之间值已变化」的竞态。 */
const QUERY = '(prefers-color-scheme: dark)'

function subscribe(onChange: () => void): () => void {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return () => {}
  }
  const query = window.matchMedia(QUERY)
  query.addEventListener('change', onChange)
  return () => query.removeEventListener('change', onChange)
}

function getSnapshot(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(QUERY).matches
    : false
}

export function usePrefersDark(): boolean {
  // 服务端/无 matchMedia 环境下回退到浅色
  return useSyncExternalStore(subscribe, getSnapshot, () => false)
}
