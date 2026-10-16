import { http, HttpResponse } from 'msw'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import LogsPage from './LogsPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'

// ====================================================================
// 日志页: 清理任务进度卡 + 类别筛选
// ====================================================================

const runningTask = {
  id: 'purge_1',
  account_id: 'acc_1',
  account_name: '主账号',
  range_start: '2026-01-01T00:00:00Z',
  range_end: '2026-10-16T00:00:00Z',
  status: 'running',
  total: 1000,
  processed: 300,
  deleted: 300,
  failed: 0,
  current_message: 'INBOX: 删除 500 封',
  started_at: '2026-10-16T09:00:00Z',
}

const doneTask = {
  ...runningTask,
  id: 'purge_2',
  status: 'done',
  processed: 1000,
  deleted: 1000,
  finished_at: '2026-10-16T09:05:00Z',
}

const logs = [
  { id: 'l1', task_id: 'auto_1', time: '2026-10-16T08:00:00Z', level: 'info', message: '自动任务日志', category: 'auto_task' },
  { id: 'l2', task_id: 'batch_alias_delete', time: '2026-10-16T08:10:00Z', level: 'info', message: '批量删除完成：成功 2，失败 0', category: 'batch_alias' },
  { id: 'l3', task_id: 'purge_1', time: '2026-10-16T08:20:00Z', level: 'info', message: '清理任务开始', category: 'purge' },
]

function renderPage() {
  return render(
    <ToastProvider>
      <LogsPage />
    </ToastProvider>,
  )
}

describe('LogsPage 清理任务与类别', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    server.use(
      http.get('/api/alias-task-logs', () => HttpResponse.json({ success: true, data: logs })),
      http.get('/api/purge-tasks', () => HttpResponse.json({ success: true, data: [runningTask, doneTask] })),
    )
  })

  it('显示运行中清理任务: 进度条与当前处理项', async () => {
    renderPage()
    // 等待任务卡渲染(进度条出现)。
    const bar = await screen.findByRole('progressbar')
    expect(bar).toBeInTheDocument()
    // 进度: 300/1000
    expect(screen.getByText(/300\s*\/\s*1000/)).toBeInTheDocument()
    expect(screen.getByText(/INBOX: 删除 500 封/)).toBeInTheDocument()
    expect(screen.getAllByText('主账号').length).toBeGreaterThan(0)
  })

  it('完成的任务显示摘要', async () => {
    renderPage()
    await screen.findByRole('progressbar')
    // done 任务显示「已删除 1000 封」摘要。
    expect(screen.getByText(/已删除 1000 封/)).toBeInTheDocument()
  })

  it('日志表显示类别列', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    // 表格徽标(下拉选项里也有同名文本, 用表格范围限定)。
    const table = screen.getByRole('table')
    expect(within(table).getByText('自动任务')).toBeInTheDocument()
    expect(within(table).getByText('批量操作')).toBeInTheDocument()
    expect(within(table).getByText('邮件清理')).toBeInTheDocument()
  })

  it('类别筛选: 选「邮件清理」只显示清理日志', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    const user = userEvent.setup()
    // 找到筛选下拉并选择「邮件清理」。
    const filter = screen.getByLabelText(/日志类别/)
    await user.selectOptions(filter, 'purge')
    await waitFor(() => {
      expect(screen.queryByText('自动任务日志')).not.toBeInTheDocument()
    })
    expect(screen.getByText('清理任务开始')).toBeInTheDocument()
  })
})
