import { useState } from 'react'
import Dialog from './Dialog'
import { ApiError, request } from '../api/client'
import SelectMenu from './SelectMenu'
import type { AccountSummary, AliasTask } from '../api/types'

type Account = Pick<AccountSummary, 'id' | 'name'>
type Props = {
  open: boolean
  accounts: Account[]
  edit?: AliasTask
  onClose: () => void
  onSaved: () => void
}

type FormState = {
  account: string
  interval: string
  target: string
  dailyLimit: string
  enabled: boolean
}

function initialState(accounts: Account[], edit?: AliasTask): FormState {
  return {
    account: edit?.account_id ?? accounts[0]?.id ?? '',
    interval: String(edit?.interval_minutes ?? 60),
    target: String(edit?.max_total ?? 20),
    dailyLimit: String(edit?.daily_limit ?? 20),
    enabled: edit?.enabled ?? true,
  }
}

export default function AliasTaskForm({ open, accounts, edit, onClose, onSaved }: Props) {
  const [form, setForm] = useState(() => initialState(accounts, edit))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((current) => ({ ...current, [key]: value }))
  }

  async function save() {
    setBusy(true)
    setError('')
    try {
      const body = {
        enabled: form.enabled,
        account_id: form.account,
        interval_minutes: Number(form.interval),
        target_count: Number(form.target),
        daily_limit: Number(form.dailyLimit),
      }
      await request(edit ? `/api/alias-tasks/${edit.id}` : '/api/alias-tasks', {
        method: edit ? 'PATCH' : 'POST',
        body,
      })
      onSaved()
      onClose()
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : '保存失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={edit ? '编辑自动任务' : '新建自动任务'} open={open} onClose={onClose}>
      {error && <div className="alert-error" role="alert">{error}</div>}
      <div className="form-field">
        <label htmlFor="task-account">账号</label>
        <SelectMenu
          ariaLabel="选择账号"
          value={form.account}
          options={accounts.map((account) => ({ value: account.id, label: account.name }))}
          onChange={(value) => update('account', value)}
        />
      </div>
      <div className="form-field">
        <label htmlFor="task-target">目标邮箱数量</label>
        <input id="task-target" type="number" min="1" max="999" value={form.target} onChange={(event) => update('target', event.target.value)} />
        <p className="hint">达到目标后自动停止，不会自动删除已有别名。</p>
      </div>
      <div className="form-field">
        <label htmlFor="task-daily-limit">每日创建上限</label>
        <SelectMenu
          id="task-daily-limit"
          block
          ariaLabel="每日创建上限"
          value={form.dailyLimit}
          options={[5, 10, 15, 20].map((value) => ({ value: String(value), label: `${value} 个/天` }))}
          onChange={(value) => update('dailyLimit', value)}
        />
        <p className="hint">所有任务合计每个账号每日最多创建 20 个。</p>
      </div>
      <div className="form-field">
        <label htmlFor="task-interval">间隔分钟</label>
        <SelectMenu
          id="task-interval"
          block
          ariaLabel="创建间隔"
          value={form.interval}
          options={[20, 30, 45, 60].map((value) => ({ value: String(value), label: `${value} 分钟` }))}
          onChange={(value) => update('interval', value)}
        />
        <p className="hint">每次只创建 1 个；系统将从内置名称库轮换标签。任何失败或上游提示都会立即暂停任务。</p>
      </div>
      <p className="hint">名称库包含 GitHub、Google Workspace、Notion、Slack、淘宝等常用服务名称，仅作为本地标签。</p>
      <div className="form-actions">
        <button type="button" onClick={onClose}>取消</button>
        <button type="button" className="primary" disabled={busy || !form.account} onClick={() => void save()}>
          {busy ? '保存中…' : '保存'}
        </button>
      </div>
    </Dialog>
  )
}
