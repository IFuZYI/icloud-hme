import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import TaskMoreMenu from './TaskMoreMenu'

describe('TaskMoreMenu', () => {
  it('打开可聚焦菜单并执行暂停动作', async () => {
    const onToggle = vi.fn()
    render(
      <TaskMoreMenu
        enabled
        onToggle={onToggle}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /更多/ }))
    const menu = screen.getByRole('menu', { name: '任务操作' })
    expect(menu).toHaveAttribute('tabindex', '-1')

    await user.click(screen.getByRole('menuitem', { name: '暂停' }))
    expect(onToggle).toHaveBeenCalledOnce()
  })
})
