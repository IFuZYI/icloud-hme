import { useState } from 'react'
import Dialog from './Dialog'
import SelectMenu from './SelectMenu'
import { request, ApiError } from '../api/client'
import SmartCookieInput from './SmartCookieInput'

interface AccountFormDialogProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  editing?: {
    id: string
    name: string
    icloudEmail: string
    host: string
  } | null
}

/** 添加账号 / 编辑基本信息对话框 */
export default function AccountFormDialog({
  open,
  onClose,
  onSaved,
  editing,
}: AccountFormDialogProps) {
  const [name, setName] = useState(editing?.name ?? '')
  const [icloudEmail, setIcloudEmail] = useState(editing?.icloudEmail ?? '')
  const [host, setHost] = useState(editing?.host ?? 'icloud.com')
  const [cookies, setCookies] = useState('')
  const [proxy, setProxy] = useState('')
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [useProxy, setUseProxy] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  function reset() {
    setName('')
    setIcloudEmail('')
    setHost('icloud.com')
    setCookies('')
    setProxy('')
    setAdvancedOpen(false)
    setUseProxy(false)
    setError('')
    setSubmitting(false)
  }

  async function handleSubmit() {
    if (submitting) return
    if (!name.trim()) {
      setError('请输入账号名称')
      return
    }
    if (!icloudEmail.trim()) {
      setError('请输入 iCloud 邮箱')
      return
    }
    setSubmitting(true)
    setError('')
    try {
      if (editing) {
        await request(`/api/accounts/${editing.id}`, {
          method: 'PATCH',
          body: JSON.stringify({ name, icloud_email: icloudEmail, host }),
        })
      } else {
        await request('/api/accounts', {
          method: 'POST',
          body: JSON.stringify({ name, icloud_email: icloudEmail, host, proxy: useProxy ? proxy : '', cookies }),
        })
      }
      reset()
      onSaved()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  function close() {
    reset()
    onClose()
  }

  return (
    <Dialog
      title={editing ? '编辑账号' : '添加账号'}
      open={open}
      onClose={close}
    >
      {error && (
        <div className="alert-error" role="alert">
          {error}
        </div>
      )}
      <div className="form-field">
        <label htmlFor="acc-name">名称</label>
        <input
          id="acc-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={64}
        />
      </div>
      <div className="form-field">
        <label htmlFor="acc-email">iCloud 邮箱</label>
        <input
          id="acc-email"
          type="email"
          value={icloudEmail}
          onChange={(e) => {
            const value = e.target.value
            setIcloudEmail(value)
            if (!editing && !name) setName(value.split('@')[0] ?? '')
            if (!editing && /@icloud\.com\.cn$/i.test(value)) setHost('icloud.com.cn')
            if (!editing && /@(icloud\.com|me\.com|mac\.com)$/i.test(value)) setHost('icloud.com')
          }}
          disabled={Boolean(editing)}
        />
      </div>
      <div className="form-field">
        <label htmlFor="acc-host">区域</label>
        <SelectMenu
          id="acc-host"
          block
          ariaLabel="选择区域"
          value={host}
          options={[
            { value: 'icloud.com', label: '全球区 (icloud.com)' },
            { value: 'icloud.com.cn', label: '中国区 (icloud.com.cn)' },
          ]}
          onChange={setHost}
          disabled={Boolean(editing)}
        />
      </div>
      {!editing && (
        <>
          <div className="advanced-config">
            <button type="button" className="advanced-config-toggle" aria-expanded={advancedOpen} onClick={() => setAdvancedOpen((value) => !value)}>
              高级配置 <span aria-hidden="true">{advancedOpen ? '−' : '+'}</span>
            </button>
            {advancedOpen && (
              <div className="advanced-config-panel">
                <SmartCookieInput id="acc-cookies" value={cookies} onChange={setCookies} />
                <fieldset className="proxy-config">
                  <legend>网络连接</legend>
                  <label><input type="radio" name="proxy-mode" checked={!useProxy} onChange={() => setUseProxy(false)} /> 直连</label>
                  <label><input type="radio" name="proxy-mode" checked={useProxy} onChange={() => setUseProxy(true)} /> 使用代理</label>
                  {useProxy && (
                    <div className="form-field proxy-config-input">
                      <label htmlFor="acc-proxy">代理地址</label>
                      <input id="acc-proxy" type="url" value={proxy} onChange={(e) => setProxy(e.target.value)} placeholder="http://user:pass@host:port" autoComplete="off" />
                      <p className="hint">支持 http、https 或 socks5 代理。</p>
                    </div>
                  )}
                </fieldset>
              </div>
            )}
          </div>
        </>
      )}
      <div className="form-actions">
        <button onClick={close}>取消</button>
        <button className="primary" onClick={() => void handleSubmit()} disabled={submitting}>
          {submitting ? '保存中…' : '保存'}
        </button>
      </div>
    </Dialog>
  )
}
