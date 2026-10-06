import { http, HttpResponse } from 'msw'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import AccountsPage from './AccountsPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary } from '../api/types'

// 复现 dogfood QA 发现的表格渲染缺陷: 长文本撑高行 / 别名计数换行 / 原始 ISO 时间
const accounts: AccountSummary[] = [
  {
    id: 'acc_long',
    name: '测试主号（很长的名字用来测试截断行为）',
    real_email: 'very-long-email-address-for-testing@icloud.com',
    icloud_email: 'very-long-email-address-for-testing@icloud.com',
    host: 'icloud.com',
    status: 'active',
    alias_total: 123,
    alias_active: 45,
    has_cookies: true,
    has_app_password: true,
    has_proxy: false,
    last_validated: '2026-10-05T09:00:00+08:00',
    created_at: '2026-08-01T09:00:00+08:00',
  },
]

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <AccountsPage />
      </ToastProvider>
    </MemoryRouter>,
  )
}

describe('AccountsPage 表格渲染', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    )
  })

  it('最近验证时间格式化为可读形式, 不显示原始 ISO 串', async () => {
    renderPage()
    expect(await screen.findByText('测试主号（很长的名字用来测试截断行为）')).toBeInTheDocument()
    // 不应出现原始 ISO 串
    expect(screen.queryByText('2026-10-05T09:00:00+08:00')).toBeNull()
    // 应显示格式化后的时间(zh-CN 格式)
    expect(screen.getByText(/2026\/10\/05/)).toBeInTheDocument()
  })

  it('别名计数用不换行的整体单元渲染, 避免 "45 /" 与 "123" 折行', async () => {
    renderPage()
    const cell = await screen.findByText('45 / 123')
    expect(cell).toBeInTheDocument()
    // 计数单元不应允许内部换行
    expect(cell.className).toContain('alias-count')
  })

  it('名称与邮箱单行截断并带 title 提示, 避免长文本撑高行', async () => {
    renderPage()
    const name = await screen.findByText('测试主号（很长的名字用来测试截断行为）')
    // 长文本必须可截断(单行 + ellipsis 类)并提供完整值提示
    expect(name.className).toContain('cell-truncate')
    expect(name).toHaveAttribute('title', '测试主号（很长的名字用来测试截断行为）')
    const email = screen.getByText('very-long-email-address-for-testing@icloud.com')
    expect(email.className).toContain('cell-truncate')
    expect(email).toHaveAttribute('title', 'very-long-email-address-for-testing@icloud.com')
  })

  it('短标签列(凭据/最近验证)禁止逐字换行, 避免窄列把整行撑高', async () => {
    renderPage()
    // 凭据列的短文本曾被窄列逐字折成 3 行("未"/"配"/"置"), 行高被撑到 92px
    const cred = await screen.findByText('已配置（Cookie·App密码）')
    expect(cred.className).toContain('cell-nowrap')
    const time = screen.getByText(/2026\/10\/05/)
    expect(time.className).toContain('cell-nowrap')
  })
})
