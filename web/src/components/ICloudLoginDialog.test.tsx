import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import ICloudLoginDialog from './ICloudLoginDialog'

describe('ICloudLoginDialog', () => {
  it('显示目标账号，并在需要双重认证时从密码流转到验证码流', async () => {
    server.use(
      http.post('/api/accounts/:id/login/begin', () =>
        HttpResponse.json({
          success: true,
          data: { status: 'otp_required', session_id: 'login-session-1' },
        }),
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
    await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByLabelText('验证码')).toBeInTheDocument()
    expect(screen.queryByLabelText('密码')).not.toBeInTheDocument()
  })

  it('两步提交: begin 拿 session_id, otp 提交验证码', async () => {
    const calls: Array<{ path: string; body: Record<string, unknown> }> = []
    server.use(
      http.post('/api/accounts/:id/login/begin', async ({ request }) => {
        calls.push({ path: 'begin', body: (await request.json()) as Record<string, unknown> })
        return HttpResponse.json({
          success: true,
          data: { status: 'otp_required', session_id: 'login-session-1' },
        })
      }),
      http.post('/api/accounts/:id/login/otp', async ({ request }) => {
        calls.push({ path: 'otp', body: (await request.json()) as Record<string, unknown> })
        return HttpResponse.json({ success: true, data: { status: 'done' } })
      }),
    )
    const onSaved = vi.fn()
    render(
      <ICloudLoginDialog
        accountId="acc_1"
        accountEmail="owner@icloud.com"
        open
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    )
    const user = userEvent.setup()

    await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
    await user.click(screen.getByRole('button', { name: '登录' }))
    await screen.findByLabelText('验证码')
    await user.type(screen.getByLabelText('验证码'), '123456')
    await user.click(screen.getByRole('button', { name: '验证并登录' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(calls).toEqual([
      { path: 'begin', body: { password: 'p@ssw0rd' } },
      { path: 'otp', body: { session_id: 'login-session-1', code: '123456' } },
    ])
  })

  it('无需 2FA 时 begin 直接完成', async () => {
    let otpCalled = false
    server.use(
      http.post('/api/accounts/:id/login/begin', () =>
        HttpResponse.json({ success: true, data: { status: 'done', account: { id: 'acc_1' } } }),
      ),
      http.post('/api/accounts/:id/login/otp', () => {
        otpCalled = true
        return HttpResponse.json({ success: true, data: {} })
      }),
    )
    const onSaved = vi.fn()
    render(
      <ICloudLoginDialog
        accountId="acc_1"
        accountEmail="owner@icloud.com"
        open
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    )
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
    await user.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
    expect(otpCalled).toBe(false)
  })

  it('验证码错误后保留在验证码流并提示错误', async () => {
    server.use(
      http.post('/api/accounts/:id/login/begin', () =>
        HttpResponse.json({
          success: true,
          data: { status: 'otp_required', session_id: 'login-session-1' },
        }),
      ),
      http.post('/api/accounts/:id/login/otp', () =>
        HttpResponse.json({ success: false, code: 'OTP_INVALID', message: 'OTP 验证码错误' }, { status: 401 }),
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
    await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
    await user.click(screen.getByRole('button', { name: '登录' }))
    await screen.findByLabelText('验证码')
    await user.type(screen.getByLabelText('验证码'), '000000')
    await user.click(screen.getByRole('button', { name: '验证并登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('OTP 验证码错误')
    // 仍在验证码流,可直接重输
    expect(screen.getByLabelText('验证码')).toBeInTheDocument()
  })

  it('提交期间显示登录状态并禁用按钮', async () => {
    let resolveRequest: (() => void) | undefined
    server.use(
      http.post('/api/accounts/:id/login/begin', async () => {
        await new Promise<void>((resolve) => { resolveRequest = resolve })
        return HttpResponse.json({ success: true, data: { status: 'done' } })
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
    await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(screen.getByRole('button', { name: '登录中…' })).toBeDisabled()
    await waitFor(() => expect(resolveRequest).toBeDefined())
    resolveRequest?.()
  })
})
