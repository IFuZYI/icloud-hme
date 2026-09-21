import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import ICloudLoginDialog from './ICloudLoginDialog'

describe('ICloudLoginDialog', () => {
  it('显示目标账号，并在需要双重认证时从密码流转到验证码流', async () => {
    server.use(
      http.post('/api/accounts/:id/login', () =>
        HttpResponse.json(
          { success: false, code: 'OTP_REQUIRED', message: '需要提供 OTP 验证码' },
          { status: 409 },
        ),
      ),
    )
    render(
      <ICloudLoginDialog
        accountId="acc_1"
        accountEmail="owner@icloud.com"
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )
    const user = userEvent.setup()

    expect(screen.getByText('owner@icloud.com')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'p@ssw0rd' } })
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByLabelText('验证码')).toBeInTheDocument()
    expect(screen.queryByLabelText('密码')).not.toBeInTheDocument()
  })

  it('提交期间显示登录状态并禁用按钮', async () => {
    let resolveRequest: (() => void) | undefined
    server.use(
      http.post('/api/accounts/:id/login', async () => {
        await new Promise<void>((resolve) => { resolveRequest = resolve })
        return HttpResponse.json({ success: true, data: {} })
      }),
    )
    render(
      <ICloudLoginDialog
        accountId="acc_1"
        accountEmail="owner@icloud.com"
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'p@ssw0rd' } })
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(screen.getByRole('button', { name: '登录中…' })).toBeDisabled()
    await waitFor(() => expect(resolveRequest).toBeDefined())
    resolveRequest?.()
  })
})
