import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

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
        <label htmlFor="cookie-input">Cookie</label>
        <textarea
          id="cookie-input"
          value={cookies}
          onChange={(e) => setCookies(e.target.value)}
          spellCheck={false}
          placeholder="a=1; b=2"
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
