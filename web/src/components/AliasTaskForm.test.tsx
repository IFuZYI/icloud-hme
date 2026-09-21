import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import AliasTaskForm from './AliasTaskForm'
import { server } from '../test/server'

const accounts = [{ id: 'acc_1', name: '主账号' }]

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

  it('提交目标数量与周期，标签由内置名称库自动分配', async () => {
    let body: Record<string, unknown> | undefined
    server.use(
      http.post('/api/alias-tasks', async ({ request }) => {
        body = await request.json() as Record<string, unknown>
        return HttpResponse.json({ success: true, data: {} }, { status: 201 })
      }),
    )
    const onClose = vi.fn()
    const onSaved = vi.fn()
    render(<AliasTaskForm open accounts={accounts} onClose={onClose} onSaved={onSaved} />)
    const user = userEvent.setup()

    expect(screen.queryByText('标签前缀')).not.toBeInTheDocument()
    expect(screen.getAllByText(/名称库/)).toHaveLength(2)
    await user.clear(screen.getByLabelText('目标邮箱数量'))
    await user.type(screen.getByLabelText('目标邮箱数量'), '12')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(body).toMatchObject({
      account_id: 'acc_1', interval_minutes: 60, target_count: 12, daily_limit: 20,
    })
    expect(body).not.toHaveProperty('label_prefix')
    expect(body).not.toHaveProperty('batch_count')
  })
})
