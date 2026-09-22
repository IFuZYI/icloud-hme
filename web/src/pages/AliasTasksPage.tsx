import { useEffect, useMemo, useState } from 'react'
import { request, ApiError } from '../api/client'
import { fetchAccounts } from '../api/cache'
import { formatIntervalMinutes } from '../utils/datetime'
import AliasTaskForm from '../components/AliasTaskForm'
import TaskMoreMenu from '../components/TaskMoreMenu'
import type { AccountSummary, AliasTask } from '../api/types'
import { useToast } from '../components/ToastProvider'
import { IconPlus } from '../components/icons'

export default function AliasTasksPage() {
  const [tasks, setTasks] = useState<AliasTask[]>([])
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [open, setOpen] = useState(false)
  const [edit, setEdit] = useState<AliasTask>()
  const [error, setError] = useState('')
  const { show } = useToast()

  async function load() {
    try {
      const [taskList, accountList] = await Promise.all([
        request<AliasTask[]>('/api/alias-tasks'), fetchAccounts<AccountSummary[]>(),
      ])
      setTasks(taskList); setAccounts(accountList); setError('')
    } catch (e) { setError(e instanceof ApiError ? e.message : '加载失败') }
  }
  useEffect(() => {
    // 首次加载与轮询都放进定时器回调,避免在 effect 主体同步 setState
    const initial = window.setTimeout(() => { void load() }, 0)
    if (open) return () => window.clearTimeout(initial)
    const timer = window.setInterval(() => { void load() }, 2000)
    return () => { window.clearTimeout(initial); window.clearInterval(timer) }
  }, [open])
  // 账号名查找建索引：2 秒轮询下每帧对每张卡片都 accounts.find 是 O(卡片×账号)，
  // 预建 id→name 映射后单卡查名降为 O(1)。
  const accountNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const a of accounts) map.set(a.id, a.name)
    return map
  }, [accounts])
  const accountName = (id: string) => accountNames.get(id) || id

  async function action(task: AliasTask, kind: 'toggle' | 'delete') {
    try {
      await request(`/api/alias-tasks/${task.id}${kind === 'toggle' ? '/toggle' : ''}`, { method: kind === 'toggle' ? 'POST' : 'DELETE' })
      show(kind === 'delete' ? '任务已删除' : task.enabled ? '任务已暂停' : '任务已启用'); void load()
    } catch (e) { setError(e instanceof ApiError ? e.message : '操作失败') }
  }

  const labelText = (task: AliasTask) => {
    if (task.label_mode === 'sequential') return `${task.label_prefix ?? ''}001 顺序`
    if (task.label_mode === 'hash') return `${task.label_prefix ?? ''}+哈希`
    return '名称库自动'
  }

  return <section>
    <div className="task-page-header">
      <div><h2>自动创建任务</h2><p>自主任务按每天数量自动安排时间，定时任务按固定周期创建；两者都在达到目标数量后停止。同账号创建尝试至少间隔 20 分钟、每日合计最多 50 个，任何失败立即暂停。</p></div>
      <button className="primary task-create-button" onClick={() => { setEdit(undefined); setOpen(true) }}><IconPlus size={16} />新建任务</button>
    </div>
    {error && <div className="alert-error" role="alert">{error}</div>}
    {tasks.length === 0 ? <p className="empty-state">暂无自动创建任务</p> : <div className="task-card-grid">
      {tasks.map((task) => {
        const percent = Math.min(100, Math.round((task.created_count / task.max_total) * 100))
        const isAuto = task.mode === 'auto'
        return <article className={`task-card ${task.enabled ? 'task-card-enabled' : 'task-card-disabled'}`} key={task.id}>
          <div className="task-card-header"><div className="task-card-title"><div className="task-title-line"><h3>{labelText(task)}</h3><span className="task-type-badge">{isAuto ? '自主任务' : '定时任务'}</span></div><p title={accountName(task.account_id)}>{accountName(task.account_id)}</p></div><TaskMoreMenu enabled={task.enabled} onToggle={() => void action(task, 'toggle')} onEdit={() => { setEdit(task); setOpen(true) }} onDelete={() => void action(task, 'delete')} /></div>
          <div className="task-card-meta"><span><small>{isAuto ? '每天数量' : '创建周期'}</small><strong>{isAuto ? `${task.daily_limit} 个/天` : `每${formatIntervalMinutes(task.interval_minutes)} ${task.batch_count} 个`}</strong></span><span><small>今日</small><strong>{task.daily_count} / {task.daily_limit} 个</strong></span></div>
          <div className="task-progress-label"><span>进度</span><strong>{task.created_count} / {task.max_total}（{percent}%）</strong></div>
          <div className="task-next-run">下次执行：{task.enabled ? (task.next_run ? new Date(task.next_run).toLocaleString() : '已达上限或等待下一轮') : '已暂停'}</div>
          <div className="task-progress" role="progressbar" aria-valuenow={task.created_count} aria-valuemin={0} aria-valuemax={task.max_total} aria-label={`${task.created_count}/${task.max_total}`}><div className="task-progress-fill" style={{ width: `${percent}%` }} /></div>
          {task.last_error && <p className="task-card-error" title={task.last_error}>{task.last_error}</p>}
        </article>
      })}
    </div>}
    <AliasTaskForm open={open} accounts={accounts.map((a) => ({ id: a.id, name: a.name }))} edit={edit} onClose={() => setOpen(false)} onSaved={() => { show('任务已保存'); void load() }} />
  </section>
}
