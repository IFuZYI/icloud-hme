import { useEffect, useRef, useState } from 'react'
import { ApiError, request } from '../api/client'
import { formatDateTime } from '../utils/datetime'
import type { AliasTaskLog, PurgeTask } from '../api/types'
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

/** 日志类别: 值 → 展示标签。空/未知 = 自动任务(向后兼容)。 */
const LOG_CATEGORIES: Array<{ value: string; label: string }> = [
  { value: 'auto_task', label: '自动任务' },
  { value: 'batch_alias', label: '批量操作' },
  { value: 'purge', label: '邮件清理' },
]

function categoryLabel(value?: string): string {
  const found = LOG_CATEGORIES.find((c) => c.value === (value || 'auto_task'))
  return found?.label ?? '自动任务'
}

/** 清理档位: 删除「N 天前」的日志, 保留最近 N 天。 */
const CLEANUP_OPTIONS: Array<{ days: number; label: string }> = [
  { days: 1, label: '1 天前' },
  { days: 7, label: '1 周前' },
  { days: 30, label: '1 个月前' },
]

/** 清理任务状态: 值 → 展示文案。 */
const PURGE_STATUS_TEXT: Record<string, string> = {
  running: '进行中',
  throttled: '风控节流中',
  paused: '已暂停',
  interrupted: '已中断',
  done: '已完成',
  failed: '失败',
  cancelled: '已取消',
}

function purgeStatusText(status: string): string {
  return PURGE_STATUS_TEXT[status] ?? status
}

/** 清理任务卡: 仅用于运行中任务(进度条 + 暂停/继续/取消 + 查看详情)。
 *
 * 已完成任务的完整信息不在此显示——由日志表格中该任务日志行的
 * 「查看详情」打开「清理任务详情」承载(任务信息 + 该任务运行日志)。 */
function PurgeTaskCard({
  task,
  onAction,
  onDetail,
}: {
  task: PurgeTask
  onAction: (id: string, action: 'pause' | 'resume' | 'cancel') => void
  onDetail: (task: PurgeTask) => void
}) {
  const pct = task.total > 0 ? Math.min(100, Math.round((task.processed / task.total) * 100)) : 0

  return (
    <div className={`purge-task-card is-active${task.status === 'paused' || task.status === 'interrupted' ? ' is-paused' : ''}`}>
      <div className="purge-task-head">
        <span className={`badge ${task.status === 'running' ? 'badge-active' : 'badge-neutral'}`}>
          {purgeStatusText(task.status)}
        </span>
        <strong>{task.account_name || task.account_id}</strong>
        <span className="hint">{task.range_end.slice(0, 10)} 及更早的邮件</span>
        <button
          type="button"
          className="link-button"
          aria-label="查看清理任务详情"
          onClick={() => onDetail(task)}
        >
          查看详情
        </button>
      </div>
      {/* 进度条 */}
      <div
        className="purge-progress"
        role="progressbar"
        aria-valuenow={task.processed}
        aria-valuemin={0}
        aria-valuemax={task.total}
        aria-label={`清理进度 ${task.processed}/${task.total}`}
      >
        <div className="purge-progress-fill" style={{ width: `${pct}%` }} />
      </div>
      <p className="purge-task-detail">
        <span className="purge-task-count">
          {task.processed} / {task.total}
        </span>
        {task.current_message && <span className="purge-task-current">{task.current_message}</span>}
        {task.error && <span className="log-message-error">{task.error}</span>}
      </p>
      <div className="purge-task-actions">
        {task.status === 'running' || task.status === 'throttled' ? (
          <button type="button" className="link-button" onClick={() => onAction(task.id, 'pause')}>
            暂停
          </button>
        ) : (
          <button type="button" className="link-button" onClick={() => onAction(task.id, 'resume')}>
            继续
          </button>
        )}
        <button type="button" className="link-button key-danger-link" onClick={() => onAction(task.id, 'cancel')}>
          取消
        </button>
      </div>
    </div>
  )
}

export default function LogsPage() {
  const [logs, setLogs] = useState<AliasTaskLog[]>([])
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<AliasTaskLog | null>(null)
  const [cleanupOpen, setCleanupOpen] = useState(false)
  const [cleanupDays, setCleanupDays] = useState(7)
  const [cleaning, setCleaning] = useState(false)
  const [cleanupError, setCleanupError] = useState('')
  // 清理任务(进度卡区)。
  const [purgeTasks, setPurgeTasks] = useState<PurgeTask[]>([])
  // 清理任务详情弹层(日志行/运行中卡片「查看详情」打开; 按 ID 派生,
  // 运行中任务随 2s 轮询实时更新)。
  const [detailTaskId, setDetailTaskId] = useState<string | null>(null)
  // 类别筛选('' = 全部)。
  const [categoryFilter, setCategoryFilter] = useState('')
  const { show } = useToast()

  function load() {
    request<AliasTaskLog[]>('/api/alias-task-logs')
      .then(setLogs)
      .catch((cause) => setError(cause instanceof ApiError ? cause.message : '日志加载失败'))
  }

  function loadPurgeTasks() {
    request<PurgeTask[]>('/api/purge-tasks')
      .then(setPurgeTasks)
      .catch(() => {
        // 清理任务加载失败不阻塞日志(可能无权限); 静默。
      })
  }

  useEffect(() => {
    load()
    loadPurgeTasks()
  }, [])

  // 有活跃清理任务时轮询(2s), 全部结束即停。
  // 任务完成的那一刻额外刷新一次日志列表: 让「清理完成」条目即时出现
  // (完成条由后端在任务结束时写入)。
  const prevActiveRef = useRef(false)
  useEffect(() => {
    const active = purgeTasks.some((t) => ['running', 'throttled', 'paused', 'interrupted'].includes(t.status))
    if (!active) {
      // 从「有活跃任务」变为「无」: 刷新日志一次。
      if (prevActiveRef.current) {
        load()
      }
      prevActiveRef.current = false
      return
    }
    prevActiveRef.current = true
    const timer = window.setInterval(() => loadPurgeTasks(), 2000)
    return () => window.clearInterval(timer)
  }, [purgeTasks])

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

  /** 清理任务操作: pause/resume/cancel。 */
  async function purgeAction(id: string, action: 'pause' | 'resume' | 'cancel') {
    try {
      await request(`/api/purge-tasks/${encodeURIComponent(id)}/${action}`, { method: 'POST' })
      show(action === 'pause' ? '清理任务已暂停' : action === 'resume' ? '清理任务已继续' : '清理任务已取消')
      loadPurgeTasks()
    } catch (cause) {
      show(cause instanceof ApiError ? cause.message : '操作失败')
    }
  }

  const cleanupLabel = CLEANUP_OPTIONS.find((option) => option.days === cleanupDays)?.label ?? ''

  // 类别筛选后的日志。
  const visibleLogs = categoryFilter
    ? logs.filter((log) => (log.category || 'auto_task') === categoryFilter)
    : logs

  /** 活跃任务(进度条)置顶显示; 已完成任务不显示卡片。 */
  const activeTasks = purgeTasks.filter((t) => ['running', 'throttled', 'paused', 'interrupted'].includes(t.status))
  /** 详情弹层对应的任务(按 ID 派生: 运行中任务随轮询实时更新)。 */
  const detailTask = detailTaskId ? purgeTasks.find((t) => t.id === detailTaskId) ?? null : null
  /** 详情弹层里展示的该任务运行日志(新→旧)。 */
  const taskLogs = detailTask
    ? logs.filter((log) => log.task_id === detailTask.id).slice().sort((a, b) => b.time.localeCompare(a.time))
    : []
  /** 日志行的「查看详情」: 清理类日志打开任务详情(任务可查时), 否则打开日志详情。 */
  function openLogDetail(log: AliasTaskLog) {
    if ((log.category || 'auto_task') === 'purge' && purgeTasks.some((t) => t.id === log.task_id)) {
      setDetailTaskId(log.task_id)
      return
    }
    setSelected(log)
  }

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>任务日志</h2>
          <p>查看自动任务、批量操作与邮件清理的完整运行记录</p>
        </div>
        <button className="logs-cleanup-btn" onClick={openCleanup}>
          清理日志
        </button>
      </div>
      {error && <div className="alert-error">{error}</div>}

      {/* 清理任务进度区: 只显示运行中任务(已完成任务无卡片, 信息在任务详情里) */}
      {activeTasks.length > 0 && (
        <div className="purge-task-cards">
          {activeTasks.map((t) => (
            <PurgeTaskCard key={t.id} task={t} onAction={purgeAction} onDetail={(task) => setDetailTaskId(task.id)} />
          ))}
        </div>
      )}

      <div className="logs-filter">
        <label htmlFor="log-category-filter">日志类别</label>
        <select
          id="log-category-filter"
          value={categoryFilter}
          onChange={(e) => setCategoryFilter(e.target.value)}
        >
          <option value="">全部</option>
          {LOG_CATEGORIES.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      </div>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>时间</th>
              <th>任务</th>
              <th>类别</th>
              <th>级别</th>
              <th>内容</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {visibleLogs.map((log) => (
              <tr key={log.id}>
                <td>{formatLogTime(log.time)}</td>
                <td>{log.task_id}</td>
                <td>
                  <span className="badge badge-neutral">{categoryLabel(log.category)}</span>
                </td>
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
                    onClick={() => openLogDetail(log)}
                  >
                    查看详情
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {visibleLogs.length === 0 && <p className="empty-state">暂无日志</p>}
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

      {/* 清理任务详情: 任务级信息 + 该任务的运行日志。
          打开来源: 日志表格中清理日志行的「查看详情」/ 运行中任务卡的「查看详情」。 */}
      <Dialog
        title="清理任务详情"
        open={detailTask !== null}
        onClose={() => setDetailTaskId(null)}
      >
        {detailTask && (
          <div className="log-detail">
            <dl>
              <div>
                <dt>状态</dt>
                <dd>
                  <span className={`badge ${detailTask.status === 'done' ? 'badge-active' : detailTask.status === 'failed' ? 'badge-error' : 'badge-neutral'}`}>
                    {purgeStatusText(detailTask.status)}
                  </span>
                </dd>
              </div>
              <div><dt>账号</dt><dd>{detailTask.account_name || detailTask.account_id}</dd></div>
              <div><dt>清理范围</dt><dd>{detailTask.range_end.slice(0, 10)} 及更早的邮件</dd></div>
              <div>
                <dt>删除</dt>
                <dd>
                  已删除 {detailTask.deleted} 封
                  {detailTask.failed > 0 && `，失败 ${detailTask.failed}`}
                  {detailTask.total > 0 && `（共 ${detailTask.total} 封）`}
                </dd>
              </div>
              {detailTask.started_at && <div><dt>开始时间</dt><dd>{formatLogTime(detailTask.started_at)}</dd></div>}
              {detailTask.finished_at && <div><dt>完成时间</dt><dd>{formatLogTime(detailTask.finished_at)}</dd></div>}
              {detailTask.error && <div><dt>错误</dt><dd className="log-message-error">{detailTask.error}</dd></div>}
              <div><dt>任务 ID</dt><dd><code>{detailTask.id}</code></dd></div>
            </dl>
            {/* 该任务的运行日志(按 task_id 过滤)。 */}
            {taskLogs.length > 0 && (
              <>
                <p className="purge-task-logs-title">运行日志</p>
                <ul className="purge-task-logs">
                  {taskLogs.map((log) => (
                    <li key={log.id}>
                      <span className="purge-log-time">{formatLogTime(log.time)}</span>
                      <span className={log.level === 'error' ? 'log-message-error' : undefined}>{log.message}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
            <div className="form-actions">
              <button type="button" onClick={() => setDetailTaskId(null)}>关闭</button>
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
