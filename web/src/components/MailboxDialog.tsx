import { useState } from 'react'
import Dialog from './Dialog'
import SelectMenu from './SelectMenu'
import { request, ApiError } from '../api/client'
import type { MailboxSummary } from '../api/types'

interface MailboxDialogProps {
  accountId: string
  current?: MailboxSummary
  open: boolean
  onClose: () => void
  onSaved: () => void
}

type MailboxForm = {
  provider: string
  email: string
  host: string
  port: string
  code: string
}

function initialForm(current?: MailboxSummary): MailboxForm {
  return {
    provider: current?.provider ?? 'qq',
    email: current?.email ?? '',
    host: current?.imap_host ?? 'imap.qq.com',
    port: String(current?.imap_port ?? 993),
    code: '',
  }
}

export default function MailboxDialog({ accountId, current, open, onClose, onSaved }: MailboxDialogProps) {
  const [form, setForm] = useState(() => initialForm(current))
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  function update<K extends keyof MailboxForm>(key: K, value: MailboxForm[K]) {
    setForm((previous) => ({ ...previous, [key]: value }))
  }

  function changeProvider(value: string) {
    const presets: Record<string, [string, number]> = {
      qq: ['imap.qq.com', 993],
      gmail: ['imap.gmail.com', 993],
      outlook: ['outlook.office365.com', 993],
    }
    const preset = presets[value]
    setForm((previous) => ({
      ...previous,
      provider: value,
      ...(preset ? { host: preset[0], port: String(preset[1]) } : {}),
    }))
  }

  async function handleSubmit() {
    if (submitting) return
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/mailbox`, {
        method: 'PUT',
        body: JSON.stringify({
          provider: form.provider,
          email: form.email,
          imap_host: form.host,
          imap_port: Number(form.port),
          authorization_code: form.code,
        }),
      })
      onSaved()
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : '收件邮箱接入失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog title="接入收件邮箱" open={open} onClose={onClose}>
      {error && <div className="alert-error" role="alert">{error}</div>}
      <div className="form-field">
        <label htmlFor="mailbox-provider">邮箱服务商</label>
        <SelectMenu
          id="mailbox-provider"
          block
          ariaLabel="选择邮箱服务商"
          value={form.provider}
          options={[
            { value: 'qq', label: 'QQ 邮箱' },
            { value: 'gmail', label: 'Gmail' },
            { value: 'outlook', label: 'Outlook' },
            { value: 'custom', label: '其他' },
          ]}
          onChange={changeProvider}
        />
      </div>
      <div className="form-field"><label htmlFor="mailbox-email">收件邮箱</label><input id="mailbox-email" type="email" value={form.email} onChange={(event) => update('email', event.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-host">IMAP 服务器</label><input id="mailbox-host" value={form.host} onChange={(event) => update('host', event.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-port">SSL 端口</label><input id="mailbox-port" type="number" min="1" max="65535" value={form.port} onChange={(event) => update('port', event.target.value)} /></div>
      <div className="form-field"><label htmlFor="mailbox-code">邮箱授权码</label><input id="mailbox-code" type="password" autoComplete="off" value={form.code} onChange={(event) => update('code', event.target.value)} /></div>
      <div className="form-actions">
        <button type="button" onClick={onClose}>取消</button>
        <button type="button" className="primary" onClick={() => void handleSubmit()} disabled={submitting}>{submitting ? '验证中…' : '验证并接入'}</button>
      </div>
    </Dialog>
  )
}
