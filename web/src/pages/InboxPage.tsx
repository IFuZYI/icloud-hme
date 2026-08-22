import { useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import type { AccountSummary, Alias, InboxResult } from '../api/types'
import AsyncState from '../components/AsyncState'
import { IconKey, IconMail } from '../components/icons'

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

export default function InboxPage() {
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [aliases, setAliases] = useState<Alias[]>([])
  const [accountId, setAccountId] = useState('')
  const [alias, setAlias] = useState('')
  const [limit, setLimit] = useState(20)
  const [days, setDays] = useState(7)

  const [result, setResult] = useState<InboxResult | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)

  const [searchParams, setSearchParams] = useSearchParams()
  const abortRef = useRef<AbortController | null>(null)

  // 加载账号列表并初始化筛选状态(只保存 account_id/alias/limit/days)
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
        if (target) {
          const next: Record<string, string> = { account_id: target }
          const qAlias = searchParams.get('alias')
          if (qAlias) {
            setAlias(qAlias)
            next.alias = qAlias
          }
          const qLimit = searchParams.get('limit')
          if (qLimit) next.limit = qLimit
          const qDays = searchParams.get('days')
          if (qDays) next.days = qDays
          setSearchParams(next, { replace: true })
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

  // 账号变化时加载别名列表(供筛选)
  useEffect(() => {
    if (!accountId) return
    let cancelled = false
    request<{ account_id: string; count: number; aliases: Alias[] }>(
      `/api/aliases?account_id=${encodeURIComponent(accountId)}`,
    )
      .then((data) => {
        if (cancelled) return
        setAliases(data.aliases ?? [])
      })
      .catch(() => {
        if (cancelled) return
        setAliases([])
      })
    return () => {
      cancelled = true
    }
  }, [accountId])

  // 查询收件箱;账号变化时清空旧邮件并中止旧请求
  useEffect(() => {
    if (!accountId) return
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    let cancelled = false
    const params = new URLSearchParams({ account_id: accountId })
    if (alias) params.set('alias', alias)
    params.set('limit', String(limit))
    params.set('days', String(days))
    request<InboxResult>(`/api/inbox?${params.toString()}`, {
      signal: controller.signal,
    })
      .then((data) => {
        if (cancelled) return
        setResult(data)
        setError('')
      })
      .catch((err) => {
        if (cancelled || err instanceof ApiError && err.status === 0) return
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setResult(null)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
      controller.abort()
    }
  }, [accountId, alias, limit, days, retryKey])

  const qAlias = useMemo(() => alias, [alias])

  function handleSearch() {
    const next: Record<string, string> = { account_id: accountId }
    if (qAlias) next.alias = qAlias
    next.limit = String(limit)
    next.days = String(days)
    setSearchParams(next, { replace: true })
    setRetryKey((k) => k + 1)
  }

  function handleAccountChange(id: string) {
    setAccountId(id)
    setAlias('')
    setResult(null)
    setSearchParams({ account_id: id }, { replace: true })
  }

  const methodText = result?.method === 'imap' ? 'IMAP' : 'Web API'

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>收件箱摘要</h2>
          <p>查看发往隐私别名的邮件（仅显示纯文本摘要）</p>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 12 }}>
          <div className="form-field" style={{ marginBottom: 0 }}>
            <label htmlFor="inbox-account">账号</label>
            <select
              id="inbox-account"
              value={accountId}
              onChange={(e) => handleAccountChange(e.target.value)}
            >
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </div>
          <div className="form-field" style={{ marginBottom: 0 }}>
            <label htmlFor="inbox-alias">别名</label>
            <select
              id="inbox-alias"
              value={alias}
              onChange={(e) => setAlias(e.target.value)}
            >
              <option value="">全部</option>
              {aliases.map((a) => (
                <option key={a.anonymousId} value={a.email}>
                  {a.email}
                </option>
              ))}
            </select>
          </div>
          <div className="form-field" style={{ marginBottom: 0 }}>
            <label htmlFor="inbox-limit">每页</label>
            <select
              id="inbox-limit"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              <option value={1}>1</option>
              <option value={20}>20</option>
              <option value={100}>100</option>
            </select>
          </div>
          <div className="form-field" style={{ marginBottom: 0 }}>
            <label htmlFor="inbox-days">时间范围</label>
            <select
              id="inbox-days"
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            >
              <option value={1}>1 天</option>
              <option value={7}>7 天</option>
              <option value={30}>30 天</option>
              <option value={90}>90 天</option>
            </select>
          </div>
          <div style={{ display: 'flex', alignItems: 'flex-end' }}>
            <button className="primary" onClick={handleSearch}>
              查询
            </button>
          </div>
        </div>
      </div>

      <AsyncState
        loading={loading}
        error={error}
        empty={!result || result.messages.length === 0}
        emptyText="暂无邮件"
        onRetry={() => {
          setLoading(true)
          setRetryKey((k) => k + 1)
        }}
      >
        {result && result.messages.length > 0 && (
          <>
            <p className="hint" style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8 }}>
              <span>共 {result.count} 封</span>
              <span className={result.method === 'imap' ? 'badge badge-info' : 'badge badge-neutral'}>
                {result.method === 'imap' ? <IconKey size={12} /> : <IconMail size={12} />}
                读取方式：{methodText}
              </span>
            </p>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>主题</th>
                    <th>发件人</th>
                    <th>收件人</th>
                    <th>日期</th>
                    <th>摘要</th>
                  </tr>
                </thead>
                <tbody>
                  {result.messages.map((m) => (
                    <tr key={m.id}>
                      <td>{m.subject || '（无主题）'}</td>
                      <td>{m.from}</td>
                      <td>{m.to}</td>
                      <td>{formatDate(m.date)}</td>
                      <td>{m.preview}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </AsyncState>
    </section>
  )
}
