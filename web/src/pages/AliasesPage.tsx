import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import type { AccountSummary, Alias } from '../api/types'
import AsyncState from '../components/AsyncState'
import CreateAliasDialog from '../components/CreateAliasDialog'
import ConfirmDialog from '../components/ConfirmDialog'
import { useToast } from '../components/ToastProvider'
import { IconCheck, IconClock, IconPlus, IconSearch, IconTrash } from '../components/icons'

function formatDate(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(d)
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
  const [createOpen, setCreateOpen] = useState(false)
  const [confirm, setConfirm] = useState<{
    type: 'deactivate' | 'reactivate' | 'delete'
    alias: Alias
  } | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')

  const [searchParams, setSearchParams] = useSearchParams()
  const { show } = useToast()

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
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 加载别名列表
  useEffect(() => {
    if (!accountId) return
    let cancelled = false
    request<{ account_id: string; count: number; aliases: Alias[] }>(
      `/api/aliases?account_id=${encodeURIComponent(accountId)}`,
    )
      .then((data) => {
        if (cancelled) return
        setAliases(data.aliases ?? [])
        setError('')
      })
      .catch((err) => {
        if (cancelled) return
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [accountId, retryKey])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return aliases.filter((a) => {
      if (filter === 'active' && !a.active) return false
      if (filter === 'inactive' && a.active) return false
      if (!q) return true
      return (
        a.email.toLowerCase().includes(q) || a.label.toLowerCase().includes(q)
      )
    })
  }, [aliases, search, filter])

  function handleRetry() {
    setLoading(true)
    setRetryKey((k) => k + 1)
  }

  const copyEmail = useCallback(
    async (email: string) => {
      try {
        await navigator.clipboard.writeText(email)
        show('邮箱已复制')
      } catch {
        show(`复制失败，请手动复制：${email}`)
      }
    },
    [show],
  )

  async function runAction(type: 'deactivate' | 'reactivate' | 'delete') {
    if (!confirm) return
    setBusy(true)
    setActionError('')
    const { alias } = confirm
    try {
      if (type === 'delete') {
        await request(
          `/api/aliases/${encodeURIComponent(alias.anonymousId)}`,
          { method: 'DELETE', body: JSON.stringify({ account_id: accountId }) },
        )
        show('别名已删除')
      } else {
        await request(
          `/api/aliases/${encodeURIComponent(alias.anonymousId)}/${type === 'deactivate' ? 'deactivate' : 'reactivate'}`,
          { method: 'POST', body: JSON.stringify({ account_id: accountId }) },
        )
        show(type === 'deactivate' ? '别名已停用' : '别名已激活')
      }
      setConfirm(null)
      setRetryKey((k) => k + 1)
    } catch (err) {
      setActionError(
        err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态',
      )
    } finally {
      setBusy(false)
    }
  }

  function handleCreated(email: string) {
    setCreateOpen(false)
    show(`别名已创建：${email}`)
    setRetryKey((k) => k + 1)
  }

  if (accounts.length === 0 && !loading && !error) {
    return <p className="empty-state">暂无账号，请先到「账号」页面添加账号</p>
  }

  const confirmTitle =
    confirm?.type === 'delete'
      ? '删除别名'
      : confirm?.type === 'deactivate'
        ? '停用别名'
        : '激活别名'

  const confirmLabel =
    confirm?.type === 'delete' ? '确认删除' : confirm?.type === 'deactivate' ? '确认停用' : '确认激活'

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>别名管理</h2>
          <p>创建、停用、激活或删除 Hide My Email 别名</p>
        </div>
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
          <label htmlFor="alias-account">账号</label>
          <select
            id="alias-account"
            value={accountId}
            onChange={(e) => {
              setAccountId(e.target.value)
              setSearchParams({ account_id: e.target.value }, { replace: true })
            }}
            style={{ width: 'auto' }}
          >
            {accounts.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
          <button className="primary" onClick={() => setCreateOpen(true)} disabled={!accountId}>
            <IconPlus size={16} />
            创建别名
          </button>
        </div>
      </div>

      <div style={{ display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 200 }}>
          <label htmlFor="alias-search">搜索</label>
          <div style={{ position: 'relative' }}>
            <input
              id="alias-search"
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="按邮箱或标签搜索"
              style={{ paddingRight: 36 }}
            />
            <span
              aria-hidden="true"
              style={{
                position: 'absolute',
                right: 10,
                top: '50%',
                transform: 'translateY(-50%)',
                color: 'var(--color-text-tertiary)',
                display: 'flex',
                pointerEvents: 'none',
              }}
            >
              <IconSearch size={16} />
            </span>
          </div>
        </div>
        <div>
          <label htmlFor="alias-filter">状态</label>
          <select
            id="alias-filter"
            value={filter}
            onChange={(e) => setFilter(e.target.value as 'all' | 'active' | 'inactive')}
            style={{ width: 'auto' }}
          >
            <option value="all">全部</option>
            <option value="active">已启用</option>
            <option value="inactive">已停用</option>
          </select>
        </div>
      </div>

      <AsyncState
        loading={loading}
        error={error}
        empty={filtered.length === 0}
        emptyText={aliases.length === 0 ? '暂无别名' : '没有匹配的别名'}
        onRetry={handleRetry}
      >
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>邮箱</th>
                <th>标签</th>
                <th>状态</th>
                <th>创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((alias) => (
                <tr key={alias.anonymousId}>
                  <td>
                    <button
                      type="button"
                      className="link-like"
                      onClick={() => void copyEmail(alias.email)}
                      title="复制邮箱"
                    >
                      {alias.email}
                    </button>
                  </td>
                  <td>{alias.label || '—'}</td>
                  <td>
                    <span className={alias.active ? 'badge badge-active' : 'badge badge-neutral'}>
                      {alias.active ? <IconCheck size={12} /> : <IconClock size={12} />}
                      {alias.active ? '已启用' : '已停用'}
                    </span>
                  </td>
                  <td>{alias.createdAt ? formatDate(alias.createdAt) : '—'}</td>
                  <td>
                    <div className="row-actions">
                      {alias.active ? (
                        <button
                          disabled={busy}
                          onClick={() => setConfirm({ type: 'deactivate', alias })}
                        >
                          停用
                        </button>
                      ) : (
                        <button
                          disabled={busy}
                          onClick={() => setConfirm({ type: 'reactivate', alias })}
                        >
                          激活
                        </button>
                      )}
                      <button
                        className="danger"
                        disabled={busy}
                        onClick={() => setConfirm({ type: 'delete', alias })}
                      >
                        <IconTrash size={14} />
                        删除
                      </button>
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
          requireText={
            confirm.type === 'delete' ? confirm.alias.email : undefined
          }
          requireLabel={
            confirm.type === 'delete' ? '输入完整邮箱' : undefined
          }
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

