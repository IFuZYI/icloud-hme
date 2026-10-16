import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, expect, it } from 'vitest'
import InboxPage from './InboxPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { chooseOption } from '../test/selectMenu'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary, InboxResult } from '../api/types'

// ====================================================================
// 收件箱并发交互: 被让位(abort)的请求不得清掉新请求的忙碌态
// ====================================================================
//
// 回归背景(用户实测): 刷新进行中触发新请求(改筛选/点查询)会 abort 旧请求。
// 旧请求的 finally 无条件 setRefreshing(false), 把新请求正在使用的忙碌态
// 清掉 → 按钮提前恢复可点 → 用户以为没反应而重复点击 → 反复中止在飞请求。
// 服务端不感知客户端断连(每次中止仍会跑完一次完整 IMAP 读取), 白白浪费
// 并可能触发账号风控。
//
// 契约:
//   1. 忙碌态只由「未被让位的最新请求」收尾清除;
//   2. 忙碌期间「查询」「加载更多」按钮禁用, 不叠加新请求。

const accounts: AccountSummary[] = [
  {
    id: 'acc_1',
    name: '主号',
    real_email: 'a@example.com',
    icloud_email: 'a@icloud.com',
    host: 'icloud.com',
    status: 'active',
    alias_total: 2,
    alias_active: 2,
    has_cookies: true,
    has_app_password: true,
    has_proxy: false,
    last_validated: '2026-08-04T09:00:00+08:00',
    created_at: '2026-08-01T09:00:00+08:00',
  },
]

const inboxResult: InboxResult = {
  account_id: 'acc_1',
  alias: '',
  count: 1,
  total: 1,
  offset: 0,
  method: 'imap',
  messages: [
    {
      id: '1',
      from: 'sender@example.com',
      to: 'alpha@icloud.com',
      subject: '主题一',
      date: '2026-08-04T10:00:00+08:00',
      preview: '预览内容',
    },
  ],
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/inbox']}>
      <Routes>
        <Route
          path="/inbox"
          element={
            <ToastProvider>
              <InboxPage />
            </ToastProvider>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  setCSRFToken('csrf-test')
  server.resetHandlers()
  server.use(
    http.post('/api/inbox/previews', async ({ request }) => {
      const body = (await request.json()) as { ids: string[] }
      const previews = Object.fromEntries(body.ids.map((id) => [id, `预览 ${id}`]))
      return HttpResponse.json({ success: true, data: previews })
    }),
  )
})

it('刷新进行中修改筛选: 新请求期间忙碌态保持, 不被旧请求收尾清掉', async () => {
  let calls = 0
  let release3: (() => void) | undefined
  server.use(
    http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    http.get('/api/inbox', () => {
      calls += 1
      if (calls === 1) return HttpResponse.json({ success: true, data: inboxResult })
      if (calls === 2) {
        // 刷新的请求: 挂起(随后会被新请求 abort 让位)
        return new Promise<Response>(() => {})
      }
      // 第 3 个请求(筛选变化触发): 挂起直到 release3
      return new Promise<Response>((resolve) => {
        release3 = () =>
          resolve(
            HttpResponse.json({
              success: true,
              data: {
                ...inboxResult,
                messages: [{ ...inboxResult.messages[0], id: '2', subject: '筛选后主题' }],
              },
            }),
          )
      })
    }),
  )
  renderPage()
  await screen.findByText('主题一')
  const user = userEvent.setup()

  // 1) 点查询 → 请求 2 挂起, 查询按钮进入忙碌态
  const query = screen.getByRole('button', { name: '查询' })
  await user.click(query)
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).toBeDisabled())

  // 2) 查询进行中修改每页数量 → 请求 2 被 abort 让位, 请求 3 发起
  await chooseOption(user, /每页数量/, '50')
  await waitFor(() => expect(calls).toBe(3))

  // 3) 请求 3 仍在飞行: 查询按钮必须保持忙碌(旧请求的收尾不得清掉新请求的忙碌态)
  await new Promise((r) => setTimeout(r, 50))
  expect(screen.getByRole('button', { name: '查询' })).toBeDisabled()

  // 4) 请求 3 返回后: 忙碌态恢复, 展示新数据(不卡死、不残留旧态)
  release3?.()
  await screen.findByText('筛选后主题')
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).not.toBeDisabled())
})

it('查询进行中: 加载更多按钮禁用, 不叠加新请求', async () => {
  let calls = 0
  let releaseRefresh: (() => void) | undefined
  server.use(
    http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    http.get('/api/inbox', () => {
      calls += 1
      if (calls === 1) {
        // total=2 > messages=1: 让「加载更多」按钮可见
        return HttpResponse.json({ success: true, data: { ...inboxResult, total: 2 } })
      }
      // 刷新请求: 挂起直到 releaseRefresh
      return new Promise<Response>((resolve) => {
        releaseRefresh = () => resolve(HttpResponse.json({ success: true, data: { ...inboxResult, total: 2 } }))
      })
    }),
  )
  renderPage()
  await screen.findByText('主题一')
  const user = userEvent.setup()

  const query = screen.getByRole('button', { name: /查询/ })
  const loadMoreBtn = screen.getByRole('button', { name: /加载更多/ })
  // 空闲时可点
  expect(query).not.toBeDisabled()
  expect(loadMoreBtn).not.toBeDisabled()

  // 点查询 → 请求挂起, 查询按钮忙碌
  await user.click(query)
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).toBeDisabled())

  // 忙碌期间: 加载更多必须禁用(避免叠加中止在飞请求、分页边界错位)
  expect(loadMoreBtn).toBeDisabled()

  // 返回后: 全部恢复可点(不永久卡死)
  releaseRefresh?.()
  await waitFor(() => expect(screen.getByRole('button', { name: /查询/ })).not.toBeDisabled())
  expect(screen.getByRole('button', { name: /加载更多/ })).not.toBeDisabled()
})

it('加载更多进行中: 查询按钮禁用, 不叠加新请求', async () => {
  let calls = 0
  let releaseMore: (() => void) | undefined
  server.use(
    http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    http.get('/api/inbox', ({ request }) => {
      calls += 1
      const offset = new URL(request.url).searchParams.get('offset')
      if (calls === 1) {
        return HttpResponse.json({ success: true, data: { ...inboxResult, total: 2 } })
      }
      // 第二页请求(加载更多): 挂起直到 releaseMore
      return new Promise<Response>((resolve) => {
        releaseMore = () =>
          resolve(
            HttpResponse.json({
              success: true,
              data: {
                ...inboxResult,
                total: 2,
                offset: Number(offset),
                messages: [{ ...inboxResult.messages[0], id: '2', subject: '第二页' }],
              },
            }),
          )
      })
    }),
  )
  renderPage()
  await screen.findByText('主题一')
  const user = userEvent.setup()

  // 点「加载更多」→ 请求挂起(按钮的可访问名来自 aria-label="加载更多邮件")
  await user.click(screen.getByRole('button', { name: /加载更多邮件/ }))
  await waitFor(() => expect(screen.getByRole('button', { name: /加载更多邮件/ })).toBeDisabled())

  // 加载更多进行中: 查询必须禁用(统一忙碌门禁)
  expect(screen.getByRole('button', { name: /查询/ })).toBeDisabled()

  // 返回后恢复
  releaseMore?.()
  await screen.findByText('第二页')
  await waitFor(() => expect(screen.getByRole('button', { name: /查询/ })).not.toBeDisabled())
})
