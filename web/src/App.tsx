import { useMemo } from 'react'
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
import { buildAntdTheme } from './antdTheme'
import { usePrefersDark } from './usePrefersDark'

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

export default function App() {
  // antd 主题跟随系统色: 暗色模式下用暗色算法 + 暗色 token,
  // 否则 DatePicker 等组件会在黑页面上显示为白斑。
  const dark = usePrefersDark()
  // useMemo: 保持主题对象引用稳定, 避免每次渲染都让 ConfigProvider 重算 token。
  const theme = useMemo(() => buildAntdTheme(dark), [dark])
  return (
    <ConfigProvider locale={zhCN} theme={theme}>
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
