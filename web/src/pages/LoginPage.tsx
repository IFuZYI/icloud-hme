import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { ApiError } from '../api/client'
import { IconLock, IconShield } from '../components/icons'

export default function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting || !password) return
    setSubmitting(true)
    setError('')
    try {
      await login(password)
      navigate('/accounts', { replace: true })
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态',
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="login-page">
      <div className="login-brand">
        <span className="logo" aria-hidden="true">
          <IconShield size={28} />
        </span>
        <h1>iCloud HME 管理台</h1>
        <p>管理你的 iCloud 隐藏邮箱别名与邮件</p>
      </div>
      <form onSubmit={handleSubmit} className="card login-form">
        {error && (
          <div className="alert-error" role="alert">
            {error}
          </div>
        )}
        <div className="form-field">
          <label htmlFor="admin-password">管理员密码</label>
          <div style={{ position: 'relative' }}>
            <input
              id="admin-password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              placeholder="请输入管理员密码"
              style={{ paddingRight: 40 }}
            />
            <span
              aria-hidden="true"
              style={{
                position: 'absolute',
                right: 12,
                top: '50%',
                transform: 'translateY(-50%)',
                color: 'var(--color-text-tertiary)',
                display: 'flex',
              }}
            >
              <IconLock size={18} />
            </span>
          </div>
        </div>
        <div className="form-actions">
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? '登录中…' : '登录'}
          </button>
        </div>
      </form>
    </div>
  )
}
