import { useState } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import Dialog from './Dialog'

function ControlledInputDialog() {
  const [first, setFirst] = useState('')
  const [second, setSecond] = useState('')

  return (
    <Dialog title="输入测试" open onClose={() => undefined}>
      <label htmlFor="first-input">第一个输入框</label>
      <input id="first-input" value={first} onChange={(event) => setFirst(event.target.value)} />
      <label htmlFor="second-input">第二个输入框</label>
      <input id="second-input" value={second} onChange={(event) => setSecond(event.target.value)} />
    </Dialog>
  )
}

describe('Dialog', () => {
  it('通过 body portal 渲染，避免被页面容器的动画和层叠上下文偏移', () => {
    const { container } = render(
      <div className="animated-page">
        <Dialog title="居中测试" open onClose={() => undefined}>
          内容
        </Dialog>
      </div>,
    )

    expect(container.querySelector('.dialog-backdrop')).not.toBeInTheDocument()
    expect(document.body.querySelector('.dialog-backdrop')).toBeInTheDocument()
  })

  it('受控输入触发重渲染时不会抢走当前输入框焦点', async () => {
    const user = userEvent.setup()
    render(<ControlledInputDialog />)
    const first = screen.getByLabelText('第一个输入框')
    const second = screen.getByLabelText('第二个输入框')

    await user.click(second)
    await user.type(second, '中文输入')

    expect(second).toHaveValue('中文输入')
    expect(second).toHaveFocus()
    expect(first).toHaveValue('')
  })

  it('内容后到(先渲染"读取中…")时焦点仍会落进弹层', async () => {
    const { rerender } = render(
      <Dialog title="邮件详情" open onClose={() => undefined}>
        <p>读取中…</p>
      </Dialog>,
    )

    rerender(
      <Dialog title="邮件详情" open onClose={() => undefined}>
        <p>正文</p>
        <button>关闭</button>
      </Dialog>,
    )

    const closeButton = screen.getByRole('button', { name: '关闭' })
    await waitFor(() => expect(closeButton).toHaveFocus())
  })

  it('叠放时 Escape 只关闭最上层弹层', async () => {
    function Stacked() {
      const [detail, setDetail] = useState(true)
      const [confirm, setConfirm] = useState(true)
      return (
        <>
          <Dialog title="邮件详情" open={detail} onClose={() => setDetail(false)}>
            <button>详情按钮</button>
          </Dialog>
          <Dialog title="删除邮件" open={confirm} onClose={() => setConfirm(false)}>
            <button>确认按钮</button>
          </Dialog>
        </>
      )
    }
    const user = userEvent.setup()
    render(<Stacked />)

    // 一次 Esc 只应关掉最上层的确认框, 详情弹层必须保留
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('button', { name: '确认按钮' })).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: '详情按钮' })).toBeInTheDocument()

    // 再按一次才关闭详情
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('button', { name: '详情按钮' })).not.toBeInTheDocument())
  })
})
