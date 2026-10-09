import { useState } from 'react'
import Dialog from './Dialog'
import { ApiError, request } from '../api/client'
import SelectMenu from './SelectMenu'
import { formatIntervalMinutes } from '../utils/datetime'
import type { AccountSummary, AliasTask, LabelMode, TaskMode } from '../api/types'

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
  mode: TaskMode
  dailyCount: string
  interval: string
  batchCount: string
  target: string
  labelMode: LabelMode
  labelPrefix: string
  hashLength: string
  enabled: boolean
}

// 自主任务每日创建数量的允许范围(手动输入)。
const MIN_DAILY = 1
const MAX_DAILY = 50
const DEFAULT_DAILY = 20
// 定时任务可选的创建间隔（分钟）。
const INTERVALS = [20, 30, 45, 60, 90, 120, 180, 360, 720, 1440]

function initialState(accounts: Account[], edit?: AliasTask): FormState {
  return {
    // 编辑时用任务所属账号; 新建时仅单账号自动选中——多账号下默认不选择,
    // 由用户显式选择(顶部菜单打开就自动加载第一个账号会把它刷爆)。
    account: edit?.account_id ?? (accounts.length === 1 ? accounts[0].id : ''),
    mode: edit?.mode ?? 'auto',
    dailyCount: String(edit?.mode === 'auto' ? edit?.daily_limit ?? DEFAULT_DAILY : DEFAULT_DAILY),
    interval: String(edit?.interval_minutes ?? 60),
    batchCount: String(edit?.batch_count ?? 1),
    target: String(edit?.max_total ?? 20),
    labelMode: edit?.label_mode ?? 'library',
    labelPrefix: edit?.label_prefix ?? '',
    hashLength: String(edit?.hash_length ?? 4),
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
    if (form.mode === 'auto') {
      const daily = Number(form.dailyCount)
      if (!Number.isInteger(daily) || daily < MIN_DAILY || daily > MAX_DAILY) {
        setError(`每天创建数量需在 ${MIN_DAILY}-${MAX_DAILY} 之间`)
        return
      }
    }
    setBusy(true)
    setError('')
    try {
      const body: Record<string, unknown> = {
        enabled: form.enabled,
        account_id: form.account,
        mode: form.mode,
        target_count: Number(form.target),
        label_mode: form.labelMode,
      }
      if (form.mode === 'auto') {
        body.daily_limit = Number(form.dailyCount)
      } else {
        body.interval_minutes = Number(form.interval)
        body.batch_count = Number(form.batchCount)
      }
      if (form.labelMode !== 'library') {
        body.label_prefix = form.labelPrefix.trim()
      }
      if (form.labelMode === 'hash') {
        body.hash_length = Number(form.hashLength)
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

  const prefixExample =
    form.labelMode === 'sequential'
      ? `${form.labelPrefix || '标签'}001`
      : `${form.labelPrefix || '标签'}${'x'.repeat(Number(form.hashLength) || 4)}`

  return (
    <Dialog title={edit ? '编辑任务' : '新建任务'} open={open} onClose={onClose}>
      {error && <div className="alert-error" role="alert">{error}</div>}

      <div className="form-field">
        <label htmlFor="task-account">账号</label>
        <SelectMenu
          ariaLabel="选择账号"
          placeholder="请选择账号"
          value={form.account}
          options={accounts.map((account) => ({ value: account.id, label: account.name }))}
          onChange={(value) => update('account', value)}
        />
      </div>

      <div className="form-field">
        <span className="field-label">任务类型</span>
        <div className="task-mode-toggle" role="radiogroup" aria-label="任务类型">
          <button
            type="button"
            role="radio"
            aria-checked={form.mode === 'auto'}
            className={`task-mode-option ${form.mode === 'auto' ? 'is-active' : ''}`}
            onClick={() => update('mode', 'auto')}
          >
            <strong>自主任务</strong>
            <small>设定每天数量，系统自动安排时间</small>
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={form.mode === 'scheduled'}
            className={`task-mode-option ${form.mode === 'scheduled' ? 'is-active' : ''}`}
            onClick={() => update('mode', 'scheduled')}
          >
            <strong>定时任务</strong>
            <small>每隔固定时间创建固定数量</small>
          </button>
        </div>
      </div>

      <div className="form-field">
        <label htmlFor="task-target">目标邮箱数量</label>
        <input id="task-target" type="number" min="1" max="999" value={form.target} onChange={(event) => update('target', event.target.value)} />
        <p className="hint">达到目标后自动停止，不会自动删除已有别名。</p>
      </div>

      {form.mode === 'auto' ? (
        <div className="form-field">
          <label htmlFor="task-daily-count">每天创建数量</label>
          <input
            id="task-daily-count"
            type="number"
            min={MIN_DAILY}
            max={MAX_DAILY}
            value={form.dailyCount}
            onChange={(event) => update('dailyCount', event.target.value)}
          />
          <p className="hint">输入 1-50 之间的数量；系统会按当天剩余时间分摊创建时刻（中午开始只生成半天目标）。单账号每天合计最多 50 个。</p>
        </div>
      ) : (
        <>
          <div className="form-field">
            <label htmlFor="task-interval">创建间隔</label>
            <SelectMenu
              id="task-interval"
              block
              ariaLabel="创建间隔"
              value={form.interval}
              options={INTERVALS.map((value) => ({ value: String(value), label: formatIntervalMinutes(value) }))}
              onChange={(value) => update('interval', value)}
            />
          </div>
          <div className="form-field">
            <label htmlFor="task-batch">每次创建数量</label>
            <input id="task-batch" type="number" min="1" max="20" value={form.batchCount} onChange={(event) => update('batchCount', event.target.value)} />
            <p className="hint">每个周期创建的数量（1–20）；单账号每天合计仍不超过 50 个。</p>
          </div>
        </>
      )}

      <div className="form-field">
        <span className="field-label">邮箱标签</span>
        <div className="task-mode-toggle" role="radiogroup" aria-label="邮箱标签方式">
          <button type="button" role="radio" aria-checked={form.labelMode === 'library'} className={`task-mode-option ${form.labelMode === 'library' ? 'is-active' : ''}`} onClick={() => update('labelMode', 'library')}>
            <strong>自动生成</strong>
            <small>从名称库轮换标签</small>
          </button>
          <button type="button" role="radio" aria-checked={form.labelMode === 'sequential'} className={`task-mode-option ${form.labelMode === 'sequential' ? 'is-active' : ''}`} onClick={() => update('labelMode', 'sequential')}>
            <strong>顺序生成</strong>
            <small>前缀 + 001 计数</small>
          </button>
          <button type="button" role="radio" aria-checked={form.labelMode === 'hash'} className={`task-mode-option ${form.labelMode === 'hash' ? 'is-active' : ''}`} onClick={() => update('labelMode', 'hash')}>
            <strong>哈希生成</strong>
            <small>前缀 + 随机哈希</small>
          </button>
        </div>
      </div>

      {form.labelMode === 'library' ? (
        <p className="hint">名称库内置 500+ 常用服务名称（GitHub、Notion、淘宝、微信等），按顺序循环取用，仅作为本地标签。</p>
      ) : (
        <>
          <div className="form-field">
            <label htmlFor="task-label-prefix">标签前缀</label>
            <input id="task-label-prefix" type="text" maxLength={32} placeholder="如 主邮箱" value={form.labelPrefix} onChange={(event) => update('labelPrefix', event.target.value)} />
          </div>
          {form.labelMode === 'hash' && (
            <div className="form-field">
              <label htmlFor="task-hash-length">哈希位数</label>
              <SelectMenu
                id="task-hash-length"
                block
                ariaLabel="哈希位数"
                value={form.hashLength}
                options={[4, 5, 6, 7, 8].map((value) => ({ value: String(value), label: `${value} 位` }))}
                onChange={(value) => update('hashLength', value)}
              />
            </div>
          )}
          <p className="hint">示例：{prefixExample}</p>
        </>
      )}

      <div className="form-actions">
        <button type="button" onClick={onClose}>取消</button>
        <button type="button" className="primary" disabled={busy || !form.account} onClick={() => void save()}>
          {busy ? '保存中…' : '保存'}
        </button>
      </div>
    </Dialog>
  )
}
