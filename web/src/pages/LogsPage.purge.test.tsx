import { http, HttpResponse } from 'msw'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import LogsPage from './LogsPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'

// ====================================================================
// 日志页: 运行中清理任务进度卡 + 日志行「查看详情」打开任务详情 + 类别筛选
// ====================================================================
//
// 语义(用户确认): 已完成任务不显示卡片——其完整信息(状态/账号/清理范围/
// 已删除数/完成时间/运行日志)收在「清理任务详情」里, 由日志表格中该任务
// 日志行的「查看详情」打开; 运行中任务保留进度卡(进度条 + 暂停/继续/取消)。

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
  { id: 'l4', task_id: 'purge_2', time: '2026-10-16T09:05:00Z', level: 'info', message: '清理完成: 删除 1000 封', category: 'purge' },
  { id: 'l5', task_id: 'purge_gone', time: '2026-10-16T09:30:00Z', level: 'info', message: '清理完成: 删除 5 封', category: 'purge' },
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
    // 清理范围按截止日(range_end)表述: 删除该日及更早。
    expect(screen.getByText('2026-10-16 及更早的邮件')).toBeInTheDocument()
  })

  it('运行中任务卡片也有查看详情', async () => {
    renderPage()
    const bar = await screen.findByRole('progressbar')
    const card = bar.closest('.purge-task-card') as HTMLElement
    expect(within(card).getByRole('button', { name: '查看清理任务详情' })).toBeInTheDocument()
  })

  it('已完成任务不显示为卡片(卡片区只留运行中任务)', async () => {
    renderPage()
    await screen.findByRole('progressbar')
    // 完成态摘要(已删除 N 封 · 完成于 …)不得作为卡片出现;
    // 其完整信息由日志行的「查看详情」承载。
    expect(screen.queryByText(/已删除 1000 封/)).not.toBeInTheDocument()
    expect(screen.queryByText(/完成于/)).not.toBeInTheDocument()
    expect(document.querySelector('.purge-task-card.is-done')).toBeNull()
    // 只应渲染运行中任务这一张卡(done 任务不得渲染任何卡片)。
    expect(document.querySelectorAll('.purge-task-card')).toHaveLength(1)
  })

  it('清理任务日志行的「查看详情」打开「清理任务详情」: 任务信息 + 该任务日志', async () => {
    renderPage()
    await screen.findByText('清理完成: 删除 1000 封')
    const user = userEvent.setup()
    const table = screen.getByRole('table')
    const rows = within(table).getAllByRole('row')
    const row = rows.find((r) => within(r).queryByText('清理完成: 删除 1000 封'))
    if (!row) throw new Error('未找到清理日志行')
    await user.click(within(row).getByRole('button', { name: /查看/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理任务详情' })
    // 任务级信息(原卡片摘要内容: 状态/账号/范围/已删除数)。
    expect(within(dialog).getByText('已完成')).toBeInTheDocument()
    expect(within(dialog).getByText('主账号')).toBeInTheDocument()
    expect(within(dialog).getByText('2026-10-16 及更早的邮件')).toBeInTheDocument()
    expect(within(dialog).getByText(/已删除 1000 封/)).toBeInTheDocument()
    // 该任务的运行日志: 只含 purge_2 的条目, 不含其它任务的。
    expect(within(dialog).getByText('清理完成: 删除 1000 封')).toBeInTheDocument()
    expect(within(dialog).queryByText('清理任务开始')).not.toBeInTheDocument()
  })

  it('运行中任务的日志行同样打开「清理任务详情」', async () => {
    renderPage()
    await screen.findByText('清理任务开始')
    const user = userEvent.setup()
    const table = screen.getByRole('table')
    const rows = within(table).getAllByRole('row')
    const row = rows.find((r) => within(r).queryByText('清理任务开始'))
    if (!row) throw new Error('未找到清理日志行')
    await user.click(within(row).getByRole('button', { name: /查看/ }))
    const dialog = await screen.findByRole('dialog', { name: '清理任务详情' })
    expect(within(dialog).getByText('进行中')).toBeInTheDocument()
    expect(within(dialog).getByText('清理任务开始')).toBeInTheDocument()
  })

  it('任务已不可查时清理日志行回退「日志详情」(不含任务信息)', async () => {
    renderPage()
    await screen.findByText('清理完成: 删除 5 封')
    const user = userEvent.setup()
    const table = screen.getByRole('table')
    const rows = within(table).getAllByRole('row')
    const row = rows.find((r) => within(r).queryByText('清理完成: 删除 5 封'))
    if (!row) throw new Error('未找到清理日志行')
    await user.click(within(row).getByRole('button', { name: /查看/ }))
    const dialog = await screen.findByRole('dialog', { name: '日志详情' })
    expect(within(dialog).getByText('完整日志')).toBeInTheDocument()
    expect(within(dialog).getByText('清理完成: 删除 5 封')).toBeInTheDocument()
    expect(within(dialog).queryByText('清理范围')).not.toBeInTheDocument()
  })

  it('非清理日志的「查看详情」仍是「日志详情」, 不含任务区块', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    const user = userEvent.setup()
    const table = screen.getByRole('table')
    const rows = within(table).getAllByRole('row')
    const row = rows.find((r) => within(r).queryByText('自动任务日志'))
    if (!row) throw new Error('未找到自动任务日志行')
    await user.click(within(row).getByRole('button', { name: /查看/ }))
    const dialog = await screen.findByRole('dialog', { name: '日志详情' })
    expect(within(dialog).queryByText(/已删除/)).not.toBeInTheDocument()
  })

  it('日志表显示类别列', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    // 表格徽标(下拉选项里也有同名文本, 用表格范围限定)。
    const table = screen.getByRole('table')
    expect(within(table).getByText('自动任务')).toBeInTheDocument()
    expect(within(table).getByText('批量操作')).toBeInTheDocument()
    // 多条清理日志 → 「邮件清理」徽标出现多次。
    expect(within(table).getAllByText('邮件清理').length).toBeGreaterThanOrEqual(1)
  })

  // 窄屏(≤1023px)表格转为卡片布局: td 的标签来自 data-label。
  it('每个表格单元格都带 data-label(窄屏卡片布局依赖)', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    const cells = Array.from(document.querySelectorAll('tbody td'))
    expect(cells.length).toBeGreaterThan(0)
    expect(cells.filter((td) => !td.getAttribute('data-label')).map((td) => td.className)).toEqual([])
    const actions = cells.filter((td) => td.getAttribute('data-label') === '操作')
    expect(actions.length).toBeGreaterThan(0)
    actions.forEach((td) => expect(td.className).toContain('cell-actions'))
  })

  it('类别筛选: 选「邮件清理」只显示清理日志', async () => {
    renderPage()
    await screen.findByText('自动任务日志')
    const user = userEvent.setup()
    // 筛选器是 SelectMenu(胶囊触发 + listbox 弹层), 不再是原生 select:
    // 点触发按钮 → 点弹层里的「邮件清理」选项。
    const filter = screen.getByLabelText(/日志类别/)
    await user.click(filter)
    await user.click(await screen.findByRole('option', { name: '邮件清理' }))
    await waitFor(() => {
      expect(screen.queryByText('自动任务日志')).not.toBeInTheDocument()
    })
    expect(screen.getByText('清理任务开始')).toBeInTheDocument()
    // 触发按钮回显当前选中类别(替代原生 select 的选中态)。
    expect(screen.getByLabelText(/日志类别：邮件清理/)).toBeInTheDocument()
  })
})
