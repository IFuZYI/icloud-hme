import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

interface AppPasswordDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/** 设置 App 专用密码对话框:提交后清空 */
export default function AppPasswordDialog({
  accountId,
  open,
  onClose,
  onSaved,
}: AppPasswordDialogProps) {
  const [email, setEmail] = useState('')
  const [appPassword, setAppPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    if (!email.trim() || !appPassword.trim()) {
      setError('请输入邮箱与 App 专用密码')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/password`, {
        method: 'POST',
        body: JSON.stringify({ icloud_email: email, app_password: appPassword }),
      })
      setEmail('')
      setAppPassword('')
      onSaved()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="设置 App 专用密码"
      open={open}
      onClose={() => {
        setEmail('')
        setAppPassword('')
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
        <label htmlFor="apppwd-email">邮箱</label>
        <input
          id="apppwd-email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
      </div>
      <div className="form-field">
        <label htmlFor="apppwd-value">App 专用密码</label>
        <input
          id="apppwd-value"
          type="password"
          autoComplete="off"
          value={appPassword}
          onChange={(e) => setAppPassword(e.target.value)}
        />
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
