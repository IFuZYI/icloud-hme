import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '@testing-library/react'
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

// 时间范围使用两个独立的 antd DatePicker(带时间), 对齐参考项目筛选条:
// 一个「开始时间」一个「结束时间」, 各自可清空, 面板为中文(此刻/确定)。
describe('InboxPage 时间范围选择器', () => {
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

  it('渲染两个独立的日期时间选择器(开始时间/结束时间), 而非 RangePicker', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
    )
    renderPage()
    await screen.findByText('主题一')
    // 两个独立的 antd picker 容器; RangePicker 的 ant-picker-range 不应存在
    expect(document.querySelectorAll('.ant-picker').length).toBe(2)
    expect(document.querySelector('.ant-picker-range')).toBeNull()
    // 占位符为「开始时间」「结束时间」
    expect(screen.getAllByPlaceholderText(/开始时间/).length).toBeGreaterThan(0)
    expect(screen.getAllByPlaceholderText(/结束时间/).length).toBeGreaterThan(0)
  })

  it('默认近 7 天: 两个输入框带值, 查询携带 start/end 且不带 days', async () => {
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
      expect(url.searchParams.get('start')).toBeTruthy()
      expect(url.searchParams.get('end')).toBeTruthy()
      expect(url.searchParams.get('days')).toBeNull()
    })
    const inputs = document.querySelectorAll<HTMLInputElement>('.ant-picker-input input')
    expect(inputs.length).toBe(2)
    expect(inputs[0].value).not.toBe('')
    expect(inputs[1].value).not.toBe('')
  })

  it('清空开始时间后查询: 不带 start(只留 end)', async () => {
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
    // 清空第一个 picker(开始时间): 清除按钮未 hover 时 pointer-events:none,
    // 直接派发 click 事件绕过该限制。
    const clear = document.querySelectorAll('.ant-picker-clear')[0] as HTMLElement
    expect(clear).not.toBeNull()
    clear.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => {
      const url = new URL(lastUrl)
      expect(url.searchParams.get('start')).toBeNull()
      expect(url.searchParams.get('end')).toBeTruthy()
    })
  })
})

// URL 里的 start/end 必须在首次加载时水合回筛选状态——否则刷新/分享
// 带区间的链接后, URL 显示区间而实际按默认近 7 天查询, 两者静默不一致。
describe('InboxPage URL 日期区间水合', () => {
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

  it('从 URL start/end 初始化查询参数', async () => {
    let lastUrl = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        lastUrl = request.url
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage('/inbox?account_id=acc_1&start=2026-03-01T08:30&end=2026-03-05T18:45')
    await screen.findByText('主题一')
    await waitFor(() => {
      const url = new URL(lastUrl)
      // 请求统一以秒精度发出(YYYY-MM-DDTHH:mm:ss), 时间部分保持不变
      expect(url.searchParams.get('start')).toBe('2026-03-01T08:30:00')
      expect(url.searchParams.get('end')).toBe('2026-03-05T18:45:00')
    })
    // 两个输入框分别回填了对应时间
    const inputs = document.querySelectorAll<HTMLInputElement>('.ant-picker-input input')
    expect(inputs[0].value).toContain('2026-03-01')
    expect(inputs[1].value).toContain('2026-03-05')
  })
})
