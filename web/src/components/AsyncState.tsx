import type { ReactNode } from 'react'

interface AsyncStateProps {
  loading: boolean
  error: string
  empty: boolean
  emptyText?: string
  onRetry: () => void
  children: ReactNode
}

/** 统一异步状态:loading / error+retry / empty / content */
export default function AsyncState({
  loading,
  error,
  empty,
  emptyText = '暂无数据',
  onRetry,
  children,
}: AsyncStateProps) {
  if (loading) {
    return <p className="empty-state" aria-busy="true">加载中…</p>
  }
  if (error) {
    return (
      <div>
        <div className="alert-error" role="alert">
          {error}
        </div>
        <button onClick={onRetry}>重试</button>
      </div>
    )
  }
  if (empty) {
    return <p className="empty-state">{emptyText}</p>
  }
  return <>{children}</>
}
