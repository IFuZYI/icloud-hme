import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import AliasTaskForm from './AliasTaskForm'

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
    const prefix = screen.getByRole('textbox')
    const user = userEvent.setup()

    await user.clear(prefix)
    await user.type(prefix, '我的任务')
    rerender(<AliasTaskForm {...props} accounts={[...accounts]} />)

    expect(prefix).toHaveValue('我的任务')
  })
})
