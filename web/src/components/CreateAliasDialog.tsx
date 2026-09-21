import { useEffect, useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'
import SelectMenu from './SelectMenu'

interface CreateAliasDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onCreated: (email: string) => void
}

/** 创建别名对话框 */
export default function CreateAliasDialog({
  accountId,
  open,
  onClose,
  onCreated,
}: CreateAliasDialogProps) {
  const [labels, setLabels] = useState<string[]>([])
  const [label, setLabel] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open || labels.length > 0) return
    let cancelled = false
    request<string[]>('/api/alias-labels')
      .then((items) => {
        if (cancelled) return
        setLabels(items)
        setLabel((current) => current || items[0] || '')
      })
      .catch((cause) => {
        if (!cancelled) setError(cause instanceof ApiError ? cause.message : '名称库加载失败')
      })
    return () => { cancelled = true }
  }, [open, labels.length])

  async function handleSubmit() {
    if (submitting) return
    if (!label) {
      setError('请选择标签')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      const data = await request<{ email: string }>('/api/create', {
        method: 'POST',
        body: JSON.stringify({ account_id: accountId, label }),
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
        <SelectMenu
          id="alias-label"
          block
          ariaLabel="选择标签"
          value={label}
          options={labels.map((item) => ({ value: item, label: item }))}
          onChange={setLabel}
          disabled={labels.length === 0}
        />
        <p className="hint">标签从名称库选择；创建后会自动生成新的隐私邮箱。建议按真实用途创建，避免短时间内重复创建。</p>
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
