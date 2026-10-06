import type { ApiResponse } from './types'

/** CSRF token,仅存 React 内存状态 */
let csrfToken: string | null = null

/** 全局 401 回调(由 AuthProvider 注册,避免循环 import) */
let unauthorizedHandler: (() => void) | null = null

/** 注册全局 401 回调 */
export function registerUnauthorizedHandler(handler: (() => void) | null): void {
  unauthorizedHandler = handler
}

/** 设置内存 CSRF token(由 AuthProvider 管理) */
export function setCSRFToken(token: string | null): void {
  csrfToken = token
}

/** ApiError 携带 HTTP 状态与稳定错误码 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
  signal?: AbortSignal
}

/** 业务级 401 白名单: 这些错误码不表示管理会话失效, 不应触发全局登出。
 *
 *  INVALID_CREDENTIALS: 登录 iCloud 时密码错误(不是管理员会话问题,
 *  否则会把管理员从登录对话框踢回管理台登录页)。
 *  OTP_INVALID: 验证码错误。
 *  UPSTREAM_UNAUTHORIZED: iCloud 侧 Cookie 失效(账号级, 非管理会话)。 */
const BUSINESS_401_CODES = new Set(['OTP_INVALID', 'UPSTREAM_UNAUTHORIZED', 'INVALID_CREDENTIALS'])

/**
 * 唯一的 fetch 入口。
 *
 * 统一设置 Accept、JSON Content-Type 与 credentials: same-origin;
 * 非 GET/HEAD/OPTIONS 自动携带 X-CSRF-Token;
 * 401 触发登出回调——但业务级 401(如验证码输错)除外, 否则会把管理员
 * 从 2FA 对话框直接踢回登录页, 使其无法重试。
 */
export async function request<T>(
  path: string,
  init?: RequestOptions,
  onUnauthorized?: () => void,
): Promise<T> {  const headers = new Headers(init?.headers)
  headers.set('Accept', 'application/json')
  headers.set('Content-Type', 'application/json')

  const method = (init?.method ?? 'GET').toUpperCase()
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS' && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }

  let body: BodyInit | undefined
  if (init?.body !== undefined) {
    body = typeof init.body === 'string' ? init.body : JSON.stringify(init.body)
  }

  let resp: Response
  try {
    resp = await fetch(path, {
      ...init,
      method,
      headers,
      body,
      credentials: 'same-origin',
    })
  } catch {
    throw new ApiError(0, 'NETWORK_ERROR', '网络连接失败，请检查服务状态')
  }

  let payload: ApiResponse<T>
  try {
    payload = (await resp.json()) as ApiResponse<T>
  } catch {
    // 无法解析响应体时按会话失效处理(401 且无 JSON 说明多半来自代理层)。
    if (resp.status === 401) {
      onUnauthorized?.()
      unauthorizedHandler?.()
    }
    throw new ApiError(resp.status, 'INVALID_RESPONSE', '网络连接失败，请检查服务状态')
  }

  if (resp.status === 401 && !BUSINESS_401_CODES.has(payload.code ?? '')) {
    onUnauthorized?.()
    unauthorizedHandler?.()
  }

  if (!resp.ok || payload.success === false) {
    throw new ApiError(
      resp.status,
      payload.code ?? 'INTERNAL_ERROR',
      payload.message ?? '请求失败',
    )
  }
  return payload.data as T
}
