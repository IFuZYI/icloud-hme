import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import SmartCookieInput from './SmartCookieInput'

describe('SmartCookieInput', () => {
  it('从粘贴的 Cookie Header 提取关键字段并格式化为 JSON', async () => {
    function Harness() {
      const [value, setValue] = useState('')
      return <SmartCookieInput id="cookies" value={value} onChange={setValue} />
    }
    render(<Harness />)

    fireEvent.change(screen.getByLabelText('Cookie（可选）'), {
      target: {
        value: 'foo=ignored; X-APPLE-WEBAUTH-TOKEN=v=1:t=token; X-APPLE-WEBAUTH-USER=v=1:s=1:d=123; X_APPLE_WEB_KB=key',
      },
    })

    expect(screen.getByRole('status')).toHaveTextContent('已识别 3 个关键 Cookie')
    expect(screen.getByRole('status')).toHaveTextContent('Cookie 格式可用')

    await userEvent.click(screen.getByRole('button', { name: '格式化为 JSON' }))
    expect(screen.getByLabelText('Cookie（可选）')).toHaveValue(JSON.stringify({
      'X-APPLE-WEBAUTH-TOKEN': 'v=1:t=token',
      'X-APPLE-WEBAUTH-USER': 'v=1:s=1:d=123',
      X_APPLE_WEB_KB: 'key',
    }, null, 2))
  })

  it('缺少必要 Cookie 时给出即时提示', () => {
    render(<SmartCookieInput id="cookies" value="X-APPLE-WEBAUTH-TOKEN=token" onChange={vi.fn()} />)

    expect(screen.getByText(/缺少 X-APPLE-WEBAUTH-USER/)).toBeInTheDocument()
  })
})
