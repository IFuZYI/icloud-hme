import { useEffect, useState } from 'react'
import { ApiError, request } from '../api/client'
import type { AliasTaskLog } from '../api/types'
import Dialog from '../components/Dialog'

function failureReason(message: string): string {
  const separator = message.match(/[：:]/)
  if (!separator?.index) return message
  return message.slice(separator.index + separator[0].length).trim() || message
}

export default function LogsPage() {
  const [logs, setLogs] = useState<AliasTaskLog[]>([])
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<AliasTaskLog | null>(null)

  useEffect(() => {
    request<AliasTaskLog[]>('/api/alias-task-logs')
      .then(setLogs)
      .catch((cause) => setError(cause instanceof ApiError ? cause.message : '日志加载失败'))
  }, [])

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>任务日志</h2>
          <p>查看自动创建任务的完整运行记录</p>
        </div>
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
                <td>{new Date(log.time).toLocaleString()}</td>
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
              <div><dt>时间</dt><dd>{new Date(selected.time).toLocaleString()}</dd></div>
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
    </section>
  )
}
