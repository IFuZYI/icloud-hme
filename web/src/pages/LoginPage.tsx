import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { ApiError } from '../api/client'

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
      <h1>iCloud HME 管理台</h1>
      <form onSubmit={handleSubmit} className="card login-form">
        {error && (
          <div className="alert-error" role="alert">
            {error}
          </div>
        )}
        <div className="form-field">
          <label htmlFor="admin-password">管理员密码</label>
          <input
            id="admin-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
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
