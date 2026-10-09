// Package server - 上游只读响应的 TTL 缓存。
//
// 收件箱/别名列表等数据「高频读取、低频变化」: 用户反复切页、切账号、
// 切换筛选都会触发对上游(IMAP / iCloud)的重复读取。对外部收件邮箱
// (163 等)而言, 短时间内的重复 TLS+LOGIN+扫箱是风控/封禁的典型触发信号;
// 主流 IMAP 客户端同样会在本地做短周期缓存而非每次点击都回源。
//
// 语义: 读路径命中未过期缓存即返回; refresh=1 由调用方绕过读取;
// 写操作(删除邮件/增删改别名)后由调用方 invalidate 对应前缀。
package server

import (
	"strings"
	"sync"
	"time"
)

// responseCache 是带 TTL 的进程内只读响应缓存(并发安全)。
type responseCache struct {
	mu    sync.Mutex
	items map[string]responseCacheEntry
}

type responseCacheEntry struct {
	data      any
	expiresAt time.Time
}

func newResponseCache() *responseCache {
	return &responseCache{items: make(map[string]responseCacheEntry)}
}

// get 读取未过期的缓存值; 过期项顺手清除, 防止无界增长。
func (c *responseCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.items, key)
		return nil, false
	}
	return entry.data, true
}

// set 写入缓存值。
func (c *responseCache) set(key string, data any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = responseCacheEntry{data: data, expiresAt: time.Now().Add(ttl)}
}

// invalidate 按前缀清除缓存项(如某账号的全部收件箱结果)。
func (c *responseCache) invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.items {
		if strings.HasPrefix(key, prefix) {
			delete(c.items, key)
		}
	}
}

// reset 清空全部缓存(配置整体重载后调用)。
func (c *responseCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]responseCacheEntry)
}
