/**
 * API 类型定义——与 internal/server 冻结契约保持一致。
 */

/** 统一响应包裹 */
export interface ApiResponse<T> {
  success: boolean
  data?: T
  code?: string
  message?: string
}

/** 账号安全摘要(无秘密字段) */
export interface AccountSummary {
  id: string
  name: string
  real_email: string
  icloud_email: string
  host: string
  status: 'active' | 'pending' | 'error' | string
  alias_total: number
  alias_active: number
  has_cookies: boolean
  has_app_password: boolean
  has_proxy: boolean
  mailbox?: MailboxSummary
  last_validated: string
  status_message?: string
  created_at: string
}

export interface MailboxSummary {
  provider: string
  email: string
  imap_host: string
  imap_port: number
}

/** HME 别名(iCloud 返回字段风格为 camelCase) */
export interface Alias {
  email: string
  anonymousId: string
  label: string
  active: boolean
  createdAt?: string
}

/** 邮件摘要 */
export interface InboxMessage {
  id: string
  from: string
  to: string
  subject: string
  date: string
  preview: string
}

export interface FullMessage extends InboxMessage {
  body: string
  content_type: string
}

/** 收件箱查询结果 */
export interface InboxResult {
  account_id: string
  alias?: string
  count: number
  messages: InboxMessage[]
  method: 'imap' | 'web_api'
  /** 符合日期过滤的邮件总数(用于「加载更多」) */
  total: number
  /** 本页起始偏移(新→旧) */
  offset: number
  /** 降级读取的说明(如 IMAP 不可用),缺省表示一切正常 */
  warning?: string
}

/** 登录响应 */
export interface LoginResult {
  csrf_token: string
  expires_at: string
}

/** 自动创建任务(与 internal/server AliasTask 契约一致) */
export interface AliasTask {
  id: string
  enabled: boolean
  account_id: string
  interval_minutes: number
  batch_count: number
  max_total: number
  created_count: number
  label_prefix: string
  last_run?: string
  next_run?: string
  last_success: number
  last_error?: string
}

/** 自动任务运行日志(与 internal/server AliasTaskLog 契约一致) */
export interface AliasTaskLog {
  id: string
  task_id: string
  time: string
  level: string
  message: string
}
