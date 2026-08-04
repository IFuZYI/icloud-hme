import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'

export default function AppShell() {
  const { logout } = useAuth()
  const navigate = useNavigate()

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <header>
        <h1>iCloud HME 管理台</h1>
        <nav aria-label="主导航">
          <NavLink to="/accounts">账号</NavLink>
          <NavLink to="/aliases">别名</NavLink>
          <NavLink to="/inbox">收件箱</NavLink>
        </nav>
        <span className="spacer" />
        <button onClick={() => void handleLogout()}>退出登录</button>
      </header>
      <main id="main-content">
        <Outlet />
      </main>
    </div>
  )
}
