import { useEffect, useMemo, useState } from 'react'
import { ApiError, request } from '../api/client'
import { fetchAccounts } from '../api/cache'
import { formatDateTime } from '../utils/datetime'
import { copyText } from '../utils/clipboard'
import type { ApiKey, AccountSummary, PermissionCatalog } from '../api/types'
import Dialog from '../components/Dialog'
import ConfirmDialog from '../components/ConfirmDialog'
import { useToast } from '../components/ToastProvider'
import { IconKey } from '../components/icons'

/** 服务端 RFC3339 → 项目统一的中文时间格式; 解析失败时回退原文。 */
function formatKeyTime(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return formatDateTime(d)
}

/** 秘钥脱敏: 只对足够长的秘钥保留首尾各 4 位, 中间以省略号替代。
 *
 *  长度不足以安全脱敏(≤ 16)的秘钥整体显示为固定掩码——否则首尾各露 4 位
 *  会暴露短秘钥的大部分内容(自定义秘钥下限 8 位)。 */
function maskKey(key: string): string {
  if (key.length <= 16) return '••••••••'
  return `${key.slice(0, 4)}••••${key.slice(-4)}`
}

/** 编辑中的表单状态(创建/编辑共用)。 */
interface KeyForm {
  name: string
  key: string
  permissions: Set<string>
  /** 账号使用范围; 空集合 = 全部账号。 */
  accountIDs: Set<string>
}

function emptyForm(): KeyForm {
  return { name: '', key: '', permissions: new Set(), accountIDs: new Set() }
}

function formFromKey(k: ApiKey): KeyForm {
  return {
    name: k.name,
    key: k.key,
    permissions: new Set(k.permissions),
    accountIDs: new Set(k.account_ids ?? []),
  }
}

export default function KeysPage() {
  const [keys, setKeys] = useState<ApiKey[]>([])
  const [catalog, setCatalog] = useState<PermissionCatalog>({ groups: [], permissions: [] })
  // 账号列表(供账号范围选择; 与账号页同源)。
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  // 展开查看的秘钥 ID 集合(默认全部脱敏)。
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  // 创建/编辑对话框: null = 关闭; { editing?: ApiKey } 区分模式。
  const [dialog, setDialog] = useState<{ editing?: ApiKey } | null>(null)
  const [form, setForm] = useState<KeyForm>(emptyForm())
  const [formError, setFormError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  // 删除确认。
  const [deleteTarget, setDeleteTarget] = useState<ApiKey | null>(null)
  const [deleting, setDeleting] = useState(false)
  const { show } = useToast()

  function load() {
    setLoading(true)
    setError('')
    request<{ count: number; keys: ApiKey[] }>('/api/keys')
      .then((data) => setKeys(data.keys ?? []))
      .catch((cause) => setError(cause instanceof ApiError ? cause.message : '秘钥加载失败'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    // 首次加载放进定时器回调: 避免在 effect 主体同步 setState
    // (与 InboxPage/AliasTasksPage 的约定一致)。
    const initial = window.setTimeout(() => void load(), 0)
    let cancelled = false
    request<PermissionCatalog>('/api/permissions')
      .then((data) => {
        if (!cancelled) setCatalog(data)
      })
      .catch(() => {
        // 权限目录加载失败不阻塞列表; 对话框里会提示
      })
    // 账号列表供账号范围勾选; 失败不阻塞(选择区显示空)。
    // /api/accounts 的 data 直接是数组(与其它页面一致, 见 api/cache.ts)。
    fetchAccounts<AccountSummary[]>()
      .then((data) => {
        if (!cancelled) setAccounts(Array.isArray(data) ? data : [])
      })
      .catch(() => {
        // 账号加载失败: 账号范围选择区为空, 秘钥仍可创建(默认全部账号)
      })
    return () => {
      cancelled = true
      window.clearTimeout(initial)
    }
  }, [])

  /** 权限按分组归拢(保持目录顺序)。 */
  const grouped = useMemo(
    () =>
      catalog.groups.map((group) => ({
        group,
        items: catalog.permissions.filter((p) => p.group === group.id),
      })),
    [catalog],
  )

  function openCreate() {
    setForm(emptyForm())
    setFormError('')
    setDialog({})
  }

  function openEdit(k: ApiKey) {
    setForm(formFromKey(k))
    setFormError('')
    setDialog({ editing: k })
  }

  function togglePermission(id: string) {
    setForm((prev) => {
      const next = new Set(prev.permissions)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return { ...prev, permissions: next }
    })
  }

  function toggleAccount(id: string) {
    setForm((prev) => {
      const next = new Set(prev.accountIDs)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return { ...prev, accountIDs: next }
    })
  }

  async function submitForm() {
    if (submitting) return
    const name = form.name.trim()
    if (!name) {
      setFormError('请填写名称')
      return
    }
    setSubmitting(true)
    setFormError('')
    try {
      const permissions = [...form.permissions]
      const account_ids = [...form.accountIDs]
      if (dialog?.editing) {
        await request(`/api/keys/${encodeURIComponent(dialog.editing.id)}`, {
          method: 'PATCH',
          body: { name, permissions, account_ids },
        })
        show('秘钥已更新')
      } else {
        // 秘钥留空 = 服务端自动生成(sk- + 24 位随机字符)。
        await request('/api/keys', {
          method: 'POST',
          body: { name, key: form.key.trim(), permissions, account_ids },
        })
        show('秘钥已创建')
      }
      setDialog(null)
      load()
    } catch (cause) {
      setFormError(cause instanceof ApiError ? cause.message : '保存失败')
    } finally {
      setSubmitting(false)
    }
  }

  async function toggleEnabled(k: ApiKey) {
    try {
      await request(`/api/keys/${encodeURIComponent(k.id)}`, {
        method: 'PATCH',
        body: { enabled: !k.enabled },
      })
      show(k.enabled ? '秘钥已停用' : '秘钥已启用')
      load()
    } catch (cause) {
      show(cause instanceof ApiError ? cause.message : '操作失败')
    }
  }

  async function runDelete() {
    if (!deleteTarget || deleting) return
    setDeleting(true)
    try {
      await request(`/api/keys/${encodeURIComponent(deleteTarget.id)}`, { method: 'DELETE' })
      show('秘钥已删除')
      setDeleteTarget(null)
      load()
    } catch (cause) {
      show(cause instanceof ApiError ? cause.message : '删除失败')
    } finally {
      setDeleting(false)
    }
  }

  async function copyKey(k: ApiKey) {
    const copied = await copyText(k.key)
    show(copied ? '秘钥已复制' : '复制失败，请手动选择')
  }

  function toggleReveal(id: string) {
    setRevealed((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const editing = dialog?.editing

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>秘钥</h2>
          <p>为脚本创建 API 秘钥，每个秘钥可单独设置可访问的接口</p>
        </div>
        <button className="primary" onClick={openCreate}>
          <IconKey size={16} />
          创建秘钥
        </button>
      </div>
      {error && <div className="alert-error">{error}</div>}
      {!loading && keys.length === 0 && !error && (
        <p className="empty-state">暂无秘钥。创建后脚本可用 X-API-Key 请求头代替登录 Cookie 调用接口。</p>
      )}
      {keys.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>秘钥</th>
                <th>权限</th>
                <th>账号范围</th>
                <th>状态</th>
                <th>最近使用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td data-label="名称">{k.name}</td>
                  <td data-label="秘钥" className="key-cell">
                    <code className={revealed.has(k.id) ? 'key-value' : 'key-value masked'}>
                      {revealed.has(k.id) ? k.key : maskKey(k.key)}
                    </code>
                    <button
                      type="button"
                      className="link-button"
                      aria-label={revealed.has(k.id) ? '隐藏秘钥' : '显示秘钥'}
                      onClick={() => toggleReveal(k.id)}
                    >
                      {revealed.has(k.id) ? '隐藏' : '显示'}
                    </button>
                    <button
                      type="button"
                      className="link-button"
                      aria-label="复制秘钥"
                      onClick={() => void copyKey(k)}
                    >
                      复制
                    </button>
                  </td>
                  <td data-label="权限">{k.permissions.length > 0 ? `${k.permissions.length} 项` : '无权限'}</td>
                  <td data-label="账号范围">
                    {!k.account_ids || k.account_ids.length === 0
                      ? '全部账号'
                      : `${k.account_ids.length} 个账号`}
                  </td>
                  <td data-label="状态">
                    <span className={k.enabled ? 'badge badge-active' : 'badge badge-neutral'}>
                      {k.enabled ? '已启用' : '已停用'}
                    </span>
                  </td>
                  <td data-label="最近使用">
                    {k.last_used_at ? (
                      <span title={k.last_used_ip ?? ''}>{formatKeyTime(k.last_used_at)}</span>
                    ) : (
                      <span className="hint">从未使用</span>
                    )}
                  </td>
                  <td data-label="操作" className="key-row-actions cell-actions">
                    <button type="button" className="link-button" aria-label="编辑秘钥" onClick={() => openEdit(k)}>
                      编辑
                    </button>
                    <button type="button" className="link-button" aria-label="启用或停用秘钥" onClick={() => void toggleEnabled(k)}>
                      {k.enabled ? '停用' : '启用'}
                    </button>
                    <button type="button" className="link-button key-danger-link" aria-label="删除秘钥" onClick={() => setDeleteTarget(k)}>
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog
        title={editing ? '编辑秘钥' : '创建秘钥'}
        open={dialog !== null}
        onClose={() => {
          if (!submitting) setDialog(null)
        }}
      >
        {formError && <div className="alert-error" role="alert">{formError}</div>}
        <div className="form-field">
          <label htmlFor="key-name">名称</label>
          <input
            id="key-name"
            value={form.name}
            onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
            placeholder="如：签到脚本"
            autoComplete="off"
            maxLength={64}
          />
        </div>
        {!editing && (
          <div className="form-field">
            <label htmlFor="key-value">秘钥</label>
            <input
              id="key-value"
              value={form.key}
              onChange={(e) => setForm((prev) => ({ ...prev, key: e.target.value }))}
              placeholder="留空自动生成"
              autoComplete="off"
              maxLength={128}
            />
            <p className="hint">留空将自动生成 sk- 开头的 24 位随机秘钥；也可手动指定（8-128 位）。</p>
          </div>
        )}
        <fieldset className="permission-group account-scope-group">
          <legend>账号使用范围</legend>
          <p className="hint">不选 = 全部账号；勾选后仅限选定的账号可用。</p>
          {accounts.length === 0 && <p className="hint">暂无账号或加载中…</p>}
          <div className="account-scope-list">
            {accounts.map((acc) => (
              <label key={acc.id} className="permission-item">
                <input
                  type="checkbox"
                  checked={form.accountIDs.has(acc.id)}
                  onChange={() => toggleAccount(acc.id)}
                />
                {acc.name || acc.id}
              </label>
            ))}
          </div>
        </fieldset>
        <div className="form-field">
          <span className="field-label">权限</span>
          {grouped.length === 0 && <p className="hint">权限目录加载中…</p>}
          {grouped.map(({ group, items }) => (
            <fieldset key={group.id} className="permission-group">
              <legend>{group.label}</legend>
              {items.map((p) => (
                <label key={p.id} className="permission-item">
                  <input
                    type="checkbox"
                    checked={form.permissions.has(p.id)}
                    onChange={() => togglePermission(p.id)}
                  />
                  {p.label}
                </label>
              ))}
            </fieldset>
          ))}
        </div>
        <div className="form-actions">
          <button type="button" onClick={() => setDialog(null)} disabled={submitting}>
            取消
          </button>
          <button type="button" className="primary" onClick={() => void submitForm()} disabled={submitting}>
            {submitting ? '保存中…' : editing ? '保存' : '创建'}
          </button>
        </div>
      </Dialog>

      <ConfirmDialog
        title="删除秘钥"
        message={deleteTarget ? `将删除「${deleteTarget.name}」，使用该秘钥的脚本将立即失效。` : ''}
        confirmLabel="确认删除"
        open={deleteTarget !== null}
        onClose={() => {
          if (!deleting) setDeleteTarget(null)
        }}
        onConfirm={() => void runDelete()}
        busy={deleting}
      />
    </section>
  )
}
