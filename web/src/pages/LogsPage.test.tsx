import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../test/server'
import LogsPage from './LogsPage'

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

describe('LogsPage', () => {
  it('失败日志可打开详情并突出显示失败原因', async () => {
    server.use(
      http.get('/api/alias-task-logs', () =>
        HttpResponse.json({ success: true, data: logs }),
      ),
    )
    const user = userEvent.setup()
    render(<LogsPage />)

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
    render(<LogsPage />)

    await screen.findByText('创建任务，等待 10 秒后开始')
    await user.click(screen.getByRole('button', { name: '查看普通日志详情' }))

    expect(screen.getByRole('dialog', { name: '日志详情' })).toHaveTextContent(
      '创建任务，等待 10 秒后开始',
    )
  })
})
