import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { request, setCSRFToken } from './client'
import { server } from '../test/server'

describe('api client', () => {
  beforeEach(() => {
    setCSRFToken(null)
    server.resetHandlers()
  })

  it('成功解包 data', async () => {
    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json({ success: true, data: { id: 'acc_1' } }),
      ),
    )
    const data = await request<{ id: string }>('/api/accounts')
    expect(data.id).toBe('acc_1')
  })

  it('非 2xx 抛出 ApiError 并携带 code/message/status', async () => {
    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json(
          { success: false, code: 'VALIDATION_ERROR', message: '参数错误' },
          { status: 400 },
        ),
      ),
    )
    await expect(request('/api/accounts')).rejects.toMatchObject({
      status: 400,
      code: 'VALIDATION_ERROR',
      message: '参数错误',
    })
  })

  it('非 JSON 响应抛出网络错误', async () => {
    server.use(
      http.get('/api/accounts', () =>
        new HttpResponse('<html>bad</html>', { status: 502 }),
      ),
    )
    await expect(request('/api/accounts')).rejects.toThrow('网络连接失败')
  })

  it('401 触发全局回调', async () => {
    const onUnauthorized = vi.fn()
    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json(
          { success: false, code: 'AUTH_REQUIRED', message: '请先登录' },
          { status: 401 },
        ),
      ),
    )
    request('/api/accounts', undefined, onUnauthorized).catch(() => {})
    await vi.waitFor(() => expect(onUnauthorized).toHaveBeenCalled())
  })

  it('业务级 401(验证码错误/上游会话失效)不触发全局登出', async () => {
    // 回归背景: OTP 输错返回 401 OTP_INVALID, 全局登出会把管理员从
    // 2FA 对话框直接踢回登录页, "保留会话可重试"的承诺失效。
    const onUnauthorized = vi.fn()
    server.use(
      http.post('/api/accounts/acc_1/login/otp', () =>
        HttpResponse.json(
          { success: false, code: 'OTP_INVALID', message: 'OTP 验证码错误' },
          { status: 401 },
        ),
      ),
    )
    await expect(
      request('/api/accounts/acc_1/login/otp', { method: 'POST' }, onUnauthorized),
    ).rejects.toMatchObject({ code: 'OTP_INVALID' })
    expect(onUnauthorized).not.toHaveBeenCalled()

    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json(
          { success: false, code: 'UPSTREAM_UNAUTHORIZED', message: 'iCloud 会话失效,请更新 Cookie' },
          { status: 401 },
        ),
      ),
    )
    await expect(
      request('/api/accounts', undefined, onUnauthorized),
    ).rejects.toMatchObject({ code: 'UPSTREAM_UNAUTHORIZED' })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('iCloud 密码错误(INVALID_CREDENTIALS 401)不触发全局登出', async () => {
    // 回归背景: 登录对话框里输错 iCloud 密码返回 401 INVALID_CREDENTIALS,
    // 全局登出会把管理员从对话框踢回管理台登录页, 无法重试。
    const onUnauthorized = vi.fn()
    server.use(
      http.post('/api/accounts/acc_1/login/begin', () =>
        HttpResponse.json(
          { success: false, code: 'INVALID_CREDENTIALS', message: 'iCloud 邮箱或密码错误' },
          { status: 401 },
        ),
      ),
    )
    await expect(
      request('/api/accounts/acc_1/login/begin', { method: 'POST' }, onUnauthorized),
    ).rejects.toMatchObject({ code: 'INVALID_CREDENTIALS', message: 'iCloud 邮箱或密码错误' })
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('GET 不带 CSRF,POST 自动带 CSRF', async () => {
    setCSRFToken('csrf-token-123')
    let getHeaders: Headers | undefined
    let postHeaders: Headers | undefined
    server.use(
      http.get('/api/auth/session', ({ request }) => {
        getHeaders = request.headers
        return HttpResponse.json({ success: true, data: {} })
      }),
      http.post('/api/auth/logout', ({ request }) => {
        postHeaders = request.headers
        return HttpResponse.json({ success: true, data: { logged_out: true } })
      }),
    )
    await request('/api/auth/session')
    await request('/api/auth/logout', { method: 'POST' })
    expect(getHeaders?.get('X-CSRF-Token')).toBeNull()
    expect(postHeaders?.get('X-CSRF-Token')).toBe('csrf-token-123')
  })

  it('使用 credentials same-origin', async () => {
    let seen: RequestInit | undefined
    server.use(
      http.get('/api/accounts', ({ request }) => {
        seen = request as unknown as RequestInit
        return HttpResponse.json({ success: true, data: [] })
      }),
    )
    await request('/api/accounts')
    expect(seen?.credentials).toBe('same-origin')
  })

  it('支持 AbortSignal', async () => {
    const controller = new AbortController()
    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json({ success: true, data: [] }),
      ),
    )
    controller.abort()
    await expect(
      request('/api/accounts', { signal: controller.signal }),
    ).rejects.toThrow()
  })
})
