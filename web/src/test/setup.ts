import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './server'
import { __resetCache } from '../api/cache'

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
