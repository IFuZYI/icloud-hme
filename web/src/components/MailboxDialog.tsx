import { useEffect, useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'
import type { MailboxSummary } from '../api/types'

interface MailboxDialogProps {
  accountId: string
  current?: MailboxSummary
  open: boolean
  onClose: () => void
  onSaved: () => void
}

export default function MailboxDialog({ accountId, current, open, onClose, onSaved }: MailboxDialogProps) {
  const [provider, setProvider] = useState(current?.provider || 'qq')
  const [email, setEmail] = useState(current?.email || '')
  const [host, setHost] = useState(current?.imap_host || 'imap.qq.com')
  const [port, setPort] = useState(String(current?.imap_port || 993))
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open) return
    setProvider(current?.provider || 'qq')
    setEmail(current?.email || '')
    setHost(current?.imap_host || 'imap.qq.com')
    setPort(String(current?.imap_port || 993))
    setCode('')
    setError('')
  }, [open, current])

  function changeProvider(value: string) {
    setProvider(value)
    const presets: Record<string, [string, number]> = {
      qq: ['imap.qq.com', 993],
      gmail: ['imap.gmail.com', 993],
      outlook: ['outlook.office365.com', 993],
    }
    if (presets[value]) {
      setHost(presets[value][0])
      setPort(String(presets[value][1]))
    }
  }

  async function handleSubmit() {
    if (submitting) return
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/mailbox`, {
        method: 'PUT',
        body: JSON.stringify({ provider, email, imap_host: host, imap_port: Number(port), authorization_code: code }),
      })
      onSaved()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '收件邮箱接入失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog title="接入收件邮箱" open={open} onClose={onClose}>
      {error && <div className="alert-error" role="alert">{error}</div>}
      <div className="form-field">
        <label htmlFor="mailbox-provider">邮箱服务商</label>
        <select id="mailbox-provider" value={provider} onChange={(e) => changeProvider(e.target.value)}>
          <option value="qq">QQ 邮箱</option>
          <option value="gmail">Gmail</option>
          <option value="outlook">Outlook</option>
          <option value="custom">其他</option>
        </select>
      </div>
      <div className="form-field"><label htmlFor="mailbox-email">收件邮箱</label><input id="mailbox-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-host">IMAP 服务器</label><input id="mailbox-host" value={host} onChange={(e) => setHost(e.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-port">SSL 端口</label><input id="mailbox-port" type="number" min="1" max="65535" value={port} onChange={(e) => setPort(e.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-code">邮箱授权码</label><input id="mailbox-code" type="password" autoComplete="off" value={code} onChange={(e) => setCode(e.target.value)} /></div>
      <div className="form-actions">
        <button onClick={onClose}>取消</button>
        <button className="primary" onClick={() => void handleSubmit()} disabled={submitting}>{submitting ? '验证中…' : '验证并接入'}</button>
      </div>
    </Dialog>
  )
}
