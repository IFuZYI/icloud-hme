import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './server'
import { __resetCache } from '../api/cache'

// @testing-library 的 findBy* 轮询超时默认 1000ms, 不受 vitest 的
// testTimeout/hookTimeout 管辖——高负载(CI 并行/本机多任务)下 1s 轮询会先
// 超时, 表现为 `Unable to find an element with the text` 的假失败(实测在
// 2 核机器叠加 go test 时 1427/1496ms 触发)。提高到 5s: 仍远低于 vitest
// 的 20s 用例上限, 真正的挂死仍会被用例超时捕获。
configure({ asyncUtilTimeout: 5000 })

// jsdom 不提供 ResizeObserver; antd 的 DatePicker/RangePicker 需要它。
if (!('ResizeObserver' in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
  // 清空共享缓存: 避免跨用例复用账号数据造成串扰。
  __resetCache()
})
afterAll(() => server.close())
