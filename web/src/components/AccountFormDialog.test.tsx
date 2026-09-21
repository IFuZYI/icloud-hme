import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import AccountFormDialog from './AccountFormDialog'

describe('AccountFormDialog', () => {
  it('邮箱输入自动填充名称，并依据 iCloud 中国区域名选择中国区', () => {
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('iCloud 邮箱'), { target: { value: 'owner@icloud.com.cn' } })

    expect(screen.getByLabelText('名称')).toHaveValue('owner')
    expect(screen.getByRole('button', { name: '选择区域' })).toHaveTextContent('中国区')
  })

  it('高级配置默认折叠，代理输入仅在启用代理后显示', () => {
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    expect(screen.queryByLabelText('Cookie（可选）')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /高级配置/ }))
    expect(screen.getByLabelText('Cookie（可选）')).toBeInTheDocument()
    expect(screen.queryByLabelText('代理地址')).not.toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('使用代理'))
    expect(screen.getByLabelText('代理地址')).toBeInTheDocument()
  })
})
