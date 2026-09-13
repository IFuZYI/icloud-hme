import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import CookieDialog from './CookieDialog'

describe('CookieDialog', () => {
  it('展示 JSON 格式的 iCloud Web Cookie 示例和用途', () => {
    render(
      <CookieDialog
        accountId="acc_1"
        open
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(screen.getByLabelText('Cookie')).toHaveAttribute(
      'placeholder',
      '{\n  "X-APPLE-WEBAUTH-TOKEN": "v=1:t=AQAAAAB...",\n  "X-APPLE-WEBAUTH-USER": "d=...:s=...",\n  "X_APPLE_WEB_KB": "..."\n}',
    )
    expect(screen.getByText(/iCloud Web 端会话 Token/)).toBeInTheDocument()
    expect(screen.getByText(/双重认证（2FA）受信任设备\/浏览器标记/)).toBeInTheDocument()
    expect(screen.getByText(/Protected Cloud Storage/)).toBeInTheDocument()
    expect(screen.getByText(/验证请求合法性及用户身份/)).toBeInTheDocument()
  })
})
