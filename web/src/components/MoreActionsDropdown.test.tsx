import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import MoreActionsDropdown from './MoreActionsDropdown'

describe('MoreActionsDropdown', () => {
  it('按认证与配置分组，并把选择的操作回传给父组件', async () => {
    const onAction = vi.fn()
    render(<MoreActionsDropdown accountName="主账号" onAction={onAction} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: '主账号 更多操作' }))
    expect(screen.getByText('认证')).toBeInTheDocument()
    expect(screen.getByText('配置')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '更新 Cookie' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '别名管理' })).toBeInTheDocument()

    await user.click(screen.getByRole('menuitem', { name: 'iCloud 登录' }))
    expect(onAction).toHaveBeenCalledWith('login')
  })
})
