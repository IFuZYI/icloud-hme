import { http, HttpResponse } from 'msw'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import AliasTasksPage from './AliasTasksPage'
import { server } from '../test/server'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary, AliasTask } from '../api/types'

// 单账号场景: 表单应自动选中该账号; 编辑任务应回填任务字段。
// 背景: AliasTaskForm 随页面常驻挂载(accounts 尚为空数组), useState 初始化器
// 只跑一次——若打开时表单不重建, 编辑不回填且保存会用默认值覆盖任务配置。
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

const task: AliasTask = {
  id: 'task_1',
  enabled: true,
  account_id: 'acc_1',
  mode: 'auto',
  interval_minutes: 60,
  batch_count: 1,
  daily_limit: 7,
  label_mode: 'library',
  max_total: 33,
  created_count: 0,
  daily_count: 0,
  last_success: 0,
}

function stub() {
  server.use(
    http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    http.get('/api/alias-tasks', () => HttpResponse.json({ success: true, data: [task] })),
  )
}

function renderPage() {
  return render(
    <ToastProvider>
      <AliasTasksPage />
    </ToastProvider>,
  )
}

describe('AliasTasksPage 表单挂载时机', () => {
  it('单账号时新建任务自动选中该账号', async () => {
    stub()
    renderPage()
    // 任务卡的账号名从 id 变为「主号」= 账号列表已加载
    await screen.findByText('主号')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /新建任务/ }))
    expect(screen.getByRole('button', { name: '选择账号' }).textContent).toContain('主号')
    expect(screen.getByRole('button', { name: '保存' })).not.toBeDisabled()
  })

  it('编辑任务时回填该任务的字段', async () => {
    stub()
    renderPage()
    await screen.findByText('主号')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /更多/ }))
    await user.click(await screen.findByRole('menuitem', { name: '编辑' }))
    expect(screen.getByLabelText('每天创建数量')).toHaveValue(7)
    expect(screen.getByLabelText('目标邮箱数量')).toHaveValue(33)
    expect(screen.getByRole('button', { name: '选择账号' }).textContent).toContain('主号')
  })

  it('取消后重新打开表单为全新状态', async () => {
    stub()
    renderPage()
    await screen.findByText('主号')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /新建任务/ }))
    const target = screen.getByLabelText('目标邮箱数量')
    await user.clear(target)
    await user.type(target, '12')
    await user.click(screen.getByRole('button', { name: '取消' }))
    await user.click(screen.getByRole('button', { name: /新建任务/ }))
    expect(screen.getByLabelText('目标邮箱数量')).toHaveValue(20)
  })
})
