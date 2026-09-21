import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

type LoginStep = 'PASSWORD_INPUT' | 'LOADING' | '2FA_INPUT' | 'SUCCESS' | 'FAIL'

interface ICloudLoginDialogProps {
  accountId: string
  accountEmail: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/** iCloud 密码登录对话框:支持 OTP 两阶段 */
export default function ICloudLoginDialog({
  accountId,
  accountEmail,
  open,
  onClose,
  onSaved,
}: ICloudLoginDialogProps) {
  const [password, setPassword] = useState('')
  const [otp, setOtp] = useState('')
  const [step, setStep] = useState<LoginStep>('PASSWORD_INPUT')
  const [error, setError] = useState('')
  const otpRequired = step === '2FA_INPUT'
  const submitting = step === 'LOADING'

  function reset() {
    setPassword('')
    setOtp('')
    setStep('PASSWORD_INPUT')
    setError('')
  }

  async function handleSubmit() {
    if (submitting) return
    if (!otpRequired && !password) {
      setError('请输入 iCloud 密码')
      return
    }
    if (otpRequired && otp.length !== 6) {
      setError('请输入 6 位验证码')
      return
    }
    setStep('LOADING')
    setError('')
    try {
      await request(`/api/accounts/${accountId}/login`, {
        method: 'POST',
        body: JSON.stringify({
          password,
          ...(otpRequired ? { otp_code: otp } : {}),
        }),
      })
      setStep('SUCCESS')
      setPassword('')
      setOtp('')
      onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'OTP_REQUIRED') {
        setStep('2FA_INPUT')
      } else {
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setStep(otpRequired ? '2FA_INPUT' : 'FAIL')
      }
    }
  }

  return (
    <Dialog
      title="iCloud 登录"
      open={open}
      onClose={() => {
        reset()
        onClose()
      }}
    >
      <p className="login-account-context">正在登录：<strong>{accountEmail || accountId}</strong></p>
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
            name="password"
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
            pattern="[0-9]*"
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
          {submitting ? '登录中…' : otpRequired ? '验证并登录' : '登录'}
        </button>
      </div>
    </Dialog>
  )
}
