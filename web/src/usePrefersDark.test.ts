import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { usePrefersDark } from './usePrefersDark'

/** 构造一个可控的 matchMedia mock: 记录监听器, 允许测试手动触发 change。 */
function mockMatchMedia(initialMatches: boolean) {
  const listeners = new Set<() => void>()
  let matches = initialMatches
  const query = {
    get matches() {
      return matches
    },
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_: string, cb: () => void) => listeners.add(cb),
    removeEventListener: (_: string, cb: () => void) => listeners.delete(cb),
  }
  const matchMedia = vi.fn().mockReturnValue(query)
  vi.stubGlobal('matchMedia', matchMedia)
  return {
    matchMedia,
    emit(next: boolean) {
      matches = next
      listeners.forEach((cb) => cb())
    },
    listenerCount: () => listeners.size,
  }
}

describe('usePrefersDark', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('初始为暗色时返回 true, 浅色时返回 false', () => {
    const dark = mockMatchMedia(true)
    const { result, unmount } = renderHook(() => usePrefersDark())
    expect(result.current).toBe(true)
    unmount()

    const light = mockMatchMedia(false)
    const { result: lightResult } = renderHook(() => usePrefersDark())
    expect(lightResult.current).toBe(false)
    expect(light.matchMedia).toHaveBeenCalledWith('(prefers-color-scheme: dark)')
    expect(dark.matchMedia).toHaveBeenCalled()
  })

  it('系统色切换时实时更新, 卸载时移除监听', () => {
    const media = mockMatchMedia(false)
    const { result, unmount } = renderHook(() => usePrefersDark())
    expect(result.current).toBe(false)

    act(() => media.emit(true))
    expect(result.current).toBe(true)

    act(() => media.emit(false))
    expect(result.current).toBe(false)

    expect(media.listenerCount()).toBe(1)
    unmount()
    expect(media.listenerCount()).toBe(0)
  })

  it('无 matchMedia 的环境(旧浏览器/SSR)回退为浅色', () => {
    vi.stubGlobal('matchMedia', undefined)
    const { result } = renderHook(() => usePrefersDark())
    expect(result.current).toBe(false)
  })
})
