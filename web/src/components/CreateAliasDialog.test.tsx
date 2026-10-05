import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import CreateAliasDialog from './CreateAliasDialog'

describe('CreateAliasDialog', () => {
  it('提供自由输入框而非名称库下拉', async () => {
    render(<CreateAliasDialog accountId="acc_1" open onClose={vi.fn()} onCreated={vi.fn()} />)
    // 不应出现"选择标签"下拉触发器
    expect(screen.queryByRole('button', { name: '选择标签' })).not.toBeInTheDocument()
    // 应有可编辑的标签输入框
    const input = screen.getByLabelText('标签')
    expect(input.tagName).toBe('INPUT')
  })

  it('提交用户输入的标签', async () => {
    let body: Record<string, unknown> | undefined
    server.use(
      http.post('/api/create', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ success: true, data: { email: 'new@icloud.com' } })
      }),
    )
    const onCreated = vi.fn()
    render(<CreateAliasDialog accountId="acc_1" open onClose={vi.fn()} onCreated={onCreated} />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText('标签'), '我的自定义标签')
    await user.click(screen.getByRole('button', { name: /创建$/ }))

    await waitFor(() => expect(onCreated).toHaveBeenCalledWith('new@icloud.com'))
    expect(body).toEqual({ account_id: 'acc_1', label: '我的自定义标签' })
  })

  it('标签为空时不提交', async () => {
    let called = false
    server.use(
      http.post('/api/create', () => {
        called = true
        return HttpResponse.json({ success: true, data: { email: 'x@icloud.com' } })
      }),
    )
    render(<CreateAliasDialog accountId="acc_1" open onClose={vi.fn()} onCreated={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /创建$/ }))
    expect(called).toBe(false)
    expect(await screen.findByRole('alert')).toHaveTextContent('请输入标签')
  })
})
