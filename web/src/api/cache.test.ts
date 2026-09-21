import { http, HttpResponse } from 'msw'
import { describe, expect, it, beforeEach } from 'vitest'
import { server } from '../test/server'
import { fetchAccounts, invalidateAccounts, readCached, invalidate, __resetCache } from './cache'

describe('api cache', () => {
  beforeEach(() => {
    __resetCache()
    server.resetHandlers()
  })

  it('命中未过期缓存时不再发起网络请求', async () => {
    let hits = 0
    server.use(
      http.get('/api/accounts', () => {
        hits += 1
        return HttpResponse.json({ success: true, data: [{ id: 'acc_1' }] })
      }),
    )
    const first = await fetchAccounts<Array<{ id: string }>>()
    expect(first[0].id).toBe('acc_1')
    // 第二次读取应命中缓存, 不增加网络请求
    await fetchAccounts<Array<{ id: string }>>()
    expect(hits).toBe(1)
  })

  it('失效后重新拉取', async () => {
    let hits = 0
    server.use(
      http.get('/api/accounts', () => {
        hits += 1
        return HttpResponse.json({ success: true, data: [] })
      }),
    )
    await fetchAccounts()
    invalidateAccounts()
    await fetchAccounts()
    expect(hits).toBe(2)
  })

  it('并发读取去重为单次请求', async () => {
    let hits = 0
    server.use(
      http.get('/api/data', () => {
        hits += 1
        return HttpResponse.json({ success: true, data: { ok: true } })
      }),
    )
    const [a, b] = await Promise.all([
      readCached<{ ok: boolean }>('data', '/api/data'),
      readCached<{ ok: boolean }>('data', '/api/data'),
    ])
    expect(a.ok && b.ok).toBe(true)
    expect(hits).toBe(1)
    invalidate('data')
  })

  it('刷新失败保留旧缓存', async () => {
    let call = 0
    server.use(
      http.get('/api/data', () => {
        call += 1
        if (call === 1) return HttpResponse.json({ success: true, data: { v: 1 } })
        return HttpResponse.json({ success: false, code: 'X', message: 'boom' }, { status: 500 })
      }),
    )
    const good = await readCached<{ v: number }>('data', '/api/data', 0)
    expect(good.v).toBe(1)
    // ttl=0 强制刷新, 第二次失败, 但旧数据仍可被 readCached(带正常 ttl) 命中
    await expect(readCached<{ v: number }>('data', '/api/data', 0)).rejects.toThrow()
    const stale = await readCached<{ v: number }>('data', '/api/data', 60_000)
    expect(stale.v).toBe(1)
  })
})
