import { useState } from 'react'
import { render, screen } from '@testing-library/react'
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
})
