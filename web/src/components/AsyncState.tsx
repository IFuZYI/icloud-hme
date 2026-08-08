import type { ReactNode } from 'react'
import { IconAlert, IconInboxEmpty } from './icons'

interface AsyncStateProps {
  loading: boolean
  error: string
  empty: boolean
  emptyText?: string
  onRetry: () => void
  children: ReactNode
}

/** 统一异步状态:骨架屏 loading / error+retry / empty / content */
export default function AsyncState({
  loading,
  error,
  empty,
  emptyText = '暂无数据',
  onRetry,
  children,
}: AsyncStateProps) {
  if (loading) {
    return (
      <div className="skeleton" role="status" aria-label="加载中" aria-busy="true">
        <span className="visually-hidden">加载中</span>
        <div className="skeleton-line" style={{ width: '30%' }} />
        <div className="skeleton-line" style={{ width: '85%' }} />
        <div className="skeleton-line" style={{ width: '70%' }} />
        <div className="skeleton-line" style={{ width: '90%' }} />
      </div>
    )
  }
  if (error) {
    return (
      <div>
        <div className="alert-error" role="alert" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <IconAlert size={18} style={{ flexShrink: 0 }} />
          <span>{error}</span>
        </div>
        <button onClick={onRetry}>重试</button>
      </div>
    )
  }
  if (empty) {
    return (
      <p className="empty-state">
        <IconInboxEmpty className="empty-icon" />
        {emptyText}
      </p>
    )
  }
  return <>{children}</>
}
