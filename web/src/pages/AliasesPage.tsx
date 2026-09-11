import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams, useNavigate } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import type { AccountSummary, Alias } from '../api/types'
import AsyncState from '../components/AsyncState'
import CreateAliasDialog from '../components/CreateAliasDialog'
import ConfirmDialog from '../components/ConfirmDialog'
import RowActionMenu from '../components/RowActionMenu'
import SelectMenu from '../components/SelectMenu'
import { useToast } from '../components/ToastProvider'
import { copyText } from '../utils/clipboard'
import { IconChevronDown, IconChevronUp, IconClock, IconCopy, IconInbox, IconPlus, IconSearch, IconTrash, IconCheck } from '../components/icons'

type SortDirection = 'asc' | 'desc'

function parseAliasDate(raw?: string): Date | null {
  const value = raw?.trim()
  if (!value) return null

  // iCloud 可能返回 ISO 字符串，也可能返回秒、毫秒或微秒时间戳。
  if (/^[+-]?\d+(?:\.\d+)?$/.test(value)) {
    const numeric = Number(value)
    if (Number.isFinite(numeric)) {
      const magnitude = Math.abs(numeric)
      const milliseconds =
        magnitude < 1e11 ? numeric * 1000
          : magnitude < 1e14 ? numeric
            : magnitude < 1e17 ? numeric / 1000
              : numeric / 1e6
      const timestampDate = new Date(milliseconds)
      if (!Number.isNaN(timestampDate.getTime())) return timestampDate
    }
  }

  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

function formatDate(raw?: string): string {
  const date = parseAliasDate(raw)
  if (!date) return raw?.trim() || '—'
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}/${pad(date.getMonth() + 1)}/${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function dateTimestamp(raw?: string): number | null {
  return parseAliasDate(raw)?.getTime() ?? null
}

export default function AliasesPage() {
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [accountId, setAccountId] = useState('')
  const [aliases, setAliases] = useState<Alias[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<'all' | 'active' | 'inactive'>('all')
  const [sortDirection, setSortDirection] = useState<SortDirection>('desc')
  const [createOpen, setCreateOpen] = useState(false)
  const [confirm, setConfirm] = useState<{ type: 'deactivate' | 'reactivate' | 'delete'; alias: Alias } | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const { show, showCopyable } = useToast()

  // 加载账号列表
  useEffect(() => {
    let cancelled = false
    request<AccountSummary[]>('/api/accounts')
      .then((data) => {
        if (cancelled) return
        setAccounts(data)
        const queryId = searchParams.get('account_id')
        const valid = data.find((a) => a.id === queryId)
        const target = valid ? valid.id : data[0]?.id ?? ''
        setAccountId(target)
        if (target && (!queryId || !valid)) {
          setSearchParams({ account_id: target }, { replace: true })
        }
      })
      .catch((err) => { if (!cancelled) setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 加载别名列表
  useEffect(() => {
    if (!accountId) return
    let cancelled = false
    request<{ account_id: string; count: number; aliases: Alias[] }>(`/api/aliases?account_id=${encodeURIComponent(accountId)}`)
      .then((data) => {
        if (cancelled) return
        setAliases(data.aliases ?? [])
        setError('')
      })
      .catch((err) => { if (!cancelled) setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [accountId, retryKey])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return aliases
      .map((alias, index) => ({ alias, index }))
      .filter(({ alias }) => {
        if (filter === 'active' && !alias.active) return false
        if (filter === 'inactive' && alias.active) return false
        if (!q) return true
        return alias.email.toLowerCase().includes(q) || alias.label.toLowerCase().includes(q)
      })
      .sort((left, right) => {
        const leftTime = dateTimestamp(left.alias.createdAt)
        const rightTime = dateTimestamp(right.alias.createdAt)
        if (leftTime === null || rightTime === null) {
          if (leftTime === rightTime) return left.index - right.index
          return leftTime === null ? 1 : -1
        }
        if (leftTime === rightTime) return left.index - right.index
        return sortDirection === 'asc' ? leftTime - rightTime : rightTime - leftTime
      })
      .map(({ alias }) => alias)
  }, [aliases, search, filter, sortDirection])

  function handleRetry() { setLoading(true); setRetryKey((k) => k + 1) }

  const copyEmail = useCallback(async (email: string) => {
    if (await copyText(email)) show('邮箱已复制')
    else showCopyable(email, '复制失败，请手动复制')
  }, [show, showCopyable])

  async function runAction(type: 'deactivate' | 'reactivate' | 'delete') {
    if (!confirm) return
    setBusy(true); setActionError('')
    const { alias } = confirm
    try {
      if (type === 'delete') {
        await request(`/api/aliases/${encodeURIComponent(alias.anonymousId)}`, { method: 'DELETE', body: JSON.stringify({ account_id: accountId }) })
        show('别名已删除')
      } else {
        await request(`/api/aliases/${encodeURIComponent(alias.anonymousId)}/${type === 'deactivate' ? 'deactivate' : 'reactivate'}`, { method: 'POST', body: JSON.stringify({ account_id: accountId }) })
        show(type === 'deactivate' ? '别名已停用' : '别名已激活')
      }
      setConfirm(null)
      setRetryKey((k) => k + 1)
    } catch (err) {
      setActionError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally { setBusy(false) }
  }

  function handleCreated(email: string) { setCreateOpen(false); showCopyable(email); setRetryKey((k) => k + 1) }

  if (accounts.length === 0 && !loading && !error) {
    return <p className="empty-state">暂无账号，请先到「账号」页面添加账号</p>
  }

  const confirmTitle = confirm?.type === 'delete' ? '删除别名' : confirm?.type === 'deactivate' ? '停用别名' : '激活别名'
  const confirmLabel = confirm?.type === 'delete' ? '确认删除' : confirm?.type === 'deactivate' ? '确认停用' : '确认激活'

  return (
    <section className="aliases-page">
      <div className="aliases-header">
        <div className="aliases-title">
          <h2>别名管理</h2>
          <p>创建、停用、激活或删除 Hide My Email 别名</p>
        </div>
        <div className="aliases-toolbar">
          <SelectMenu
            className="aliases-account-select"
            ariaLabel="选择账号"
            value={accountId}
            options={accounts.map((a) => ({ value: a.id, label: a.name }))}
            onChange={(next) => {
              setAccountId(next)
              setSearchParams({ account_id: next }, { replace: true })
            }}
          />
          <button className="aliases-ghost-btn" onClick={() => navigate('/alias-tasks')} disabled={accounts.length === 0}>
            <IconClock size={16} />
            自动创建任务
          </button>
          <button className="aliases-primary-btn" onClick={() => setCreateOpen(true)} disabled={!accountId}>
            <IconPlus size={16} />
            创建别名
          </button>
          <SelectMenu
            ghost
            className="aliases-status-filter"
            ariaLabel="状态筛选"
            value={filter}
            options={[
              { value: 'all', label: '全部状态' },
              { value: 'active', label: '已启用' },
              { value: 'inactive', label: '已停用' },
            ]}
            onChange={(next) => setFilter(next as 'all' | 'active' | 'inactive')}
          />
        </div>
      </div>

      <div className="aliases-toolbar-row">
        <div className="aliases-search">
          <IconSearch size={16} aria-hidden="true" />
          <label className="visually-hidden" htmlFor="alias-search">搜索</label>
          <input
            id="alias-search"
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="按邮箱或标签搜索"
          />
        </div>
      </div>

      <AsyncState
        loading={loading}
        error={error}
        empty={filtered.length === 0}
        emptyText={aliases.length === 0 ? '暂无别名' : '没有匹配的别名'}
        onRetry={handleRetry}
      >
        <div className="aliases-table-card">
          <table className="aliases-table">
            <thead>
              <tr>
                <th>邮箱</th>
                <th>标签</th>
                <th>状态</th>
                <th aria-sort={sortDirection === 'asc' ? 'ascending' : 'descending'}>
                  <button
                    type="button"
                    className="aliases-sort-btn"
                    onClick={() => setSortDirection((direction) => (direction === 'asc' ? 'desc' : 'asc'))}
                    aria-label={`创建时间排序：当前${sortDirection === 'asc' ? '正序' : '倒序'}，点击切换为${sortDirection === 'asc' ? '倒序' : '正序'}`}
                    title="点击切换创建时间排序"
                  >
                    <span>创建时间</span>
                    {sortDirection === 'asc' ? <IconChevronUp size={14} /> : <IconChevronDown size={14} />}
                  </button>
                </th>
                <th className="aliases-th-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((alias) => (
                <tr key={alias.anonymousId}>
                  <td>
                    <div className="aliases-email-cell">
                      <button type="button" className="aliases-email" onClick={() => void copyEmail(alias.email)} title="复制邮箱">
                        {alias.email}
                      </button>
                      <button
                        type="button"
                        className="aliases-copy-btn"
                        onClick={() => void copyEmail(alias.email)}
                        aria-label={`复制 ${alias.email}`}
                        title="复制邮箱"
                      >
                        <IconCopy size={14} />
                      </button>
                    </div>
                  </td>
                  <td><span className="aliases-label">{alias.label || '—'}</span></td>
                  <td>
                    <span className={`aliases-badge ${alias.active ? 'aliases-badge-active' : 'aliases-badge-inactive'}`}>
                      <span className="aliases-dot" />
                      {alias.active ? '已启用' : '已停用'}
                    </span>
                  </td>
                  <td><span className="aliases-date">{formatDate(alias.createdAt)}</span></td>
                  <td>
                    <div className="aliases-actions">
                      <Link
                        to={`/inbox?account_id=${encodeURIComponent(accountId)}&alias=${encodeURIComponent(alias.email)}`}
                        className="aliases-ghost-icon-btn"
                        title="查看此别名的收件箱"
                      >
                        <IconInbox size={16} />
                        <span>收件箱</span>
                      </Link>
                      <RowActionMenu
                        ariaLabel={`${alias.email} 更多操作`}
                        disabled={busy}
                        items={[
                          alias.active
                            ? { key: 'deactivate', label: '停用', icon: <IconClock size={15} /> }
                            : { key: 'reactivate', label: '激活', icon: <IconCheck size={15} /> },
                          { key: 'delete', label: '删除', danger: true, icon: <IconTrash size={15} /> },
                        ]}
                        onSelect={(key) => {
                          if (key === 'delete') setConfirm({ type: 'delete', alias })
                          else setConfirm({ type: key as 'deactivate' | 'reactivate', alias })
                        }}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </AsyncState>

      <CreateAliasDialog
        accountId={accountId}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={handleCreated}
      />

      {confirm && (
        <ConfirmDialog
          title={confirmTitle}
          message={
            confirm.type === 'delete'
              ? `将删除别名 ${confirm.alias.email}。此操作不可恢复，且不会影响 Apple 账号本身。`
              : confirm.type === 'deactivate'
                ? `将停用别名 ${confirm.alias.email}，之后该邮箱将不再接收邮件。`
                : `将重新激活别名 ${confirm.alias.email}。`
          }
          confirmLabel={confirmLabel}
          requireText={confirm.type === 'delete' ? confirm.alias.email : undefined}
          requireLabel={confirm.type === 'delete' ? '输入完整邮箱' : undefined}
          open
          busy={busy}
          onClose={() => setConfirm(null)}
          onConfirm={() => void runAction(confirm.type)}
        />
      )}

      {actionError && (
        <div className="alert-error" role="alert" style={{ marginTop: 16 }}>
          {actionError}
        </div>
      )}
    </section>
  )
}
