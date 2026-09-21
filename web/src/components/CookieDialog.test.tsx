import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import CookieDialog from './CookieDialog'

describe('CookieDialog', () => {
  it('展示智能 Cookie 输入与关键 Cookie 用途', () => {
    render(
      <CookieDialog
        accountId="acc_1"
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('Cookie（必填）')).toHaveAttribute('placeholder', expect.stringContaining('X-APPLE-WEBAUTH-TOKEN'))
    expect(screen.getByText(/iCloud Web 端会话 Token/)).toBeInTheDocument()
    expect(screen.getByText(/双重认证（2FA）受信任设备\/浏览器标记/)).toBeInTheDocument()
    expect(screen.getByText(/Protected Cloud Storage/)).toBeInTheDocument()
    expect(screen.getByText(/验证请求合法性及用户身份/)).toBeInTheDocument()
  })
})
