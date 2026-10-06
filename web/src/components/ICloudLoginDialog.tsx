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

interface TrustedPhone {
  id: number
  number_with_dial_code: string
}

/**
 * iCloud 密码登录对话框。
 *
 * 两段式流程(避免在单个请求里阻塞等待验证码):
 *  1. POST /login/begin 提交密码 → 无需 2FA 直接完成;需要时返回 session_id
 *     与 push_sent(验证码是否已推送到受信任设备);
 *  2. POST /login/otp 提交验证码(可重试,会话保留)。
 *
 * 推送失败时提供两条恢复路径:重发推送、改用短信验证。
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
  const [notice, setNotice] = useState('')
  const [pushSent, setPushSent] = useState(true)
  const [smsPhone, setSmsPhone] = useState<TrustedPhone | null>(null)
  const [busy, setBusy] = useState(false)
  // 只要已拿到 session_id 就处于验证码流——LOADING 期间也必须保持验证码 UI,
  // 否则提交在途时界面会闪回密码输入框、恢复按钮随之消失(审查探针抓到的缺陷)。
  const inOtpFlow = sessionId !== ''
  const otpRequired = inOtpFlow && step !== 'SUCCESS'
  const submitting = step === 'LOADING' || busy

  function reset() {
    setPassword('')
    setOtp('')
    setSessionId('')
    setStep('PASSWORD_INPUT')
    setError('')
    setNotice('')
    setPushSent(true)
    setSmsPhone(null)
    setBusy(false)
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
    setNotice('')
    try {
      if (!otpRequired) {
        const data = await request<{ status: string; session_id?: string; push_sent?: boolean }>(
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
          // push_sent 缺省视为已推送(兼容旧服务端)。
          setPushSent(data.push_sent !== false)
          setStep('2FA_INPUT')
          return
        }
        // 无需 2FA,登录已完成。
        setStep('SUCCESS')
        setPassword('')
        onSaved()
        return
      }
      const body: Record<string, unknown> = { session_id: sessionId, code: otp }
      if (smsPhone) {
        body.method = 'sms'
        body.phone_id = smsPhone.id
      }
      await request(`/api/accounts/${accountId}/login/otp`, {
        method: 'POST',
        body: JSON.stringify(body),
      })
      setStep('SUCCESS')
      setOtp('')
      onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'OTP_REQUIRED') {
        setStep('2FA_INPUT')
        return
      }
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      // 承重行为: 密码阶段失败留在密码输入(输入框内容天然保留, 可直接重试),
      // 验证码阶段失败留在验证码流(服务端保留会话)。凭据错误(INVALID_CREDENTIALS)
      // 走通用路径落到这里, 无需单独分支。
      setStep(otpRequired ? '2FA_INPUT' : 'PASSWORD_INPUT')
    }
  }

  /** 重新请求推送验证码(推送失败或未收到时的恢复路径)。 */
  async function handleResend() {
    if (busy || !sessionId) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await request(`/api/accounts/${accountId}/login/resend`, {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId }),
      })
      setPushSent(true)
      setNotice('已重新请求推送验证码到受信任设备。')
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '重发失败，请检查服务状态')
    } finally {
      setBusy(false)
    }
  }

  /** 改用短信验证: 取受信任手机号并向第一个号码发送短信验证码。 */
  async function handleUseSMS() {
    if (busy || !sessionId) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const data = await request<{ phones: TrustedPhone[] }>(
        `/api/accounts/${accountId}/login/phones?session_id=${sessionId}`,
      )
      if (!data.phones || data.phones.length === 0) {
        setError('该账号没有受信任手机号，无法使用短信验证')
        return
      }
      const phone = data.phones[0]
      await request(`/api/accounts/${accountId}/login/sms`, {
        method: 'POST',
        body: JSON.stringify({ session_id: sessionId, phone_id: phone.id }),
      })
      setSmsPhone(phone)
      setNotice(`已向 ${phone.number_with_dial_code} 发送短信验证码。`)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '发送短信验证码失败，请检查服务状态')
    } finally {
      setBusy(false)
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
      {otpRequired && !notice && (
        <div className={pushSent ? 'alert-info' : 'alert-warning'} role="status">
          {pushSent
            ? '验证码已推送到受信任设备。'
            : '验证码推送失败。可点击「重发验证码」，或改用短信验证。'}
        </div>
      )}
      {notice && (
        <div className="alert-info" role="status">
          {notice}
        </div>
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
      {otpRequired && (
        <div className="form-actions login-recovery-actions">
          <button type="button" className="text-button" onClick={() => void handleResend()} disabled={submitting}>
            重发验证码
          </button>
          <button type="button" className="text-button" onClick={() => void handleUseSMS()} disabled={submitting}>
            改用短信验证
          </button>
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
