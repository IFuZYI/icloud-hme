import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

interface ProxyDialogProps {
  accountId: string
  open: boolean
  /** 当前是否已配置账号代理(决定是否提供「清除」勾选)。 */
  hasProxy?: boolean
  /** 当前是否已配置 IMAP 专用代理(决定是否提供「清除」勾选)。 */
  hasIMAPProxy?: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * 更新代理对话框:从不回显当前值。
 *
 * 提交语义与后端指针三态对齐:只提交用户真正编辑过的字段(留空=不改动),
 * 要清除已配置的代理必须显式勾选「清除」——避免只想改一个通道时把另一个
 * 静默清空(清空 IMAP 代理会让收信直连, 暴露服务器真实 IP)。
 */
export default function ProxyDialog({ accountId, open, hasProxy = false, hasIMAPProxy = false, onClose, onSaved }: ProxyDialogProps) {
  const [proxy, setProxy] = useState('')
  const [imapProxy, setImapProxy] = useState('')
  const [clearProxy, setClearProxy] = useState(false)
  const [clearIMAPProxy, setClearIMAPProxy] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    const body: { proxy?: string; imap_proxy?: string } = {}
    if (proxy.trim() !== '') body.proxy = proxy.trim()
    else if (clearProxy) body.proxy = ''
    if (imapProxy.trim() !== '') body.imap_proxy = imapProxy.trim()
    else if (clearIMAPProxy) body.imap_proxy = ''

    if (Object.keys(body).length === 0) {
      setError('请填写代理地址，或勾选「清除」以移除已配置的代理')
      return
    }

    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/proxy`, {
        method: 'PUT',
        body: JSON.stringify(body),
      })
      reset()
      onSaved()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  function reset() {
    setProxy('')
    setImapProxy('')
    setClearProxy(false)
    setClearIMAPProxy(false)
    setError('')
  }

  return (
    <Dialog
      title="设置代理"
      open={open}
      onClose={() => {
        reset()
        onClose()
      }}
    >
      {error && (
        <div className="alert-error" role="alert">
          {error}
        </div>
      )}
      <div className="form-field">
        <label htmlFor="proxy-input">代理地址</label>
        <input
          id="proxy-input"
          type="text"
          value={proxy}
          onChange={(e) => setProxy(e.target.value)}
          placeholder="http://user:pass@host:port"
          autoComplete="off"
          disabled={clearProxy}
        />
        {hasProxy && (
          <label className="checkbox-inline">
            <input type="checkbox" checked={clearProxy} onChange={(e) => setClearProxy(e.target.checked)} />
            清除已配置的账号代理
          </label>
        )}
      </div>
      <div className="form-field">
        <label htmlFor="imap-proxy-input">IMAP 代理地址</label>
        <input
          id="imap-proxy-input"
          type="text"
          value={imapProxy}
          onChange={(e) => setImapProxy(e.target.value)}
          placeholder="socks5://user:pass@host:port"
          autoComplete="off"
          disabled={clearIMAPProxy}
        />
        {hasIMAPProxy && (
          <label className="checkbox-inline">
            <input type="checkbox" checked={clearIMAPProxy} onChange={(e) => setClearIMAPProxy(e.target.checked)} />
            清除已配置的 IMAP 代理
          </label>
        )}
        <p className="hint">
          两个代理互不影响：上面的代理只管 iCloud Web/账号，IMAP 代理只管收信。输入框留空表示不改动该项；要移除已配置的代理，请勾选对应「清除」。出于安全考虑不回显当前值。
        </p>
      </div>
      <div className="form-actions">
        <button onClick={onClose}>取消</button>
        <button className="primary" onClick={() => void handleSubmit()} disabled={submitting}>
          {submitting ? '保存中…' : '保存'}
        </button>
      </div>
    </Dialog>
  )
}
