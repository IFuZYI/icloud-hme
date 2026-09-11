import { useEffect, useState } from 'react'
import { ApiError, request } from '../api/client'
import type { AliasTaskLog } from '../api/types'

export default function LogsPage() {
  const [logs, setLogs] = useState<AliasTaskLog[]>([])
  const [error, setError] = useState('')

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
                <td>{log.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {logs.length === 0 && <p className="empty-state">暂无日志</p>}
    </section>
  )
}
