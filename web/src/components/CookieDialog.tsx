import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'
import SmartCookieInput from './SmartCookieInput'

interface CookieDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/** 更新 Cookie 对话框:提交后清空 textarea */
export default function CookieDialog({ accountId, open, onClose, onSaved }: CookieDialogProps) {
  const [cookies, setCookies] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    if (!cookies.trim()) {
      setError('请输入 Cookie')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/cookies`, {
        method: 'PUT',
        body: JSON.stringify({ cookies }),
      })
      setCookies('')
      onSaved()
    } catch (err) {      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="更新 Cookie"
      open={open}
      onClose={() => {
        setCookies('')
        setError('')
        onClose()
      }}
    >
      {error && (
        <div className="alert-error" role="alert">
          {error}
        </div>
      )}
      <div className="form-field">
        <SmartCookieInput
          id="cookie-input"
          value={cookies}
          onChange={setCookies}
          required
          disabled={submitting}
        />
        <ul className="cookie-help">
          <li><code>X-APPLE-DS-WEB-SESSION-TOKEN</code>：iCloud Web 端会话 Token，包含当前登录会话的权限。</li>
          <li><code>X-APPLE-WEBAUTH-HSA-TRUST</code>：双重认证（2FA）受信任设备/浏览器标记。</li>
          <li><code>X-APPLE-WEBAUTH-PCS-*</code>：用于保护各类 iCloud 数据的保护类密钥（Protected Cloud Storage）凭证。</li>
          <li><code>X-APPLE-WEBAUTH-TOKEN / X-APPLE-WEBAUTH-VALIDATE</code>：用于验证请求合法性及用户身份的认证 Token。</li>
        </ul>
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
