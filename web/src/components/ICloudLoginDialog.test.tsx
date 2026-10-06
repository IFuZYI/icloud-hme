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

// otp_required 缺 session_id 时给出明确错误, 而不是提交空 session_id。
it('begin 返回 otp_required 但缺 session_id 时提示重试', async () => {
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({ success: true, data: { status: 'otp_required' } }),
    ),
  )
  render(<ICloudLoginDialog accountId="acc_1" accountEmail="a@example.com" open onClose={() => {}} onSaved={() => {}} />)
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'pw')
  await user.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('服务端未返回登录会话')
  // 不应进入验证码步骤
  expect(screen.queryByLabelText('验证码')).toBeNull()
})

// 密码错误(INVALID_CREDENTIALS)时应留在密码输入阶段且保留已输入的密码,
// 允许直接修正后重试; 同时不得触发全局登出。
// 回归背景: 旧版把 401 一律触发全局登出, 管理员被从对话框踢回登录页。
it('密码错误保留在密码输入阶段且不丢输入', async () => {
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json(
        { success: false, code: 'INVALID_CREDENTIALS', message: 'iCloud 邮箱或密码错误' },
        { status: 401 },
      ),
    ),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'wrong-pass')
  await user.click(screen.getByRole('button', { name: '登录' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('iCloud 邮箱或密码错误')
  // 仍在密码输入阶段: 密码框存在且保留输入, 未进入验证码流
  const password = screen.getByLabelText('密码') as HTMLInputElement
  expect(password.value).toBe('wrong-pass')
  expect(screen.queryByLabelText('验证码')).toBeNull()
})
