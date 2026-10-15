import { http, HttpResponse } from 'msw'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import KeysPage from './KeysPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'
import type { ApiKey, PermissionCatalog } from '../api/types'

// ====================================================================
// 秘钥页面: 列表 / 创建(含权限勾选) / 编辑 / 删除 / 复制
// ====================================================================

const catalog: PermissionCatalog = {
  groups: [
    { id: 'inbox', label: '收件箱' },
    { id: 'aliases', label: '别名' },
  ],
  permissions: [
    { id: 'inbox.list', label: '读取收件箱列表', group: 'inbox' },
    { id: 'inbox.delete', label: '删除邮件', group: 'inbox' },
    { id: 'aliases.list', label: '查看别名', group: 'aliases' },
  ],
}

const keys: ApiKey[] = [
  {
    id: 'key_1',
    name: '脚本A',
    key: 'sk-abcdefghijklmnopqrstuvwx',
    enabled: true,
    permissions: ['inbox.list'],
    created_at: '2026-10-01T10:00:00+08:00',
    last_used_at: '2026-10-02T09:30:00+08:00',
    last_used_ip: '192.168.1.5',
  },
  {
    id: 'key_2',
    name: '停用脚本',
    key: 'sk-zyxwvutsrqponmlkjihgfedc',
    enabled: false,
    permissions: [],
    created_at: '2026-09-20T08:00:00+08:00',
  },
]

function renderPage() {
  return render(
    <ToastProvider>
      <KeysPage />
    </ToastProvider>,
  )
}

describe('KeysPage', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    server.use(
      http.get('/api/keys', () => HttpResponse.json({ success: true, data: { count: keys.length, keys } })),
      http.get('/api/permissions', () => HttpResponse.json({ success: true, data: catalog })),
    )
  })

  it('列出全部秘钥: 名称/状态/最近使用可见', async () => {
    renderPage()
    expect(await screen.findByText('脚本A')).toBeInTheDocument()
    expect(screen.getByText('停用脚本')).toBeInTheDocument()
    // 状态标签
    expect(screen.getByText('已启用')).toBeInTheDocument()
    expect(screen.getByText('已停用')).toBeInTheDocument()
    // 最近使用时间以项目统一格式显示(不依赖测试进程时区: 断言存在格式化后的日期行)
    expect(screen.getAllByText(/2026\/10\/0\d \d{2}:\d{2}/).length).toBeGreaterThan(0)
  })

  it('秘钥默认脱敏展示, 可切换显示与复制', async () => {
    // userEvent.setup() 会安装自己的剪贴板桩: 必须先 setup 再覆盖。
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })

    renderPage()
    await screen.findByText('脚本A')
    // 脱敏: 完整秘钥不出现在页面上
    expect(screen.queryByText('sk-abcdefghijklmnopqrstuvwx')).not.toBeInTheDocument()
    expect(screen.getByText(/sk-a.*w.*x/)).toBeInTheDocument()

    // 显示切换
    await user.click(screen.getAllByRole('button', { name: /显示秘钥/ })[0])
    expect(screen.getByText('sk-abcdefghijklmnopqrstuvwx')).toBeInTheDocument()

    // 复制
    await user.click(screen.getAllByRole('button', { name: /复制秘钥/ })[0])
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('sk-abcdefghijklmnopqrstuvwx'))
  })

  it('创建秘钥: 名称必填、秘钥可留空自动生成、权限按分组勾选', async () => {
    let created: unknown = null
    server.use(
      http.post('/api/keys', async ({ request }) => {
        created = await request.json()
        return HttpResponse.json({
          success: true,
          data: { ...keys[0], id: 'key_new', name: '新脚本', key: 'sk-newnewnewnewnewnewnewnew' },
        })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('脚本A')

    await user.click(screen.getByRole('button', { name: /创建秘钥/ }))
    const dialog = await screen.findByRole('dialog', { name: '创建秘钥' })

    // 权限按分组渲染
    expect(within(dialog).getByText('收件箱')).toBeInTheDocument()
    expect(within(dialog).getByText('别名')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('读取收件箱列表')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText('名称'), '新脚本')
    await user.click(within(dialog).getByLabelText('读取收件箱列表'))
    await user.click(within(dialog).getByRole('button', { name: /保存|创建/ }))

    await waitFor(() => expect(created).not.toBeNull())
    expect(created).toMatchObject({ name: '新脚本', permissions: ['inbox.list'] })
    // 秘钥留空: 提交时不含 key 字段(或为空串), 由服务端自动生成
    const body = created as Record<string, unknown>
    expect(body.key === undefined || body.key === '').toBe(true)
  })

  it('创建对话框: 手动填写秘钥时随请求提交', async () => {
    let created: Record<string, unknown> | null = null
    server.use(
      http.post('/api/keys', async ({ request }) => {
        created = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ success: true, data: keys[0] })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('脚本A')

    await user.click(screen.getByRole('button', { name: /创建秘钥/ }))
    const dialog = await screen.findByRole('dialog', { name: '创建秘钥' })
    await user.type(within(dialog).getByLabelText('名称'), '手动')
    await user.type(within(dialog).getByLabelText('秘钥'), 'my-manual-key-2026')
    await user.click(within(dialog).getByRole('button', { name: /保存|创建/ }))

    await waitFor(() => expect(created).not.toBeNull())
    expect(created!.key).toBe('my-manual-key-2026')
  })

  it('创建对话框提示留空自动生成', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('脚本A')
    await user.click(screen.getByRole('button', { name: /创建秘钥/ }))
    const dialog = await screen.findByRole('dialog', { name: '创建秘钥' })
    expect(within(dialog).getByText(/留空.*自动生成|自动生成.*sk-/)).toBeInTheDocument()
  })

  it('编辑秘钥: 名称/权限/启用状态可改, 秘钥值不可改', async () => {
    let patched: { id: string; body: Record<string, unknown> } | null = null
    server.use(
      http.patch('/api/keys/:id', async ({ request, params }) => {
        patched = { id: String(params.id), body: (await request.json()) as Record<string, unknown> }
        return HttpResponse.json({ success: true, data: { ...keys[0], enabled: false } })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('脚本A')

    await user.click(screen.getAllByRole('button', { name: /编辑/ })[0])
    const dialog = await screen.findByRole('dialog', { name: '编辑秘钥' })

    // 编辑模式不提供秘钥值输入(不可变)
    expect(within(dialog).queryByLabelText('秘钥')).not.toBeInTheDocument()

    const nameInput = within(dialog).getByLabelText('名称')
    await user.clear(nameInput)
    await user.type(nameInput, '改名脚本')
    await user.click(within(dialog).getByLabelText('删除邮件'))
    await user.click(within(dialog).getByRole('button', { name: /保存/ }))

    await waitFor(() => expect(patched).not.toBeNull())
    expect(patched!.id).toBe('key_1')
    expect(patched!.body).toMatchObject({ name: '改名脚本' })
    expect((patched!.body.permissions as string[]).sort()).toEqual(['inbox.delete', 'inbox.list'])
  })

  it('删除秘钥: 二次确认后调用 DELETE', async () => {
    let deletedId = ''
    server.use(
      http.delete('/api/keys/:id', ({ params }) => {
        deletedId = String(params.id)
        return HttpResponse.json({ success: true, data: { deleted: true } })
      }),
    )
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('脚本A')

    await user.click(screen.getAllByRole('button', { name: /删除/ })[0])
    const confirm = await screen.findByRole('dialog', { name: /删除秘钥/ })
    await user.click(within(confirm).getByRole('button', { name: /确认删除/ }))

    await waitFor(() => expect(deletedId).toBe('key_1'))
  })

  it('空列表显示引导', async () => {
    server.use(
      http.get('/api/keys', () => HttpResponse.json({ success: true, data: { count: 0, keys: [] } })),
    )
    renderPage()
    expect(await screen.findByText(/暂无秘钥|还没有秘钥/)).toBeInTheDocument()
  })

  it('加载失败显示错误', async () => {
    server.use(
      http.get('/api/keys', () =>
        HttpResponse.json({ success: false, code: 'INTERNAL_ERROR', message: '读取失败' }, { status: 500 }),
      ),
    )
    renderPage()
    expect(await screen.findByText('读取失败')).toBeInTheDocument()
  })
})
