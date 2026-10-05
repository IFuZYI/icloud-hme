import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

interface CreateAliasDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onCreated: (email: string) => void
}

const MAX_LABEL_LEN = 200

/** 创建别名对话框:标签由用户自由输入(不再限定名称库)。 */
export default function CreateAliasDialog({
  accountId,
  open,
  onClose,
  onCreated,
}: CreateAliasDialogProps) {
  const [label, setLabel] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    const trimmed = label.trim()
    if (!trimmed) {
      setError('请输入标签')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      const data = await request<{ email: string }>('/api/create', {
        method: 'POST',
        body: JSON.stringify({ account_id: accountId, label: trimmed }),
      })
      setLabel('')
      onCreated(data.email)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="创建别名"
      open={open}
      onClose={() => {
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
        <label htmlFor="alias-label">标签</label>
        <input
          id="alias-label"
          type="text"
          maxLength={MAX_LABEL_LEN}
          value={label}
          onChange={(event) => setLabel(event.target.value)}
          placeholder="如 GitHub、Notion、某网站"
          autoComplete="off"
        />
        <p className="hint">标签可自由填写，仅用于本地标识；创建后会自动生成新的隐私邮箱。建议按真实用途创建，避免短时间内重复创建。</p>
      </div>
      <div className="form-actions">
        <button onClick={onClose}>取消</button>
        <button className="primary" onClick={() => void handleSubmit()} disabled={submitting}>
          {submitting ? '创建中…' : '创建'}
        </button>
      </div>
    </Dialog>
  )
}
