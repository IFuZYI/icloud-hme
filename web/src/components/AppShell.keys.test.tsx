import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, describe, expect, it } from 'vitest'
import AppShell from './AppShell'
import { AuthProvider } from '../auth/AuthProvider'
import { ToastProvider } from './ToastProvider'

// jsdom 不实现 scrollIntoView; AppShell 的路由切换滚动依赖它。
beforeAll(() => {
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {}
  }
})

// 导航包含「秘钥」入口(脚本调用凭证的管理页面)。
describe('AppShell 导航', () => {
  it('主导航包含秘钥菜单项, 指向 /keys', () => {
    render(
      <MemoryRouter initialEntries={['/keys']}>
        <AuthProvider>
          <ToastProvider>
            <AppShell />
          </ToastProvider>
        </AuthProvider>
      </MemoryRouter>,
    )
    const link = screen.getByRole('link', { name: /秘钥/ })
    expect(link).toHaveAttribute('href', '/keys')
  })
})
