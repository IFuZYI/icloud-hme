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
          data: { status: 'otp_required', session_id: 'login-session-1', push_sent: true },
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
          data: { status: 'otp_required', session_id: 'login-session-1', push_sent: true },
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
          data: { status: 'otp_required', session_id: 'login-session-1', push_sent: true },
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

// 推送失败(push_sent=false)时给出明确提示, 并提供「重发验证码」恢复路径。
// 回归背景: 旧版推送失败只记服务端日志, 用户界面永远显示「请输入验证码」,
// 而验证码根本不会到达设备——用户只能无限等待。
it('push_sent=false 时提示推送失败并允许重发', async () => {
  let resendCalled = false
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.post('/api/accounts/:id/login/resend', async ({ request }) => {
      const body = (await request.json()) as Record<string, unknown>
      resendCalled = body.session_id === 'login-session-1'
      return HttpResponse.json({ success: true, data: { sent: true } })
    }),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))

  expect(await screen.findByText(/验证码推送失败/)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '重发验证码' }))
  await waitFor(() => expect(resendCalled).toBe(true))
  expect(await screen.findByText(/已重新请求推送验证码/)).toBeInTheDocument()
})

// 推送失败时可用短信验证作为备选通道: 取手机号 → 发短信 → 提交短信验证码。
it('推送失败时可改用短信验证', async () => {
  const calls: string[] = []
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.get('/api/accounts/:id/login/phones', () => {
      calls.push('phones')
      return HttpResponse.json({
        success: true,
        data: { phones: [{ id: 2, number_with_dial_code: '+86 138****1234' }] },
      })
    }),
    http.post('/api/accounts/:id/login/sms', async ({ request }) => {
      calls.push('sms')
      const body = (await request.json()) as Record<string, unknown>
      expect(body.phone_id).toBe(2)
      return HttpResponse.json({ success: true, data: { sent: true } })
    }),
    http.post('/api/accounts/:id/login/otp', async ({ request }) => {
      calls.push('otp')
      const body = (await request.json()) as Record<string, unknown>
      // 短信通道: 必须带 method=sms 与 phone_id
      expect(body.method).toBe('sms')
      expect(body.phone_id).toBe(2)
      return HttpResponse.json({ success: true, data: { status: 'done' } })
    }),
  )
  const onSaved = vi.fn()
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={onSaved} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))

  await screen.findByText(/验证码推送失败/)
  await user.click(screen.getByRole('button', { name: '改用短信验证' }))
  expect(await screen.findByText(/已向 \+86 138\*\*\*\*1234 发送短信验证码/)).toBeInTheDocument()
  expect(calls).toEqual(['phones', 'sms'])

  await user.type(screen.getByLabelText('验证码'), '654321')
  await user.click(screen.getByRole('button', { name: '验证并登录' }))
  await waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
  expect(calls).toEqual(['phones', 'sms', 'otp'])
})

// 提交在途(step=LOADING)时必须保持验证码 UI: 恢复按钮仍在(禁用), 不得闪回密码框。
// 回归背景: 状态派生用 `step === '2FA_INPUT'` 时, LOADING 期间界面会闪回密码输入框、
// 恢复按钮整体消失(审查探针在中断前抓到, 见 commit 修复说明)。
it('验证码提交在途时保持验证码流且恢复按钮禁用', async () => {
  let resolveOtp: (() => void) | undefined
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.post('/api/accounts/:id/login/otp', async () => {
      await new Promise<void>((resolve) => { resolveOtp = resolve })
      return HttpResponse.json({ success: true, data: { status: 'done' } })
    }),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByText(/验证码推送失败/)
  await user.type(screen.getByLabelText('验证码'), '123456')
  await user.click(screen.getByRole('button', { name: '验证并登录' }))
  await waitFor(() => expect(resolveOtp).toBeDefined())

  // 提交在途: 仍在验证码流(验证码输入框存在), 恢复按钮禁用而非消失
  expect(screen.getByLabelText('验证码')).toBeInTheDocument()
  expect(screen.queryByLabelText('密码')).toBeNull()
  expect(screen.getByRole('button', { name: '重发验证码' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '改用短信验证' })).toBeDisabled()
  resolveOtp?.()
})

// push_sent 缺省(旧服务端不返回该字段)应视为已推送, 不误报失败。
it('push_sent 缺省时显示已推送提示而非失败警示', async () => {
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1' },
      }),
    ),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))
  expect(await screen.findByText(/验证码已推送到受信任设备/)).toBeInTheDocument()
  expect(screen.queryByText(/验证码推送失败/)).toBeNull()
})

// 恢复操作失败走统一 ApiError 路径(展示服务端 message, 不吞错)。
it('重发失败时显示服务端错误信息', async () => {
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.post('/api/accounts/:id/login/resend', () =>
      HttpResponse.json({ success: false, code: 'UPSTREAM_FAILURE', message: '上游推送被拒绝' }, { status: 502 }),
    ),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByText(/验证码推送失败/)
  await user.click(screen.getByRole('button', { name: '重发验证码' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('上游推送被拒绝')
})

// 无受信任手机号时给出明确错误, 不静默停留在原状态。
it('手机号列表为空时提示无法使用短信验证', async () => {
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.get('/api/accounts/:id/login/phones', () =>
      HttpResponse.json({ success: true, data: { phones: [] } }),
    ),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByText(/验证码推送失败/)
  await user.click(screen.getByRole('button', { name: '改用短信验证' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('没有受信任手机号')
})

// 重发在途(busy)时全部按钮禁用: 恢复操作与提交互斥, 防并发双发。
it('重发在途时全部按钮禁用', async () => {
  let resolveResend: (() => void) | undefined
  server.use(
    http.post('/api/accounts/:id/login/begin', () =>
      HttpResponse.json({
        success: true,
        data: { status: 'otp_required', session_id: 'login-session-1', push_sent: false },
      }),
    ),
    http.post('/api/accounts/:id/login/resend', async () => {
      await new Promise<void>((resolve) => { resolveResend = resolve })
      return HttpResponse.json({ success: true, data: { sent: true } })
    }),
  )
  render(
    <ICloudLoginDialog accountId="acc_1" accountEmail="owner@icloud.com" open onClose={vi.fn()} onSaved={vi.fn()} />,
  )
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('密码'), 'p@ssw0rd')
  await user.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByText(/验证码推送失败/)
  await user.click(screen.getByRole('button', { name: '重发验证码' }))
  await waitFor(() => expect(resolveResend).toBeDefined())
  expect(screen.getByRole('button', { name: '重发验证码' })).toBeDisabled()
  expect(screen.getByRole('button', { name: '改用短信验证' })).toBeDisabled()
  // busy 期间主按钮标签切换为「登录中…」且禁用
  expect(screen.getByRole('button', { name: '登录中…' })).toBeDisabled()
  resolveResend?.()
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
