import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import InboxPage from './InboxPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { chooseOption } from '../test/selectMenu'
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
  alias: 'alpha@icloud.com',
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

describe('InboxPage', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    // 渐进摘要接口的默认桩: 回显请求的 id 对应的固定摘要
    server.use(
      http.post('/api/inbox/previews', async ({ request }) => {
        const body = (await request.json()) as { ids: string[] }
        const previews = Object.fromEntries(body.ids.map((id) => [id, `预览 ${id}`]))
        return HttpResponse.json({ success: true, data: previews })
      }),
    )
  })

  it('账号必选;alias 可空;limit/days 生效;query 经 URLSearchParams', async () => {
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
    // 确认 query 参数
    const url = new URL(lastUrl)
    expect(url.searchParams.get('account_id')).toBe('acc_1')
    expect(url.searchParams.get('limit')).toBe('20')
    expect(url.searchParams.get('days')).toBe('7')
    // 修改 limit/days 再查询
    const user = userEvent.setup()
    await chooseOption(user, /每页数量/, '50')
    await chooseOption(user, /时间范围/, '近 30 天')
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await waitFor(() => {
      const u = new URL(lastUrl)
      expect(u.searchParams.get('limit')).toBe('50')
      expect(u.searchParams.get('days')).toBe('30')
    })
  })

  it('从 URL 的 alias 参数初始化筛选,支持别名页直达收件箱', async () => {
    const inboxUrls: string[] = []
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/aliases', () =>
        HttpResponse.json({
          success: true,
          data: {
            account_id: 'acc_1',
            count: 1,
            aliases: [
              {
                email: 'alpha@icloud.com',
                anonymousId: 'anon_alpha',
                label: 'Alpha',
                active: true,
              },
            ],
          },
        }),
      ),
      http.get('/api/inbox', ({ request }) => {
        inboxUrls.push(request.url)
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage('/inbox?account_id=acc_1&alias=alpha%40icloud.com')
    await screen.findByText('主题一')
    await waitFor(() => {
      expect(inboxUrls.some((url) => new URL(url).searchParams.get('alias') === 'alpha@icloud.com')).toBe(true)
    })
    // SelectMenu: 触发按钮直接显示选中别名的文本
    expect(screen.getByRole('button', { name: /筛选别名/ }).textContent).toContain('alpha@icloud.com')
  })

  it('展示 method=imap 或 web_api', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: { ...inboxResult, method: 'web_api' },
        }),
      ),
    )
    renderPage()
    await screen.findByText('主题一')
    expect(screen.getByText(/Web API/)).toBeInTheDocument()
  })

  it('空列表、网络错误、401 状态', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: { account_id: 'acc_1', count: 0, total: 0, offset: 0, messages: [], method: 'imap' },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/暂无邮件/)).toBeInTheDocument()
  })

  it('恶意 HTML 只作为文本显示,不产生 img 节点', async () => {
    const evil = {
      ...inboxResult,
      messages: [
        {
          id: '2',
          from: 'evil@example.com',
          to: 'alpha@icloud.com',
          subject: '<img src=x onerror=alert(1)>',
          date: '2026-08-04T10:00:00+08:00',
          preview: '<script>alert(2)</script>预览',
        },
      ],
    }
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: evil })),
    )
    renderPage()
    await screen.findByText(/<img src=x onerror=alert\(1\)>/)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('script')).toBeNull()
  })

  it('快速切换筛选:第一请求晚返回不覆盖第二请求', async () => {
    let release: (() => void) | undefined
    let calls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        calls++
        if (calls === 1) {
          // 第一次请求挂起,稍后返回旧数据
          return new Promise<Response>((resolve) => {
            release = () =>
              resolve(
                HttpResponse.json({
                  success: true,
                  data: {
                    ...inboxResult,
                    messages: [{ ...inboxResult.messages[0], subject: '旧主题' }],
                  },
                }),
              )
          })
        }
        return HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            alias: 'second',
            messages: [
              {
                id: '9',
                from: 's2@example.com',
                to: 'alpha@icloud.com',
                subject: '第二请求主题',
                date: '2026-08-04T11:00:00+08:00',
                preview: '第二请求',
              },
            ],
          },
        })
      }),
    )
    renderPage()
    await screen.findByText(/加载中/)
    // 触发第二次查询(首次挂起中)
    const user = userEvent.setup()
    await chooseOption(user, /每页数量/, '50')
    await user.click(screen.getByRole('button', { name: /查询/ }))
    await screen.findByText('第二请求主题')
    // 第一次请求此时才返回
    release?.()
    // 旧数据不得覆盖新数据
    await new Promise((r) => setTimeout(r, 100))
    expect(screen.getByText('第二请求主题')).toBeInTheDocument()
    expect(screen.queryByText('旧主题')).toBeNull()
  })

  it('空 subject 显示(无主题)', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [{ ...inboxResult.messages[0], subject: '' }],
          },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/（无主题）/)).toBeInTheDocument()
  })

  it('空摘要显示占位符', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [{ ...inboxResult.messages[0], preview: '' }],
          },
        }),
      ),
    )
    renderPage()
    // 空摘要不再显示"—", 而是渐进加载: 骨架条出现后由 previews 接口补齐
    const previewText = await screen.findByText('预览 1')
    expect(previewText).toBeInTheDocument()
  })

  it('IMAP 不可用降级到 Web API 时展示提示,不把降级当成功', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            method: 'web_api',
            warning: 'IMAP 不可用，已回退 Web API：IMAP 登录失败',
          },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/IMAP 不可用，已回退 Web API/)).toBeInTheDocument()
    // 摘要行的时间范围描述(下拉选项里也有"近 7 天", 限定在摘要行内断言)
    const summary = document.querySelector('.inbox-summary')
    expect(summary?.textContent).toContain('近 7 天')
  })

  it('列表展示收件人别名与相对日期', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [
              { ...inboxResult.messages[0], date: new Date(Date.now() - 3600_000).toISOString() },
            ],
          },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText('主题一')).toBeInTheDocument()
    expect(screen.getByText('alpha@icloud.com')).toBeInTheDocument()
    expect(screen.getByText(/^今天 /)).toBeInTheDocument()
  })

  it('从列表删除邮件:确认后带 CSRF 发送 DELETE 并刷新', async () => {
    const deletes: { url: string; token: string | null }[] = []
    let inboxCalls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        inboxCalls++
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
      http.delete('/api/inbox/:id', ({ request }) => {
        deletes.push({ url: request.url, token: request.headers.get('x-csrf-token') })
        return HttpResponse.json({ success: true, data: { id: '1' } })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /删除邮件：主题一/ }))
    await user.click(await screen.findByRole('button', { name: '确认删除' }))
    await waitFor(() => expect(deletes).toHaveLength(1))
    expect(new URL(deletes[0].url).searchParams.get('account_id')).toBe('acc_1')
    expect(new URL(deletes[0].url).pathname).toBe('/api/inbox/1')
    expect(deletes[0].token).toBe('csrf-test')
    await waitFor(() => expect(inboxCalls).toBeGreaterThan(1))
  })

  it('点击邮件打开详情:展示元信息与正文,并可复制正文', async () => {
    // 先 setup:userEvent 会安装自己的剪贴板桩,必须在它之后再覆盖
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.get('/api/inbox/:id', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult.messages[0],
            body: '验证码：123456',
            content_type: 'text/plain',
          },
        }),
      ),
    )
    renderPage()
    await screen.findByText('主题一')
    await user.click(screen.getByRole('button', { name: '查看邮件：主题一' }))
    expect(await screen.findByText('验证码：123456')).toBeInTheDocument()
    expect(screen.getByText('发件人')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /复制正文/ }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('验证码：123456'))
  })
  it('空收件箱仍展示摘要行(共 0 封 + 读取方式)', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: { account_id: 'acc_1', count: 0, total: 0, offset: 0, messages: [], method: 'imap' },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/暂无邮件/)).toBeInTheDocument()
    // 摘要行不在 AsyncState 内, 空结果时也要可见
    // 等结果真正渲染后再断言(避免停在 accounts 已返回、收件箱未返回的中间态)
    await waitFor(() => expect(document.querySelector('.inbox-summary')).not.toBeNull())
    const summary = document.querySelector('.inbox-summary')
    expect(summary?.textContent).toContain('共')
    expect(summary?.textContent).toContain('0')
    expect(summary?.textContent).toContain('读取方式：IMAP')
  })

  it('点击刷新时按钮进入忙碌态并禁用, 完成后恢复', async () => {
    let release: (() => void) | undefined
    let calls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        calls++
        if (calls === 1) return HttpResponse.json({ success: true, data: inboxResult })
        return new Promise<Response>((resolve) => {
          release = () => resolve(HttpResponse.json({ success: true, data: inboxResult }))
        })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    const refresh = screen.getByRole('button', { name: '刷新收件箱' })
    expect(refresh.className).toBe('inbox-action')

    await user.click(refresh)
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新收件箱' })).toBeDisabled())
    expect(screen.getByRole('button', { name: '刷新收件箱' }).className).toContain('is-busy')

    release?.()
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新收件箱' })).not.toBeDisabled())
  })
})
