import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountFormDialog from './AccountFormDialog'
import { request } from '../api/client'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, request: vi.fn() }
})

const mockedRequest = vi.mocked(request)

describe('AccountFormDialog', () => {
  beforeEach(() => {
    mockedRequest.mockReset()
    mockedRequest.mockResolvedValue({} as never)
  })

  it('邮箱输入自动填充名称，并依据 iCloud 中国区域名选择中国区', () => {
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('iCloud 邮箱'), { target: { value: 'owner@icloud.com.cn' } })

    expect(screen.getByLabelText('名称')).toHaveValue('owner')
    expect(screen.getByRole('button', { name: '选择区域' })).toHaveTextContent('中国区')
  })

  it('高级配置默认折叠，代理输入仅在启用代理后显示', () => {
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    expect(screen.queryByLabelText('Cookie（可选）')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /高级配置/ }))
    expect(screen.getByLabelText('Cookie（可选）')).toBeInTheDocument()
    expect(screen.queryByLabelText('代理地址')).not.toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('使用代理'))
    expect(screen.getByLabelText('代理地址')).toBeInTheDocument()
  })

  it('不启用账号代理时也能单独配置 IMAP 代理', async () => {
    const user = userEvent.setup()
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    await user.type(screen.getByLabelText('iCloud 邮箱'), 'owner@icloud.com')
    const nameInput = screen.getByLabelText('名称')
    await user.clear(nameInput)
    await user.type(nameInput, 'owner')
    await user.click(screen.getByRole('button', { name: /高级配置/ }))

    const imapProxy = screen.getByLabelText('IMAP 代理地址')
    await user.type(imapProxy, 'socks5://u:p@host:1080')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).toHaveBeenCalledWith('/api/accounts', {
      method: 'POST',
      body: JSON.stringify({
        name: 'owner',
        icloud_email: 'owner@icloud.com',
        host: 'icloud.com',
        proxy: '',
        imap_proxy: 'socks5://u:p@host:1080',
        cookies: '',
      }),
    })
  })

  it('使用代理时在代理地址下方提供 IMAP 代理地址，并随创建请求一起提交', async () => {
    const user = userEvent.setup()
    render(<AccountFormDialog open editing={null} onClose={vi.fn()} onSaved={vi.fn()} />)

    await user.type(screen.getByLabelText('iCloud 邮箱'), 'owner@icloud.com')
    const nameInput = screen.getByLabelText('名称')
    await user.clear(nameInput)
    await user.type(nameInput, 'owner')
    await user.click(screen.getByRole('button', { name: /高级配置/ }))
    await user.click(screen.getByLabelText('使用代理'))

    const proxy = screen.getByLabelText('代理地址')
    const imapProxy = screen.getByLabelText('IMAP 代理地址')
    expect(proxy.compareDocumentPosition(imapProxy) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    await user.type(proxy, 'http://u:p@host:8080')
    await user.type(imapProxy, 'socks5://u:p@host:1080')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(mockedRequest).toHaveBeenCalledWith('/api/accounts', {
      method: 'POST',
      body: JSON.stringify({
        name: 'owner',
        icloud_email: 'owner@icloud.com',
        host: 'icloud.com',
        proxy: 'http://u:p@host:8080',
        imap_proxy: 'socks5://u:p@host:1080',
        cookies: '',
      }),
    })
  })
})
