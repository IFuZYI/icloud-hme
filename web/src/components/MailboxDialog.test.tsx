import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import MailboxDialog from './MailboxDialog'

describe('MailboxDialog', () => {
  it('父组件以等价配置重渲染时保留用户输入', async () => {
    const props = {
      accountId: 'acc_1',
      open: true,
      onClose: vi.fn(),
      onSaved: vi.fn(),
      current: { provider: 'qq', email: '', imap_host: 'imap.qq.com', imap_port: 993 },
    }
    const { rerender } = render(<MailboxDialog {...props} />)
    const user = userEvent.setup()
    const email = screen.getByLabelText('收件邮箱')

    await user.type(email, 'inbox@example.com')
    rerender(<MailboxDialog {...props} current={{ ...props.current! }} />)

    expect(email).toHaveValue('inbox@example.com')
  })
})
