import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import AccountsPage from './AccountsPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary } from '../api/types'

const account: AccountSummary = {
  id: 'acc_check',
  name: '检测号',
  real_email: 'check@example.com',
  icloud_email: 'check@icloud.com',
  host: 'icloud.com',
  status: 'active',
  alias_total: 3,
  alias_active: 2,
  has_cookies: true,
  has_app_password: false,
  has_proxy: false,
  last_validated: '2026-10-01T09:00:00+08:00',
  created_at: '2026-09-01T09:00:00+08:00',
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <AccountsPage />
      </ToastProvider>
    </MemoryRouter>,
  )
}

describe('AccountsPage 登录态检测', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
  })

  it('更多操作菜单包含「检测状态」项', async () => {
    server.use(http.get('/api/accounts', () => HttpResponse.json({ success: true, data: [account] })))
    renderPage()
    await screen.findByText('检测号')
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('button', { name: /更多操作/ })[0])
    expect(screen.getByRole('menuitem', { name: '检测状态' })).toBeInTheDocument()
  })

  it('检测成功: 调用 check 接口并提示有效', async () => {
    let checkCalls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: [account] })),
      http.post('/api/accounts/:id/check', () => {
        checkCalls++
        return HttpResponse.json({
          success: true,
          data: { ...account, last_validated: '2026-10-06T12:00:00+08:00' },
        })
      }),
    )
    renderPage()
    await screen.findByText('检测号')
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('button', { name: /更多操作/ })[0])
    await user.click(screen.getByRole('menuitem', { name: '检测状态' }))

    await waitFor(() => expect(checkCalls).toBe(1))
    // 成功提示 + 列表刷新(重新拉取账号)
    expect(await screen.findByText(/登录状态有效|状态正常/)).toBeInTheDocument()
  })

  it('检测失败(会话失效): 提示需重新登录', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: [account] })),
      http.post('/api/accounts/:id/check', () =>
        HttpResponse.json(
          { success: false, code: 'UPSTREAM_UNAUTHORIZED', message: 'iCloud 会话已失效，请重新登录或更新 Cookie' },
          { status: 401 },
        ),
      ),
    )
    renderPage()
    await screen.findByText('检测号')
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('button', { name: /更多操作/ })[0])
    await user.click(screen.getByRole('menuitem', { name: '检测状态' }))

    expect(await screen.findByText(/会话已失效/)).toBeInTheDocument()
  })

  it('无 Cookie 的账号: 检测按钮禁用或给出提示', async () => {
    const noCookie = { ...account, id: 'acc_nocookie', name: '无Cookie号', has_cookies: false, status: 'pending' as const }
    server.use(http.get('/api/accounts', () => HttpResponse.json({ success: true, data: [noCookie] })))
    renderPage()
    await screen.findByText('无Cookie号')
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('button', { name: /更多操作/ })[0])
    const item = screen.getByRole('menuitem', { name: '检测状态' })
    expect(item).toBeDisabled()
  })
})
