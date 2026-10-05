import { http, HttpResponse } from 'msw'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import InboxPage from './InboxPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary, InboxResult } from '../api/types'

const accounts: AccountSummary[] = [
  {
    id: 'acc_1',
    name: '主号',
    real_email: 'a@example.com',
    icloud_email: 'a@icloud.com',
    host: 'icloud.com',
    status: 'active',
    alias_total: 2,
    alias_active: 2,
    has_cookies: true,
    has_app_password: true,
    has_proxy: false,
    last_validated: '2026-08-04T09:00:00+08:00',
    created_at: '2026-08-01T09:00:00+08:00',
  },
]

const inboxResult: InboxResult = {
  account_id: 'acc_1',
  count: 1,
  total: 1,
  offset: 0,
  method: 'imap',
  messages: [
    {
      id: '1',
      from: 'sender@example.com',
      to: 'alpha@icloud.com',
      subject: '主题一',
      date: '2026-08-04T10:00:00+08:00',
      preview: '预览内容',
    },
  ],
}

function renderPage(initialPath = '/inbox') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route
          path="/inbox"
          element={
            <ToastProvider>
              <InboxPage />
            </ToastProvider>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

describe('InboxPage 日期范围选择器', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    server.use(
      http.post('/api/inbox/previews', async ({ request }) => {
        const body = (await request.json()) as { ids: string[] }
        const previews = Object.fromEntries(body.ids.map((id) => [id, `预览 ${id}`]))
        return HttpResponse.json({ success: true, data: previews })
      }),
    )
  })

  it('时间范围使用日期范围选择器而非固定天数下拉', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
    )
    renderPage()
    await screen.findByText('主题一')
    // 旧的"近 N 天"下拉应已移除
    expect(screen.queryByRole('button', { name: '时间范围' })).not.toBeInTheDocument()
    // 日期范围选择器存在(antd RangePicker 渲染为两个日期输入)
    expect(screen.getByText('时间范围')).toBeInTheDocument()
    expect(screen.getAllByPlaceholderText(/开始日期|结束日期/).length).toBeGreaterThan(0)
  })

  it('默认范围查询携带 start/end 参数', async () => {
    let lastUrl = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        lastUrl = request.url
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    await waitFor(() => {
      const url = new URL(lastUrl)
      expect(url.searchParams.get('account_id')).toBe('acc_1')
      // 默认近 7 天: 起止日期均存在,且不再使用 days
      expect(url.searchParams.get('start')).toBeTruthy()
      expect(url.searchParams.get('end')).toBeTruthy()
      expect(url.searchParams.get('days')).toBeNull()
    })
  })

  it('清空日期范围后按全部时间查询(不带 start/end)', async () => {
    let lastUrl = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        lastUrl = request.url
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    // 清空: antd 的清除按钮在未 hover 时 pointer-events:none,用 fireEvent 绕过。
    const clear = document.querySelector('.ant-picker-clear') as HTMLElement | null
    expect(clear).not.toBeNull()
    fireEvent.click(clear as HTMLElement)
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => {
      const url = new URL(lastUrl)
      expect(url.searchParams.get('start')).toBeNull()
      expect(url.searchParams.get('end')).toBeNull()
    })
  })
})
