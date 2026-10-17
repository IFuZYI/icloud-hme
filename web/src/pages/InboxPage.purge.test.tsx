import { http, HttpResponse } from 'msw'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import dayjs from 'dayjs'
import InboxPage from './InboxPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { __resetCache } from '../api/cache'
import { ToastProvider } from '../components/ToastProvider'
import { MemoryRouter } from 'react-router-dom'

// ====================================================================
// 收件箱: 批量清理对话框(预览 + 二次确认)
// ====================================================================
//
// 语义: 预设(3天/7天/1月/半年)是「截止日」——删除截止日及更早的旧邮件,
// 保留较新的邮件。请求区间固定为 [哨兵起点 2000-01-01, N 天前当天 00:00]。

const accounts = [{ id: 'acc_1', name: '主账号', real_email: 'a@x.com', status: 'active' }]
const inboxResult = {
  account_id: 'acc_1',
  alias: '',
  limit: 20,
  offset: 0,
  total: 3,
  method: 'imap',
  messages: [
    { id: '10', folder: 'INBOX', uid: 10, from: 'a@x.com', to: 'me@x.com', subject: '主题一', date: '2026-10-01T00:00:00Z', preview: 'p1', has_attachments: false },
    { id: '11', folder: 'INBOX', uid: 11, from: 'b@x.com', to: 'me@x.com', subject: '主题二', date: '2026-10-02T00:00:00Z', preview: 'p2', has_attachments: false },
    { id: '12', folder: 'INBOX', uid: 12, from: 'c@x.com', to: 'me@x.com', subject: '主题三', date: '2026-10-03T00:00:00Z', preview: 'p3', has_attachments: false },
  ],
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/inbox?account_id=acc_1']}>
      <ToastProvider>
        <InboxPage />
      </ToastProvider>
    </MemoryRouter>,
  )
}

describe('InboxPage 批量清理', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    __resetCache()
    server.resetHandlers()
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.post('/api/inbox/previews', () => HttpResponse.json({ success: true, data: { previews: [] } })),
    )
  })

  it('页头有「清理邮件」按钮, 打开对话框显示时间范围预设', async () => {
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    // 预设: 3天/7天/1月/半年
    expect(within(dialog).getByRole('button', { name: '3 天前' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '7 天前' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '1 个月前' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '半年前' })).toBeInTheDocument()
  })

  it('预设「3 天前」→ 请求区间 [哨兵起点, 3 天前当天](删除截止日及更早)', async () => {
    let previewBody: Record<string, unknown> | null = null
    server.use(
      http.post('/api/inbox/purge/preview', async ({ request }) => {
        previewBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({
          success: true,
          data: {
            total: 5,
            folders: [{ folder: 'INBOX', count: 5 }],
            estimated_seconds: 1,
            range_start: '2000-01-01T00:00:00Z',
            range_end: dayjs().subtract(3, 'day').startOf('day').format('YYYY-MM-DDTHH:mm:ssZ'),
          },
        })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    await user.click(within(dialog).getByRole('button', { name: '3 天前' }))
    await waitFor(() => expect(previewBody).not.toBeNull())
    // 起点固定为哨兵(早于任何邮件, 覆盖全部历史)。
    expect(previewBody!.range_start).toBe('2000-01-01T00:00:00Z')
    // 终点 = 「3 天前」当天 00:00(后端按日粒度删除该日及更早)。
    expect(String(previewBody!.range_end).slice(0, 10)).toBe(dayjs().subtract(3, 'day').format('YYYY-MM-DD'))
  })

  it('预设「7 天前」→ 请求区间映射到 7 天前的截止日', async () => {
    let previewBody: Record<string, unknown> | null = null
    server.use(
      http.post('/api/inbox/purge/preview', async ({ request }) => {
        previewBody = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({
          success: true,
          data: {
            total: 5,
            folders: [{ folder: 'INBOX', count: 5 }],
            estimated_seconds: 1,
            range_start: '2000-01-01T00:00:00Z',
            range_end: dayjs().subtract(7, 'day').startOf('day').format('YYYY-MM-DDTHH:mm:ssZ'),
          },
        })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    await user.click(within(dialog).getByRole('button', { name: '7 天前' }))
    await waitFor(() => expect(previewBody).not.toBeNull())
    expect(previewBody!.range_start).toBe('2000-01-01T00:00:00Z')
    expect(String(previewBody!.range_end).slice(0, 10)).toBe(dayjs().subtract(7, 'day').format('YYYY-MM-DD'))
  })

  it('点预设 → 显示统计(截止日 + 总数/文件夹明细)', async () => {
    server.use(
      http.post('/api/inbox/purge/preview', () =>
        HttpResponse.json({
          success: true,
          data: {
            total: 42,
            folders: [{ folder: 'INBOX', count: 42 }],
            estimated_seconds: 6,
            range_start: '2000-01-01T00:00:00Z',
            range_end: '2026-10-16T00:00:00Z',
          },
        }),
      ),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    // 点预设直接触发预览(少一次点击)。
    await user.click(within(dialog).getByRole('button', { name: '7 天前' }))
    // 预览文案: 删除截止日及更早的邮件。
    expect(await within(dialog).findByText(/将删除 2026-10-16 及更早的邮件，共/)).toBeInTheDocument()
    // 总数与文件夹明细都在。
    expect(within(dialog).getByText('42')).toBeInTheDocument()
    expect(within(dialog).getByText(/INBOX：42 封/)).toBeInTheDocument()
  })

  it('二次确认文案包含截止日与「及更早的邮件」语义', async () => {
    server.use(
      http.post('/api/inbox/purge/preview', () =>
        HttpResponse.json({
          success: true,
          data: {
            total: 10,
            folders: [{ folder: 'INBOX', count: 10 }],
            estimated_seconds: 3,
            range_start: '2000-01-01T00:00:00Z',
            range_end: '2026-10-16T00:00:00Z',
          },
        }),
      ),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    await user.click(within(dialog).getByRole('button', { name: '7 天前' }))
    await within(dialog).findByText(/及更早的邮件/)
    await user.click(within(dialog).getByRole('button', { name: /确认清理/ }))
    const confirm = await screen.findByRole('dialog', { name: '确认清理' })
    expect(within(confirm).getByText(/将永久删除 2026-10-16 及更早的 10 封邮件，操作不可恢复。确认继续？/)).toBeInTheDocument()
  })

  it('确认清理 → 二次确认 → 创建任务并提示', async () => {
    let created: Record<string, unknown> | null = null
    server.use(
      http.post('/api/inbox/purge/preview', () =>
        HttpResponse.json({
          success: true,
          data: {
            total: 10,
            folders: [{ folder: 'INBOX', count: 10 }],
            estimated_seconds: 3,
            range_start: '2000-01-01T00:00:00Z',
            range_end: '2026-10-16T00:00:00Z',
          },
        }),
      ),
      http.post('/api/inbox/purge', async ({ request }) => {
        created = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ success: true, data: { id: 'purge_x', status: 'running' } })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /清理邮件/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理邮件' })
    await user.click(within(dialog).getByRole('button', { name: '7 天前' }))
    await within(dialog).findByText(/及更早的邮件/)
    // 确认清理 → 弹二次确认。
    await user.click(within(dialog).getByRole('button', { name: /确认清理/ }))
    const confirm = await screen.findByRole('dialog', { name: '确认清理' })
    expect(within(confirm).getByText(/不可恢复/)).toBeInTheDocument()
    // 二次确认通过 → 创建。
    await user.click(within(confirm).getByRole('button', { name: /确认删除/ }))
    await waitFor(() => expect(created).not.toBeNull())
    expect(created!.account_id).toBe('acc_1')
    // 创建任务沿用预览区间(哨兵起点 + 截止日)。
    expect(created!.range_start).toBe('2000-01-01T00:00:00Z')
    expect(created!.range_end).toBe('2026-10-16T00:00:00Z')
  })
})
