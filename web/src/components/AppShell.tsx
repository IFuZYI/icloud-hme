import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { IconAccounts, IconAliases, IconInbox, IconLogout, IconShield } from './icons'

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
        <div className="brand">
          <span className="brand-logo" aria-hidden="true">
            <IconShield size={18} />
          </span>
          <span>
            iCloud HME 管理台
            <span className="brand-sub" style={{ display: 'block' }}>
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
          <NavLink to="/inbox">
            <IconInbox />
            收件箱
          </NavLink>
        </nav>
        <span className="spacer" />
        <button onClick={() => void handleLogout()} title="退出登录">
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
