import { useEffect, useState } from 'react'
import { ApiError, request } from '../api/client'
import { formatDateTime } from '../utils/datetime'
import type { AliasTaskLog } from '../api/types'
import Dialog from '../components/Dialog'
import { useToast } from '../components/ToastProvider'

/** 服务端 RFC3339 → 项目统一的中文时间格式; 解析失败时回退原文。 */
function formatLogTime(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return formatDateTime(d)
}

function failureReason(message: string): string {
  const separator = message.match(/[：:]/)
  if (!separator?.index) return message
  return message.slice(separator.index + separator[0].length).trim() || message
}

/** 清理档位: 删除「N 天前」的日志, 保留最近 N 天。 */
const CLEANUP_OPTIONS: Array<{ days: number; label: string }> = [
  { days: 1, label: '1 天前' },
  { days: 7, label: '1 周前' },
  { days: 30, label: '1 个月前' },
]

export default function LogsPage() {
  const [logs, setLogs] = useState<AliasTaskLog[]>([])
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<AliasTaskLog | null>(null)
  const [cleanupOpen, setCleanupOpen] = useState(false)
  const [cleanupDays, setCleanupDays] = useState(7)
  const [cleaning, setCleaning] = useState(false)
  const [cleanupError, setCleanupError] = useState('')
  const { show } = useToast()

  function load() {
    request<AliasTaskLog[]>('/api/alias-task-logs')
      .then(setLogs)
      .catch((cause) => setError(cause instanceof ApiError ? cause.message : '日志加载失败'))
  }

  useEffect(() => {
    load()
  }, [])

  function openCleanup() {
    setCleanupDays(7)
    setCleanupError('')
    setCleanupOpen(true)
  }

  async function runCleanup() {
    setCleaning(true)
    setCleanupError('')
    try {
      const result = await request<{ deleted: number; remaining: number }>(
        '/api/alias-task-logs/cleanup',
        { method: 'POST', body: { older_than_days: cleanupDays } },
      )
      setCleanupOpen(false)
      // 0 条时给更贴切的提示(「已清理 0 条」读起来像失败)
      show(result.deleted > 0 ? `已清理 ${result.deleted} 条日志` : '没有符合条件的日志，未删除任何条目')
      load()
    } catch (cause) {
      // 失败保留对话框, 便于用户重试或换档位
      setCleanupError(cause instanceof ApiError ? cause.message : '日志清理失败')
    } finally {
      setCleaning(false)
    }
  }

  const cleanupLabel = CLEANUP_OPTIONS.find((option) => option.days === cleanupDays)?.label ?? ''

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>任务日志</h2>
          <p>查看自动创建任务的完整运行记录</p>
        </div>
        <button className="logs-cleanup-btn" onClick={openCleanup}>
          清理日志
        </button>
      </div>
      {error && <div className="alert-error">{error}</div>}
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>任务</th>
              <th>级别</th>
              <th>内容</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {logs.map((log) => (
              <tr key={log.id}>
                <td>{formatLogTime(log.time)}</td>
                <td>{log.task_id}</td>
                <td>
                  <span className={log.level === 'error' ? 'badge badge-error' : 'badge badge-active'}>
                    {log.level}
                  </span>
                </td>
                <td className={log.level === 'error' ? 'log-message-error' : undefined}>{log.message}</td>
                <td>
                  <button
                    type="button"
                    className="link-button"
                    aria-label={log.level === 'error' ? '查看失败日志详情' : '查看普通日志详情'}
                    onClick={() => setSelected(log)}
                  >
                    查看详情
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {logs.length === 0 && <p className="empty-state">暂无日志</p>}
      <Dialog
        title={selected?.level === 'error' ? '失败日志详情' : '日志详情'}
        open={selected !== null}
        onClose={() => setSelected(null)}
      >
        {selected && (
          <div className="log-detail">
            {selected.level === 'error' && (
              <div className="alert-error log-failure-reason" role="alert">
                <strong>失败原因</strong>
                <p>{failureReason(selected.message)}</p>
              </div>
            )}
            <dl>
              <div><dt>时间</dt><dd>{formatLogTime(selected.time)}</dd></div>
              <div><dt>任务 ID</dt><dd><code>{selected.task_id}</code></dd></div>
              <div><dt>级别</dt><dd>{selected.level}</dd></div>
              <div><dt>完整日志</dt><dd className="log-detail-message">{selected.message}</dd></div>
            </dl>
            <div className="form-actions">
              <button type="button" onClick={() => setSelected(null)}>关闭</button>
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        title="清理日志"
        open={cleanupOpen}
        onClose={() => { if (!cleaning) setCleanupOpen(false) }}
      >
        <p>将删除所选时间之前的任务日志，操作不可恢复。</p>
        {cleanupError && <div className="alert-error" role="alert">{cleanupError}</div>}
        <div className="form-field">
          <span className="field-label">清理范围</span>
          <div className="task-mode-toggle" role="radiogroup" aria-label="清理范围">
            {CLEANUP_OPTIONS.map((option) => (
              <button
                key={option.days}
                type="button"
                role="radio"
                aria-checked={cleanupDays === option.days}
                className={`task-mode-option ${cleanupDays === option.days ? 'is-active' : ''}`}
                onClick={() => setCleanupDays(option.days)}
              >
                <strong>{option.label}</strong>
                <small>保留最近 {option.days} 天</small>
              </button>
            ))}
          </div>
          <p className="hint">将删除「{cleanupLabel}」的日志，最近 {cleanupDays} 天的记录保留。</p>
        </div>
        <div className="form-actions">
          <button type="button" onClick={() => setCleanupOpen(false)} disabled={cleaning}>
            取消
          </button>
          <button type="button" className="danger" onClick={() => void runCleanup()} disabled={cleaning}>
            {cleaning ? '清理中…' : '确认清理'}
          </button>
        </div>
      </Dialog>
    </section>
  )
}
