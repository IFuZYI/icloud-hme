import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

interface ICloudLoginDialogProps {
  accountId: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/** iCloud 密码登录对话框:支持 OTP 两阶段 */
export default function ICloudLoginDialog({
  accountId,
  open,
  onClose,
  onSaved,
}: ICloudLoginDialogProps) {
  const [password, setPassword] = useState('')
  const [otp, setOtp] = useState('')
  const [otpRequired, setOtpRequired] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit() {
    if (submitting) return
    setSubmitting(true)
    setError('')
    try {
      await request(`/api/accounts/${accountId}/login`, {
        method: 'POST',
        body: JSON.stringify({
          password,
          ...(otpRequired ? { otp_code: otp } : {}),
        }),
      })
      setPassword('')
      setOtp('')
      setOtpRequired(false)
      onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'OTP_REQUIRED') {
        setOtpRequired(true)
      } else {
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      title="iCloud 登录"
      open={open}
      onClose={() => {
        setPassword('')
        setOtp('')
        setOtpRequired(false)
        setError('')
        onClose()
      }}
    >
      {error && (
        <div className="alert-error" role="alert">
          {error}
        </div>
      )}
      {otpRequired && (
        <div className="alert-info">该账号启用了双重认证，请输入验证码。</div>
      )}
      {!otpRequired && (
        <div className="form-field">
          <label htmlFor="icloud-login-password">密码</label>
          <input
            id="icloud-login-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
      )}
      {otpRequired && (
        <div className="form-field">
          <label htmlFor="icloud-login-otp">验证码</label>
          <input
            id="icloud-login-otp"
            type="text"
            inputMode="numeric"
            maxLength={6}
            value={otp}
            onChange={(e) => setOtp(e.target.value.replace(/\D/g, ''))}
            autoComplete="one-time-code"
          />
        </div>
      )}
      <div className="form-actions">
        <button onClick={onClose}>取消</button>
        <button className="primary" onClick={() => void handleSubmit()} disabled={submitting}>
          {submitting ? '登录中…' : otpRequired ? '验证' : '登录'}
        </button>
      </div>
    </Dialog>
  )
}
