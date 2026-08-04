import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

interface ProxyDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/** 更新代理对话框:从不回显当前值 */
export default function ProxyDialog({ accountId, open, onClose, onSaved }: ProxyDialogProps) {
  const [proxy, setProxy] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/proxy`, {
        method: 'PUT',
        body: JSON.stringify({ proxy: proxy.trim() }),
      })
      setProxy('')
      onSaved()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="设置代理"
      open={open}
      onClose={() => {
        setProxy('')
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
        <label htmlFor="proxy-input">代理地址</label>
        <input
          id="proxy-input"
          type="text"
          value={proxy}
          onChange={(e) => setProxy(e.target.value)}
          placeholder="http://user:pass@host:port"
          autoComplete="off"
        />
        <p className="hint">留空并保存可清除代理；出于安全考虑不回显当前值。</p>
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
