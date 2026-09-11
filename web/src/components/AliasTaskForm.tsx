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
  batch: string
  total: string
  prefix: string
  enabled: boolean
}

function initialState(accounts: Account[], edit?: AliasTask): FormState {
  return {
    account: edit?.account_id ?? accounts[0]?.id ?? '',
    interval: String(edit?.interval_minutes ?? 60),
    batch: String(edit?.batch_count ?? 5),
    total: String(edit?.max_total ?? 999),
    prefix: edit?.label_prefix ?? '自动邮箱',
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
        batch_count: Number(form.batch),
        max_total: Number(form.total),
        label_prefix: form.prefix,
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
        <label htmlFor="task-prefix">标签前缀</label>
        <input id="task-prefix" value={form.prefix} maxLength={180} onChange={(event) => update('prefix', event.target.value)} />
        <p className="hint">自动追加 001、002 等三位序号。</p>
      </div>
      <div className="form-field">
        <label htmlFor="task-batch">每小时创建数</label>
        <input id="task-batch" type="number" min="1" max="999" value={form.batch} onChange={(event) => update('batch', event.target.value)} />
      </div>
      <div className="form-field">
        <label htmlFor="task-total">最大创建总数（1-999）</label>
        <input id="task-total" type="number" min="1" max="999" value={form.total} onChange={(event) => update('total', event.target.value)} />
      </div>
      <div className="form-field">
        <label htmlFor="task-interval">间隔分钟</label>
        <input id="task-interval" type="number" min="1" max="10080" value={form.interval} onChange={(event) => update('interval', event.target.value)} />
      </div>
      <div className="form-actions">
        <button type="button" onClick={onClose}>取消</button>
        <button type="button" className="primary" disabled={busy || !form.account} onClick={() => void save()}>
          {busy ? '保存中…' : '保存'}
        </button>
      </div>
    </Dialog>
  )
}
