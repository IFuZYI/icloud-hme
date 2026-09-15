import userEvent from '@testing-library/user-event'
import { screen, waitFor } from '@testing-library/react'

/**
 * 在测试里操作 SelectMenu(自定义下拉, 非原生 select):
 * 打开 ariaLabel 对应的触发按钮, 再点击选项文本。
 */
export async function chooseOption(user: ReturnType<typeof userEvent.setup>, ariaLabel: string | RegExp, optionLabel: string | RegExp) {
  await user.click(screen.getByRole('button', { name: ariaLabel }))
  await waitFor(() => {
    const item = screen.getAllByRole('option', { name: optionLabel })
    if (item.length === 0) throw new Error(`option ${String(optionLabel)} not found`)
  })
  await user.click(screen.getAllByRole('option', { name: optionLabel })[
    screen.getAllByRole('option', { name: optionLabel }).length - 1
  ])
}
