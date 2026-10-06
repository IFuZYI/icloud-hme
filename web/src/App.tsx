import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ToastProvider } from './components/ToastProvider'
import AppShell from './components/AppShell'
import LoginPage from './pages/LoginPage'
import AccountsPage from './pages/AccountsPage'
import AliasesPage from './pages/AliasesPage'
import InboxPage from './pages/InboxPage'
import AliasTasksPage from './pages/AliasTasksPage'
import LogsPage from './pages/LogsPage'

function ProtectedLayout() {
  const { status } = useAuth()
  if (status === 'checking') {
    return <p className="empty-state" aria-busy="true">加载中…</p>
  }
  if (status === 'anonymous') {
    return <Navigate to="/login" replace />
  }
  return <AppShell />
}

// antd 主题对齐项目设计系统(web/src/styles.css 的 token):
// 控件 40px 高 / 11px 圆角 / --color-border-strong 边框 / --color-text 文字色。
// 不这样做的话, DatePicker 会带着 antd 默认样式(50px 高、6px 圆角、灰边框)
// 与相邻的 SelectMenu/按钮明显错位。
const antdTheme = {
  token: {
    controlHeight: 40,
    borderRadius: 11,
    colorBorder: '#c7c7cc', // --color-border-strong
    colorText: '#1d1d1f', // --color-text
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
  },
}

export default function App() {
  return (
    <ConfigProvider locale={zhCN} theme={antdTheme}>
      <BrowserRouter>
        <AuthProvider>
          <ToastProvider>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route element={<ProtectedLayout />}>
                <Route path="/accounts" element={<AccountsPage />} />
                <Route path="/aliases" element={<AliasesPage />} />
                <Route path="/alias-tasks" element={<AliasTasksPage />} />
                <Route path="/logs" element={<LogsPage />} />
                <Route path="/inbox" element={<InboxPage />} />
                <Route path="*" element={<Navigate to="/accounts" replace />} />
              </Route>
            </Routes>
          </ToastProvider>
        </AuthProvider>
      </BrowserRouter>
    </ConfigProvider>
  )
}
