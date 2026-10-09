import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../test/server'
import LogsPage from './LogsPage'
import { ToastProvider } from '../components/ToastProvider'

const logs = [
  {
    id: 'log_error',
    task_id: 'task_1',
    time: '2026-09-12T06:00:00Z',
    level: 'error',
    message: '自动邮箱001 创建失败：认证已过期，请重新登录 iCloud',
  },
  {
    id: 'log_info',
    task_id: 'task_1',
    time: '2026-09-12T05:59:00Z',
    level: 'info',
    message: '创建任务，等待 10 秒后开始',
  },
]

function renderPage() {
  return render(
    <ToastProvider>
      <LogsPage />
    </ToastProvider>,
  )
}

describe('LogsPage', () => {
  it('失败日志可打开详情并突出显示失败原因', async () => {
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
    )
    const user = userEvent.setup()
    renderPage()

    await screen.findByText('自动邮箱001 创建失败：认证已过期，请重新登录 iCloud')
    await user.click(screen.getByRole('button', { name: '查看失败日志详情' }))

    const dialog = screen.getByRole('dialog', { name: '失败日志详情' })
    expect(dialog).toHaveTextContent('失败原因')
    expect(dialog).toHaveTextContent('认证已过期，请重新登录 iCloud')
    expect(dialog).toHaveTextContent('task_1')
    expect(dialog).toHaveTextContent('自动邮箱001 创建失败：认证已过期，请重新登录 iCloud')
  })

  it('普通日志也可查看完整详情', async () => {
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
    )
    const user = userEvent.setup()
    renderPage()

    await screen.findByText('创建任务，等待 10 秒后开始')
    await user.click(screen.getByRole('button', { name: '查看普通日志详情' }))

    expect(screen.getByRole('dialog', { name: '日志详情' })).toHaveTextContent(
      '创建任务，等待 10 秒后开始',
    )
  })

  it('日志时间用项目统一的中文格式, 不用浏览器默认的 toLocaleString', async () => {
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
    )
    renderPage()
    await screen.findByText('自动邮箱001 创建失败：认证已过期，请重新登录 iCloud')
    // 统一格式 "2026/09/12 06:00"(zh-CN, 24 小时制); 不能出现美式 "9/12/2026, 6:00:00 AM"
    expect(screen.getAllByText(/2026\/09\/12 06:00/).length).toBeGreaterThan(0)
    expect(screen.queryByText(/AM|PM/)).toBeNull()
  })

  it('清理日志: 打开对话框, 选择「1 周前」确认后发送 older_than_days=7 并刷新列表', async () => {
    let listCalls = 0
    const cleanupBodies: Array<Record<string, unknown>> = []
    server.use(
      http.get('/api/alias-task-logs', () => {
        listCalls += 1
        return HttpResponse.json({ success: true, data: listCalls === 1 ? logs : [logs[1]] })
      }),
      http.post('/api/alias-task-logs/cleanup', async ({ request }) => {
        cleanupBodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ success: true, data: { deleted: 1, remaining: 1 } })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('创建任务，等待 10 秒后开始')

    await user.click(screen.getByRole('button', { name: /清理日志/ }))
    const dialog = screen.getByRole('dialog', { name: '清理日志' })
    // 三档时间选项
    expect(within(dialog).getByRole('radio', { name: /1 天前/ })).toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: /1 周前/ })).toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: /1 个月前/ })).toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: /1 周前/ }))
    await user.click(screen.getByRole('button', { name: '确认清理' }))

    await waitFor(() => expect(cleanupBodies).toHaveLength(1))
    expect(cleanupBodies[0]).toEqual({ older_than_days: 7 })
    // 成功后关闭对话框、刷新列表、提示删除数量
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(listCalls).toBeGreaterThan(1))
    expect(await screen.findByRole('status')).toHaveTextContent('已清理 1 条日志')
  })

  it('清理日志: 选择「1 个月前」发送 older_than_days=30', async () => {
    const cleanupBodies: Array<Record<string, unknown>> = []
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
      http.post('/api/alias-task-logs/cleanup', async ({ request }) => {
        cleanupBodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ success: true, data: { deleted: 0, remaining: 2 } })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('创建任务，等待 10 秒后开始')

    await user.click(screen.getByRole('button', { name: /清理日志/ }))
    await user.click(screen.getByRole('radio', { name: /1 个月前/ }))
    await user.click(screen.getByRole('button', { name: '确认清理' }))

    await waitFor(() => expect(cleanupBodies).toHaveLength(1))
    expect(cleanupBodies[0]).toEqual({ older_than_days: 30 })
  })

  it('清理日志: 取消不发请求', async () => {
    let cleanupCalls = 0
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
      http.post('/api/alias-task-logs/cleanup', () => {
        cleanupCalls += 1
        return HttpResponse.json({ success: true, data: { deleted: 1, remaining: 1 } })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('创建任务，等待 10 秒后开始')

    await user.click(screen.getByRole('button', { name: /清理日志/ }))
    await user.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(cleanupCalls).toBe(0)
  })

  it('清理日志失败时显示错误并保留对话框', async () => {
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
      http.post('/api/alias-task-logs/cleanup', () =>
        HttpResponse.json(
          { success: false, code: 'INTERNAL_ERROR', message: '日志清理失败: disk full' },
          { status: 500 },
        ),
      ),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('创建任务，等待 10 秒后开始')

    await user.click(screen.getByRole('button', { name: /清理日志/ }))
    await user.click(screen.getByRole('button', { name: '确认清理' }))

    const dialog = await screen.findByRole('dialog', { name: '清理日志' })
    await waitFor(() => expect(dialog).toHaveTextContent('日志清理失败'))
  })
})
