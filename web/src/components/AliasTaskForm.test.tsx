import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import AliasTaskForm from './AliasTaskForm'
import { server } from '../test/server'

const accounts = [{ id: 'acc_1', name: '主账号' }]

function stubCreate(bodyRef: { current?: Record<string, unknown> }) {
  server.use(
    http.post('/api/alias-tasks', async ({ request }) => {
      bodyRef.current = (await request.json()) as Record<string, unknown>
      return HttpResponse.json({ success: true, data: {} }, { status: 201 })
    }),
  )
}

describe('AliasTaskForm', () => {
  it('父组件以等价账号数据重渲染时保留用户输入', async () => {
    const props = {
      open: true,
      accounts,
      onClose: vi.fn(),
      onSaved: vi.fn(),
    }
    const { rerender } = render(<AliasTaskForm {...props} />)
    const target = screen.getByRole('spinbutton', { name: '目标邮箱数量' })
    const user = userEvent.setup()

    await user.clear(target)
    await user.type(target, '12')
    rerender(<AliasTaskForm {...props} accounts={[...accounts]} />)

    expect(target).toHaveValue(12)
  })

  it('每日数量为自由输入的数字框,默认 20', async () => {
    render(<AliasTaskForm open accounts={accounts} onClose={vi.fn()} onSaved={vi.fn()} />)
    const input = screen.getByLabelText('每天创建数量')
    expect(input.tagName).toBe('INPUT')
    expect(input).toHaveAttribute('type', 'number')
    expect(input).toHaveValue(20)
    // 不再是下拉选择器
    expect(screen.queryByRole('button', { name: '每天创建数量' })).not.toBeInTheDocument()
  })

  it('默认提交自主任务：每日数量 + 名称库标签', async () => {
    const bodyRef: { current?: Record<string, unknown> } = {}
    stubCreate(bodyRef)
    const onClose = vi.fn()
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={onClose} onSaved={onSaved} />)
    const user = userEvent.setup()

    expect(screen.queryByText('标签前缀')).not.toBeInTheDocument()
    await user.clear(screen.getByLabelText('目标邮箱数量'))
    await user.type(screen.getByLabelText('目标邮箱数量'), '12')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(bodyRef.current).toMatchObject({
      account_id: 'acc_1', mode: 'auto', target_count: 12, daily_limit: 20, label_mode: 'library',
    })
    expect(bodyRef.current).not.toHaveProperty('interval_minutes')
    expect(bodyRef.current).not.toHaveProperty('label_prefix')
  })

  it('每日数量可输入任意 1-50 的整数', async () => {
    const bodyRef: { current?: Record<string, unknown> } = {}
    stubCreate(bodyRef)
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={vi.fn()} onSaved={onSaved} />)
    const user = userEvent.setup()

    const input = screen.getByLabelText('每天创建数量')
    await user.clear(input)
    await user.type(input, '7')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(bodyRef.current).toMatchObject({ mode: 'auto', daily_limit: 7 })
  })

  it('超出 1-50 时提示且不提交', async () => {
    const bodyRef: { current?: Record<string, unknown> } = {}
    stubCreate(bodyRef)
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={vi.fn()} onSaved={onSaved} />)
    const user = userEvent.setup()

    const input = screen.getByLabelText('每天创建数量')
    await user.clear(input)
    await user.type(input, '99')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('每天创建数量需在 1-50 之间')
    expect(onSaved).not.toHaveBeenCalled()
    expect(bodyRef.current).toBeUndefined()
  })

  it('切换到定时任务后提交间隔与每次数量', async () => {
    const bodyRef: { current?: Record<string, unknown> } = {}
    stubCreate(bodyRef)
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={vi.fn()} onSaved={onSaved} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('radio', { name: /定时任务/ }))
    await user.clear(screen.getByLabelText('每次创建数量'))
    await user.type(screen.getByLabelText('每次创建数量'), '3')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(bodyRef.current).toMatchObject({
      account_id: 'acc_1', mode: 'scheduled', interval_minutes: 60, batch_count: 3, label_mode: 'library',
    })
    expect(bodyRef.current).not.toHaveProperty('daily_limit')
  })

  it('顺序标签模式提交前缀', async () => {
    const bodyRef: { current?: Record<string, unknown> } = {}
    stubCreate(bodyRef)
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={vi.fn()} onSaved={onSaved} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('radio', { name: /顺序生成/ }))
    await user.type(screen.getByLabelText('标签前缀'), '主邮箱')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(bodyRef.current).toMatchObject({ label_mode: 'sequential', label_prefix: '主邮箱' })
  })
})
