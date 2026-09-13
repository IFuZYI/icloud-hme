import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { IconAccounts, IconAliases, IconInbox, IconLogout, IconShield, IconClock } from './icons'

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
        <div className="brand brand-lockup">
          <span className="brand-logo" aria-hidden="true">
            <IconShield size={18} />
          </span>
          <span className="brand-copy">
            <span className="brand-name">iCloud HME</span>
            <span className="brand-sub">
              Hide My Email
            </span>
          </span>
        </div>
        <nav aria-label="主导航">
          <NavLink to="/accounts">
            <IconAccounts />
            账号
          </NavLink>
          <NavLink to="/aliases">
            <IconAliases />
            别名
          </NavLink>
          <NavLink to="/alias-tasks">
            <IconClock />
            自动任务
          </NavLink>
          <NavLink to="/logs">
            日志
          </NavLink>
          <NavLink to="/inbox">
            <IconInbox />
            收件箱
          </NavLink>
        </nav>
        <span className="spacer" />
        <button className="shell-logout" onClick={() => void handleLogout()} title="退出登录">
          <IconLogout />
          退出登录
        </button>
      </header>
      <main id="main-content">
        <Outlet />
      </main>
    </div>
  )
}
