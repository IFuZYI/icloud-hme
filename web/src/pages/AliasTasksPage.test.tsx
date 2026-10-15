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

/** 读取当前渲染的任务卡标题序列(卡片顺序 = DOM 顺序)。 */
function cardTitles(): string[] {
  return Array.from(document.querySelectorAll('.task-card h3')).map((el) => el.textContent ?? '')
}

/** 构造定时任务的完整响应 DTO。 */
function scheduledTask(id: string, labelPrefix: string, intervalMinutes = 60): AliasTask {
  return {
    id,
    enabled: true,
    account_id: 'acc_1',
    mode: 'scheduled',
    interval_minutes: intervalMinutes,
    batch_count: 1,
    daily_limit: 50,
    label_mode: 'sequential',
    label_prefix: labelPrefix,
    max_total: 50,
    created_count: 0,
    daily_count: 0,
    last_success: 0,
  }
}

describe('AliasTasksPage 禁止闪现换位', () => {
  it('轮询返回乱序时, 已展示的卡片保持位置不变', async () => {
    const first = [scheduledTask('task_a', '甲'), scheduledTask('task_b', '乙'), scheduledTask('task_c', '丙')]
    // 第二次轮询: 服务端乱序(模拟 map 迭代顺序波动) + 状态字段变化。
    const second = [
      { ...scheduledTask('task_c', '丙'), created_count: 3, daily_count: 3 },
      { ...scheduledTask('task_a', '甲'), created_count: 5, daily_count: 5 },
      { ...scheduledTask('task_b', '乙'), created_count: 1, daily_count: 1 },
    ]
    let call = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/alias-tasks', () => {
        call += 1
        return HttpResponse.json({ success: true, data: call === 1 ? first : second })
      }),
    )
    renderPage()
    // 首次加载: 按服务端顺序展示。
    expect(await screen.findByText('甲001 顺序')).toBeInTheDocument()
    expect(cardTitles()).toEqual(['甲001 顺序', '乙001 顺序', '丙001 顺序'])

    // 等待轮询(2 秒)应用第二批乱序数据。
    await screen.findByText('3 / 50（6%）', undefined, { timeout: 6000 })
    // 乱序数据到达后, 卡片位置必须保持首次顺序。
    expect(cardTitles()).toEqual(['甲001 顺序', '乙001 顺序', '丙001 顺序'])
  }, 10000)

  it('轮询出现新任务时追加到末尾, 已有卡片不动', async () => {
    const first = [scheduledTask('task_a', '甲'), scheduledTask('task_b', '乙')]
    const second = [
      scheduledTask('task_new', '新'),
      scheduledTask('task_b', '乙'),
      scheduledTask('task_a', '甲'),
    ]
    let call = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/alias-tasks', () => {
        call += 1
        return HttpResponse.json({ success: true, data: call === 1 ? first : second })
      }),
    )
    renderPage()
    expect(await screen.findByText('甲001 顺序')).toBeInTheDocument()
    expect(cardTitles()).toEqual(['甲001 顺序', '乙001 顺序'])

    await screen.findByText('新001 顺序', undefined, { timeout: 6000 })
    expect(cardTitles()).toEqual(['甲001 顺序', '乙001 顺序', '新001 顺序'])
  }, 10000)
})

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
