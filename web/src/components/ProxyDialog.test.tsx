import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProxyDialog from './ProxyDialog'
import { request } from '../api/client'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, request: vi.fn() }
})

const mockedRequest = vi.mocked(request)

describe('ProxyDialog', () => {
  beforeEach(() => {
    mockedRequest.mockReset()
    mockedRequest.mockResolvedValue({} as never)
  })

  it('在代理地址下方提供 IMAP 代理地址输入框', () => {
    render(<ProxyDialog accountId="acc_1" open onClose={vi.fn()} onSaved={vi.fn()} />)

    const proxy = screen.getByLabelText('代理地址')
    const imapProxy = screen.getByLabelText('IMAP 代理地址')
    expect(proxy).toBeInTheDocument()
    expect(imapProxy).toBeInTheDocument()
    // 顺序: IMAP 代理在代理地址之后。
    expect(proxy.compareDocumentPosition(imapProxy) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('同时填写两项时一起提交', async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    render(<ProxyDialog accountId="acc_1" open onClose={vi.fn()} onSaved={onSaved} />)

    await user.type(screen.getByLabelText('代理地址'), 'http://u:p@127.0.0.1:8080')
    await user.type(screen.getByLabelText('IMAP 代理地址'), 'socks5://u:p@127.0.0.1:1080')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).toHaveBeenCalledWith('/api/accounts/acc_1/proxy', {
      method: 'PUT',
      body: JSON.stringify({
        proxy: 'http://u:p@127.0.0.1:8080',
        imap_proxy: 'socks5://u:p@127.0.0.1:1080',
      }),
    })
    expect(onSaved).toHaveBeenCalled()
  })

  it('只填 IMAP 代理时不会顺手清掉账号代理', async () => {
    const user = userEvent.setup()
    render(<ProxyDialog accountId="acc_1" open hasProxy hasIMAPProxy onClose={vi.fn()} onSaved={vi.fn()} />)

    await user.type(screen.getByLabelText('IMAP 代理地址'), 'socks5://u:p@127.0.0.1:1080')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).toHaveBeenCalledWith('/api/accounts/acc_1/proxy', {
      method: 'PUT',
      body: JSON.stringify({ imap_proxy: 'socks5://u:p@127.0.0.1:1080' }),
    })
  })

  it('两项都留空且未勾选清除时拒绝提交', async () => {
    const user = userEvent.setup()
    render(<ProxyDialog accountId="acc_1" open onClose={vi.fn()} onSaved={vi.fn()} />)

    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent(/请填写代理地址/)
  })

  it('勾选清除后才提交空串', async () => {
    const user = userEvent.setup()
    render(<ProxyDialog accountId="acc_1" open hasProxy hasIMAPProxy onClose={vi.fn()} onSaved={vi.fn()} />)

    await user.click(screen.getByLabelText('清除已配置的账号代理'))
    await user.click(screen.getByLabelText('清除已配置的 IMAP 代理'))
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).toHaveBeenCalledWith('/api/accounts/acc_1/proxy', {
      method: 'PUT',
      body: JSON.stringify({ proxy: '', imap_proxy: '' }),
    })
  })

  it('未配置代理时不显示清除勾选框', () => {
    render(<ProxyDialog accountId="acc_1" open onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.queryByLabelText(/清除已配置/)).not.toBeInTheDocument()
  })

  it('提示说明两个代理互不影响、留空表示不改动', () => {
    render(<ProxyDialog accountId="acc_1" open onClose={vi.fn()} onSaved={vi.fn()} />)
    expect(screen.getByText(/互不影响/)).toBeInTheDocument()
    expect(screen.getByText(/留空表示不改动/)).toBeInTheDocument()
  })
})
