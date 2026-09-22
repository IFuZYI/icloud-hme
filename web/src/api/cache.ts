/**
 * 轻量 stale-while-revalidate 请求缓存。
 *
 * 目标: 账号列表等只读数据在多页面(账号/别名/收件箱/任务)间共享,
 * 页面切换时先用缓存瞬时命中, TTL 过期后再拉取, 避免每次导航都重复请求。
 *
 * 只缓存幂等 GET; 任何写操作后由调用方 invalidate() 让下次读取重新拉取。
 * 并发读取通过 inflight Promise 去重, 刷新失败保留旧数据(不抹掉可用缓存)。
 */
import { request } from './client'

interface CacheEntry<T> {
  data?: T
  error?: unknown
  /** 数据写入时刻(ms); 用于判定是否过期。 */
  updatedAt: number
  /** 进行中的请求, 去重并发拉取。 */
  inflight?: Promise<T>
}

const store = new Map<string, CacheEntry<unknown>>()

/** 默认新鲜期: 10 秒内的缓存直接命中, 不发网络。 */
const DEFAULT_TTL = 10_000

/**
 * 拉取并写入缓存(带并发去重)。已有 inflight 时复用同一 Promise。
 */
function fetchInto<T>(key: string, path: string): Promise<T> {
  const existing = store.get(key) as CacheEntry<T> | undefined
  if (existing?.inflight) return existing.inflight

  const inflight = request<T>(path)
    .then((data) => {
      store.set(key, { data, updatedAt: Date.now() })
      return data
    })
    .catch((error) => {
      const prev = store.get(key) as CacheEntry<T> | undefined
      // 保留旧数据(SWR): 刷新失败不该抹掉可用的缓存, 只记录错误。
      store.set(key, { data: prev?.data, error, updatedAt: prev?.updatedAt ?? 0 })
      throw error
    })

  store.set(key, { ...(existing ?? { updatedAt: 0 }), inflight })
  return inflight
}

/** 让某个缓存键失效: 清空数据, 下一次读取会重新拉取。 */
export function invalidate(key: string): void {
  store.delete(key)
}

/**
 * 读取一个缓存键, 命中未过期缓存时直接返回(不发网络), 否则拉取。
 *
 * 供保留了自定义 .then() 初始化逻辑的页面复用: 换页时账号列表瞬时返回,
 * 后续初始化(选中账号/同步 URL 参数)逻辑保持不变。
 */
export function readCached<T>(key: string, path: string, ttl = DEFAULT_TTL): Promise<T> {
  const entry = store.get(key) as CacheEntry<T> | undefined
  if (entry?.data !== undefined && Date.now() - entry.updatedAt < ttl) {
    return Promise.resolve(entry.data)
  }
  return fetchInto<T>(key, path)
}

/** 账号列表缓存键(多页面共享)。 */
export const ACCOUNTS_KEY = 'accounts'

/** 拉取账号列表(带共享缓存)。任何账号写操作后调用 invalidateAccounts()。 */
export function fetchAccounts<T>(): Promise<T> {
  return readCached<T>(ACCOUNTS_KEY, '/api/accounts')
}

/** 账号发生增删改后让缓存失效, 保证下次读取拉到最新数据。 */
export function invalidateAccounts(): void {
  invalidate(ACCOUNTS_KEY)
}

/** 测试辅助: 清空整个缓存。 */
export function __resetCache(): void {
  store.clear()
}
