import { useState } from 'react'
import Dialog from './Dialog'
import { request, ApiError } from '../api/client'

type LoginStep = 'PASSWORD_INPUT' | 'LOADING' | '2FA_INPUT' | 'SUCCESS'

interface ICloudLoginDialogProps {
  accountId: string
  accountEmail: string
  open: boolean
  onClose: () => void
  onSaved: () => void
}

/**
 * iCloud 密码登录对话框。
 *
 * 两段式流程(避免在单个请求里阻塞等待验证码):
 *  1. POST /login/begin 提交密码 → 无需 2FA 直接完成;需要时返回 session_id;
 *  2. POST /login/otp 提交验证码(可重试,会话保留)。
 */
export default function ICloudLoginDialog({
  accountId,
  accountEmail,
  open,
  onClose,
  onSaved,
}: ICloudLoginDialogProps) {
  const [password, setPassword] = useState('')
  const [otp, setOtp] = useState('')
  const [sessionId, setSessionId] = useState('')
  const [step, setStep] = useState<LoginStep>('PASSWORD_INPUT')
  const [error, setError] = useState('')
  const otpRequired = step === '2FA_INPUT'
  const submitting = step === 'LOADING'

  function reset() {
    setPassword('')
    setOtp('')
    setSessionId('')
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
      if (!otpRequired) {
        const data = await request<{ status: string; session_id?: string }>(
          `/api/accounts/${accountId}/login/begin`,
          { method: 'POST', body: JSON.stringify({ password }) },
        )
        if (data.status === 'otp_required') {
          // otp_required 必须携带 session_id; 缺失说明服务端违背契约,
          // 静默兜底成空串只会让下一步提交 {session_id: ''} 被 400。
          if (!data.session_id) {
            setError('服务端未返回登录会话，请重试')
            setStep('PASSWORD_INPUT')
            return
          }
          setSessionId(data.session_id)
          setStep('2FA_INPUT')
          return
        }
        // 无需 2FA,登录已完成。
        setStep('SUCCESS')
        setPassword('')
        onSaved()
        return
      }
      await request(`/api/accounts/${accountId}/login/otp`, {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId, code: otp }),
      })
      setStep('SUCCESS')
      setOtp('')
      onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'OTP_REQUIRED') {
        setStep('2FA_INPUT')
        return
      }
      if (err instanceof ApiError && err.code === 'INVALID_CREDENTIALS') {
        // 密码错误: 回到密码输入阶段并保留输入框, 便于修正后重试。
        setError(err.message)
        setStep('PASSWORD_INPUT')
        return
      }
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      // 输错验证码时保留在验证码流,可直接重输;密码阶段失败则回到输入。
      setStep(otpRequired ? '2FA_INPUT' : 'PASSWORD_INPUT')
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
