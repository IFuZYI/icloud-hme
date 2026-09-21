import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import { fetchAccounts } from '../api/cache'
import type { AccountSummary, Alias, FullMessage, InboxResult, InboxMessage } from '../api/types'
import AsyncState from '../components/AsyncState'
import Dialog from '../components/Dialog'
import ConfirmDialog from '../components/ConfirmDialog'
import SelectMenu from '../components/SelectMenu'
import { useToast } from '../components/ToastProvider'
import { copyText } from '../utils/clipboard'
import { formatDateTime } from '../utils/datetime'
import { IconAlert, IconCopy, IconKey, IconMail, IconRefresh, IconTrash } from '../components/icons'

const dateFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
})

const timeFormatter = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit' })

/** 把服务端时间格式化成中文日期。 */
function formatDate(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return formatDateTime(d)
}

/** 列表里用相对日期更易扫读: 今天/昨天显示时刻, 其余显示日期。 */
function formatRelativeDate(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  const now = new Date()
  const startOfDay = (v: Date) => new Date(v.getFullYear(), v.getMonth(), v.getDate()).getTime()
  const dayDiff = Math.round((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (dayDiff === 0) return `今天 ${timeFormatter.format(d)}`
  if (dayDiff === 1) return `昨天 ${timeFormatter.format(d)}`
  return dateFormatter.format(d)
}

export default function InboxPage() {
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [aliases, setAliases] = useState<Alias[]>([])
  const [accountId, setAccountId] = useState('')
  const [alias, setAlias] = useState('')
  const [pageSize, setPageSize] = useState(20)
  const [days, setDays] = useState(7)

  const [messages, setMessages] = useState<InboxMessage[]>([])
  const [total, setTotal] = useState(0)
  const [method, setMethod] = useState<'imap' | 'web_api'>('imap')
  const [warning, setWarning] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)
  const [detail, setDetail] = useState<FullMessage | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [deleteFor, setDeleteFor] = useState<InboxMessage | null>(null)
  const [deleting, setDeleting] = useState(false)

  const [searchParams, setSearchParams] = useSearchParams()
  const abortRef = useRef<AbortController | null>(null)
  const { show } = useToast()

  /** 第二阶段: 对列表里 Preview 为空的邮件分批拉摘要(每批 10 封)。 */
  const fillPreviews = useCallback(
    async (list: InboxMessage[], accountId: string) => {
      const pending = list.filter((m) => !m.preview).map((m) => m.id)
      for (let i = 0; i < pending.length; i += 10) {
        const batch = pending.slice(i, i + 10)
        try {
          const previews = await request<Record<string, string>>(
            `/api/inbox/previews?account_id=${encodeURIComponent(accountId)}`,
            { method: 'POST', body: { ids: batch } },
          )
          setMessages((prev) =>
            prev.map((m) => (previews[m.id] ? { ...m, preview: previews[m.id] } : m)),
          )
        } catch {
          // 摘要补齐是增强体验, 失败不打断列表(详情页仍可读全文)
        }
      }
    },
    [],
  )

  /** 加载一页信封(append=true 时追加, 即「加载更多」)。 */
  const loadPage = useCallback(
    async (offset: number, append: boolean) => {
      const controller = new AbortController()
      abortRef.current = controller
      const params = new URLSearchParams({
        account_id: accountId,
        limit: String(pageSize),
        offset: String(offset),
        days: String(days),
      })
      if (alias) params.set('alias', alias)
      try {
        const data = await request<InboxResult>(`/api/inbox?${params.toString()}`, {
          signal: controller.signal,
        })
        setMethod(data.method)
        setWarning(data.warning ?? '')
        setTotal(data.total)
        setError('')
        setMessages((prev) => {
          const next = append ? [...prev, ...data.messages] : data.messages
          // 去重(刷新与追加竞态时可能重叠)
          const seen = new Set<string>()
          return next.filter((m) => (seen.has(m.id) ? false : seen.add(m.id)))
        })
        // 信封到手后渐进补摘要(不阻塞首屏)
        void fillPreviews(data.messages, accountId)
      } catch (err) {
        if (err instanceof ApiError && err.status === 0) return
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        if (!append) setMessages([])
      } finally {
        setLoading(false)
        setLoadingMore(false)
        setRefreshing(false)
      }
    },
    [accountId, alias, pageSize, days, fillPreviews],
  )

  async function openMessage(message: InboxMessage) {
    setDetailLoading(true)
    try {
      const data = await request<FullMessage>(`/api/inbox/${encodeURIComponent(message.id)}?account_id=${encodeURIComponent(accountId)}`)
      setDetail(data)
    } catch (err) {
      show(err instanceof ApiError ? err.message : '读取邮件详情失败')
    } finally {
      setDetailLoading(false)
    }
  }

  async function copyBody() {
    if (!detail?.body) return
    show((await copyText(detail.body)) ? '正文已复制' : '复制失败，请手动选择')
  }

  async function deleteMessage() {
    if (!deleteFor) return
    setDeleting(true)
    try {
      await request(`/api/inbox/${encodeURIComponent(deleteFor.id)}?account_id=${encodeURIComponent(accountId)}`, { method: 'DELETE' })
      setDeleteFor(null)
      setDetail(null)
      show('邮件已删除')
      setRetryKey((key) => key + 1)
    } catch (err) {
      show(err instanceof ApiError ? err.message : '删除邮件失败')
    } finally {
      setDeleting(false)
    }
  }

  // 加载账号列表并初始化筛选状态(只保存 account_id/alias/limit/days)
  useEffect(() => {
    let cancelled = false
    fetchAccounts<AccountSummary[]>()
      .then((data) => {
        if (cancelled) return
        setAccounts(data)
        if (data.length === 0) {
          // 无账号: 没有后续收件箱查询会来关 loading, 就在这里关
          setLoading(false)
          return
        }
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
      // 不在此处关 loading: 账号返回后收件箱查询才开始(由 accountId 触发),
      // 提前置 false 会让骨架屏闪成"暂无邮件"空态。loading 由 loadPage 关闭。
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

  // 查询收件箱(第一阶段: 信封); 筛选条件变化时回到第一页
  useEffect(() => {
    if (!accountId) return
    abortRef.current?.abort()
    // 异步发起, 避免 effect 体内同步 setState 触发级联渲染警告
    const t = window.setTimeout(() => void loadPage(0, false), 0)
    return () => window.clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountId, alias, pageSize, days, retryKey])

  function runQuery() {
    const next: Record<string, string> = { account_id: accountId }
    if (alias) next.alias = alias
    next.limit = String(pageSize)
    next.days = String(days)
    setSearchParams(next, { replace: true })
    setDetail(null)
    // 保留现有列表, 只让刷新按钮进入忙碌态(loading 会让整块变成骨架屏)
    setRefreshing(true)
    setRetryKey((k) => k + 1)
  }

  /** 加载更多: 追加下一页信封。 */
  function loadMore() {
    if (loadingMore || messages.length >= total) return
    setLoadingMore(true)
    void loadPage(messages.length, true)
  }

  function handleAccountChange(id: string) {
    setAccountId(id)
    setAlias('')
    setMessages([])
    // 账号切换后旧列表已清空: 回到骨架屏, 避免闪出空列表卡片
    setLoading(true)
    setSearchParams({ account_id: id }, { replace: true })
  }

  const isImap = method === 'imap'
  const methodText = isImap ? 'IMAP' : 'Web API'
  const accountName = accounts.find((a) => a.id === accountId)?.name ?? ''
  const busy = loading || refreshing
  const hasMore = messages.length < total

  return (
    <section className="inbox-page">
      <div className="inbox-header">
        <div className="inbox-title">
          <h2>收件箱</h2>
          <p>查看发往隐私别名的邮件，仅显示纯文本摘要</p>
        </div>
        <div className="inbox-header-actions">
          <button
            className={`inbox-action${busy ? ' is-busy' : ''}`}
            onClick={() => runQuery()}
            aria-label="刷新收件箱"
            disabled={busy}
          >
            <IconRefresh size={15} />
            刷新
          </button>
        </div>
      </div>

      <div className="inbox-filters">
        <div className="inbox-field">
          <label htmlFor="inbox-account">账号</label>
          <SelectMenu
            id="inbox-account"
            block
            ariaLabel="选择账号"
            value={accountId}
            options={accounts.map((a) => ({ value: a.id, label: a.name }))}
            onChange={handleAccountChange}
          />
        </div>
        <div className="inbox-field">
          <label htmlFor="inbox-alias">别名</label>
          <SelectMenu
            id="inbox-alias"
            block
            ariaLabel="筛选别名"
            value={alias}
            options={[{ value: '', label: '全部别名' }, ...aliases.map((a) => ({ value: a.email, label: a.email }))]}
            onChange={setAlias}
          />
        </div>
        <div className="inbox-field">
          <label htmlFor="inbox-limit">每页</label>
          <SelectMenu
            id="inbox-limit"
            block
            ariaLabel="每页数量"
            value={String(pageSize)}
            options={[
              { value: '10', label: '10' },
              { value: '20', label: '20' },
              { value: '50', label: '50' },
            ]}
            onChange={(v) => setPageSize(Number(v))}
          />
        </div>
        <div className="inbox-field">
          <label htmlFor="inbox-days">时间范围</label>
          <SelectMenu
            id="inbox-days"
            block
            ariaLabel="时间范围"
            value={String(days)}
            options={[
              { value: '1', label: '近 1 天' },
              { value: '7', label: '近 7 天' },
              { value: '30', label: '近 30 天' },
              { value: '90', label: '近 90 天' },
              { value: '0', label: '全部' },
            ]}
            onChange={(v) => setDays(Number(v))}
          />
        </div>
        <div className="inbox-field inbox-field-submit">
          <button className="inbox-action primary" onClick={() => runQuery()}>
            查询
          </button>
        </div>
      </div>

      {warning && (
        <div className="alert-warning" role="status">
          <IconAlert size={16} />
          <span>{warning}</span>
        </div>
      )}

      {/* 摘要行放在 AsyncState 之外: 空结果时 AsyncState 只渲染空状态,
          放在里面会导致"共 0 封/读取方式"永远看不到 */}
      {!loading && !error && (
        <div className="inbox-summary">
          <span className="inbox-count">
            共 <strong>{total}</strong> 封{hasMore ? `（已加载 ${messages.length}）` : ''}
          </span>
          <span className={isImap ? 'badge badge-info' : 'badge badge-neutral'}>
            {isImap ? <IconKey size={12} /> : <IconMail size={12} />}
            读取方式：{methodText}
          </span>
          <span className="inbox-summary-scope">
            {accountName ? `${accountName} · ` : ''}
            {alias ? `仅 ${alias} · ` : ''}
            {days > 0 ? `近 ${days} 天` : '全部时间'}
          </span>
        </div>
      )}

      <AsyncState
        loading={loading}
        error={error}
        // 必须等拿到结果才判断"空": 否则 accounts 先返回时会短暂闪出"暂无邮件"
        empty={!loading && messages.length === 0}
        emptyText="暂无邮件"
        onRetry={() => {
          setLoading(true)
          setRetryKey((k) => k + 1)
        }}
      >
        <ul className="inbox-list">
          {messages.map((m) => (
            <li className="inbox-item" key={m.id}>
              <button
                className="inbox-item-main"
                aria-label={`查看邮件：${m.subject || '（无主题）'}`}
                onClick={() => void openMessage(m)}
              >
                <span className="inbox-item-top">
                  <span className="inbox-sender" title={m.from || undefined}>{m.from || '未知发件人'}</span>
                  <span className="inbox-divider" aria-hidden="true">·</span>
                  <span className={`inbox-subject${m.subject ? '' : ' is-empty'}`} title={m.subject || undefined}>
                    {m.subject || '（无主题）'}
                  </span>
                </span>
                <span className="inbox-item-date">{formatRelativeDate(m.date)}</span>
                <span className="inbox-preview">
                  {m.to && <span className="inbox-recipient" title={m.to}>{m.to}</span>}
                  {/* 摘要渐进加载: 未到时显示骨架条, 不占住列表 */}
                  {m.preview
                    ? <span className="inbox-preview-text">{m.preview}</span>
                    : <span className="inbox-preview-text is-loading" aria-label="摘要加载中" />}
                </span>
              </button>
              <div className="inbox-item-actions">
                <button
                  className="inbox-delete-btn"
                  aria-label={`删除邮件：${m.subject || '（无主题）'}`}
                  title="删除邮件"
                  onClick={() => setDeleteFor(m)}
                >
                  <IconTrash size={15} />
                </button>
              </div>
            </li>
          ))}
        </ul>
        {hasMore && (
          <div className="inbox-load-more">
            <button
              className="inbox-action"
              onClick={loadMore}
              disabled={loadingMore}
              aria-label="加载更多邮件"
            >
              {loadingMore ? '加载中…' : `加载更多（还剩 ${total - messages.length} 封）`}
            </button>
          </div>
        )}
      </AsyncState>

      <Dialog title={detail?.subject || '邮件详情'} open={detail !== null || detailLoading} onClose={() => setDetail(null)}>
        {detailLoading && <p className="hint">读取中…</p>}
        {detail && (
          <>
            <dl className="mail-meta">
              <dt>发件人</dt>
              <dd>{detail.from || '—'}</dd>
              <dt>收件人</dt>
              <dd className="is-mono">{detail.to || '—'}</dd>
              <dt>日期</dt>
              <dd>{formatDate(detail.date)}</dd>
            </dl>
            <pre className={`mail-body${detail.body ? '' : ' is-empty'}`}>{detail.body || '无正文'}</pre>
            <div className="mail-detail-actions">
              <button className="inbox-action" onClick={() => void copyBody()} disabled={!detail.body}>
                <IconCopy size={15} />
                复制正文
              </button>
              <button className="inbox-action" onClick={() => setDeleteFor(detail)}>删除邮件</button>
              <button className="inbox-action primary" onClick={() => setDetail(null)}>关闭</button>
            </div>
          </>
        )}
      </Dialog>
      {deleteFor && <ConfirmDialog title="删除邮件" message="邮件将从收件箱中永久删除。" open busy={deleting} onClose={() => setDeleteFor(null)} onConfirm={() => void deleteMessage()} />}
    </section>
  )
}
