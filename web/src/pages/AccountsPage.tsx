import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import { fetchAccounts, invalidateAccounts } from '../api/cache'
import { formatDateTime } from '../utils/datetime'
import type { AccountSummary } from '../api/types'
import AsyncState from '../components/AsyncState'
import AccountFormDialog from '../components/AccountFormDialog'
import CookieDialog from '../components/CookieDialog'
import ICloudLoginDialog from '../components/ICloudLoginDialog'
import AppPasswordDialog from '../components/AppPasswordDialog'
import ProxyDialog from '../components/ProxyDialog'
import MailboxDialog from '../components/MailboxDialog'
import MoreActionsDropdown, { type AccountAction } from '../components/MoreActionsDropdown'
import ConfirmDialog from '../components/ConfirmDialog'
import { useToast } from '../components/ToastProvider'
import {
  IconCheck,
  IconClock,
  IconAlert,
  IconPlus,
  IconEdit,
  IconTrash,
} from '../components/icons'

const statusMeta: Record<string, { text: string; badge: string; icon: typeof IconCheck }> = {
  active: { text: '正常', badge: 'badge badge-active', icon: IconCheck },
  pending: { text: '待配置', badge: 'badge badge-pending', icon: IconClock },
  error: { text: '异常', badge: 'badge badge-error', icon: IconAlert },
}

function StatusBadge({ status }: { status: string }) {
  const meta = statusMeta[status] ?? {
    text: status,
    badge: 'badge badge-neutral',
    icon: IconClock,
  }
  const Icon = meta.icon
  return (
    <span className={meta.badge}>
      <Icon />
      {meta.text}
    </span>
  )
}

function credText(acc: AccountSummary): string {
  const parts: string[] = []
  if (acc.has_cookies) parts.push('Cookie')
  if (acc.has_app_password) parts.push('App密码')
  if (acc.mailbox) parts.push(`收件箱:${acc.mailbox.email}`)
  if (acc.has_proxy) parts.push('代理')
  return parts.length > 0 ? `已配置（${parts.join('·')}）` : '未配置'
}

/** 最近验证时间: 空值显示占位符, 其余格式化为本地可读时间(服务端返回 RFC3339)。 */
function formatValidated(raw: string): string {
  if (!raw) return '—'
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return formatDateTime(d)
}

export default function AccountsPage() {
  const [accounts, setAccounts] = useState<AccountSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)

  // dialog 状态
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<AccountSummary | null>(null)
  const [formSession, setFormSession] = useState(0)
  const [cookieFor, setCookieFor] = useState<AccountSummary | null>(null)
  const [loginFor, setLoginFor] = useState<AccountSummary | null>(null)
  const [appPwdFor, setAppPwdFor] = useState<AccountSummary | null>(null)
  const [proxyFor, setProxyFor] = useState<AccountSummary | null>(null)
  const [mailboxFor, setMailboxFor] = useState<AccountSummary | null>(null)
  const [deleteFor, setDeleteFor] = useState<AccountSummary | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [checkingId, setCheckingId] = useState<string | null>(null)

  const { show } = useToast()
  const navigate = useNavigate()

  // 账号页是写操作入口: 每次刷新都让共享缓存失效并重新拉取,
  // 保证别名/收件箱/任务页下次读取拿到最新账号数据。
  const load = useCallback(async () => {
    setLoading(true)
    try {
      invalidateAccounts()
      const data = await fetchAccounts<AccountSummary[]>()
      setAccounts(data)
      setError('')
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    fetchAccounts<AccountSummary[]>()
      .then((data) => {
        if (cancelled) return
        setAccounts(data)
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
  }, [retryKey])

  function handleRetry() {
    setLoading(true)
    invalidateAccounts()
    setRetryKey((k) => k + 1)
  }

  async function handleDelete() {
    if (!deleteFor) return
    setDeleting(true)
    try {
      await request(`/api/accounts/${deleteFor.id}`, { method: 'DELETE' })
      setDeleteFor(null)
      show('账号已删除')
      void load()
    } catch (err) {
      show(err instanceof ApiError ? err.message : '删除失败')
    } finally {
      setDeleting(false)
    }
  }

  function handleMoreAction(account: AccountSummary, action: AccountAction) {
    switch (action) {
      case 'cookies': setCookieFor(account); break
      case 'login': setLoginFor(account); break
      case 'check': void handleCheck(account); break
      case 'password': setAppPwdFor(account); break
      case 'mailbox': setMailboxFor(account); break
      case 'proxy': setProxyFor(account); break
      case 'aliases': navigate(`/aliases?account_id=${encodeURIComponent(account.id)}`); break
      case 'inbox': navigate(`/inbox?account_id=${encodeURIComponent(account.id)}`); break
    }
  }

  /** 手动检测登录态: 调用 check 接口, 成功/失败都刷新列表以更新状态徽章。 */
  async function handleCheck(account: AccountSummary) {
    if (checkingId) return
    setCheckingId(account.id)
    try {
      await request(`/api/accounts/${account.id}/check`, { method: 'POST' })
      show('登录状态有效')
    } catch (err) {
      show(err instanceof ApiError ? err.message : '检测失败，请稍后重试')
    } finally {
      setCheckingId(null)
      void load()
    }
  }

  return (
    <section>
      <div className="page-header">
        <div className="page-title">
          <h2>账号管理</h2>
          <p>管理 iCloud 账号、Cookie 与登录凭据</p>
        </div>
        <button
          className="primary"
          onClick={() => {
            setEditing(null)
            setFormSession((session) => session + 1)
            setFormOpen(true)
          }}
        >
          <IconPlus size={16} />
          添加账号
        </button>
      </div>

      <AsyncState
        loading={loading}
        error={error}
        empty={accounts.length === 0}
        emptyText="暂无账号，点击“添加账号”开始"
        onRetry={handleRetry}
      >
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>邮箱</th>
                <th>状态</th>
                <th>别名</th>
                <th>凭据</th>
                <th>最近验证</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((acc) => (
                <tr key={acc.id}>
                  <td>
                    <span className="cell-truncate" title={acc.name}>
                      {acc.name}
                    </span>
                    {acc.status_message && (
                      <span className="hint" style={{ display: 'block' }}>
                        {acc.status_message}
                      </span>
                    )}
                  </td>
                  <td>
                    <span className="cell-truncate" title={acc.icloud_email || acc.real_email || ''}>
                      {acc.icloud_email || acc.real_email || '—'}
                    </span>
                    <span className="cell-secondary">{acc.id}</span>
                  </td>
                  <td>
                    <StatusBadge status={acc.status} />
                  </td>
                  <td>
                    <span className="cell-strong alias-count">
                      {acc.alias_active} / {acc.alias_total}
                    </span>
                  </td>
                  <td><span className="cell-nowrap">{credText(acc)}</span></td>
                  <td><span className="cell-nowrap">{formatValidated(acc.last_validated)}</span></td>
                  <td>
                    <div className="account-row-actions">
                      <button className="account-edit-button" onClick={() => { setEditing(acc); setFormSession((session) => session + 1); setFormOpen(true) }}>
                        <IconEdit size={14} />
                        编辑
                      </button>
                      <button className="account-delete-button" onClick={() => setDeleteFor(acc)}>
                        <IconTrash size={14} />
                        删除
                      </button>
                      <MoreActionsDropdown
                        accountName={acc.name}
                        onAction={(action) => handleMoreAction(acc, action)}
                        checkDisabled={!acc.has_cookies}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </AsyncState>

      <AccountFormDialog
        key={formSession}
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onSaved={() => {
          setFormOpen(false)
          show('账号已保存')
          void load()
        }}
        editing={editing ? { id: editing.id, name: editing.name, icloudEmail: editing.icloud_email, host: editing.host } : null}
      />
      {cookieFor && (
        <CookieDialog
          accountId={cookieFor.id}
          open
          onClose={() => setCookieFor(null)}
          onSaved={() => {
            show('Cookie 已更新')
            void load()
          }}
        />
      )}
      {loginFor && (
        <ICloudLoginDialog
          accountId={loginFor.id}
          accountEmail={loginFor.icloud_email || loginFor.real_email}
          open
          onClose={() => setLoginFor(null)}
          onSaved={() => {
            setLoginFor(null)
            show('登录成功')
            void load()
          }}
        />
      )}
      {appPwdFor && (
        <AppPasswordDialog
          accountId={appPwdFor.id}
          accountEmail={appPwdFor.icloud_email || appPwdFor.real_email}
          open
          onClose={() => setAppPwdFor(null)}
          onSaved={() => {
            setAppPwdFor(null)
            show('App 专用密码已设置')
            void load()
          }}
        />
      )}
      {proxyFor && (
        <ProxyDialog
          accountId={proxyFor.id}
          open
          onClose={() => setProxyFor(null)}
          onSaved={() => {
            show('代理已更新')
            void load()
          }}
        />
      )}
      {mailboxFor && (
        <MailboxDialog
          accountId={mailboxFor.id}
          current={mailboxFor.mailbox}
          open
          onClose={() => setMailboxFor(null)}
          onSaved={() => { setMailboxFor(null); show('收件邮箱已接入'); void load() }}
        />
      )}
      {deleteFor && (
        <ConfirmDialog
          title="删除账号"
          message={`将移除本地账号配置「${deleteFor.name}」，不会删除 Apple 账号本身。`}
          requireText={deleteFor.name}
          open
          busy={deleting}
          onClose={() => setDeleteFor(null)}
          onConfirm={() => void handleDelete()}
        />
      )}
    </section>
  )
}
