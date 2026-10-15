// 并发回源合并(singleflight)。
//
// TTL 过期瞬间的并发请求(多标签页/前端重试/自动加载)会各自回源, 对外部
// 收件邮箱(163 等)形成 N 倍扫箱——风控/封禁的典型触发信号(见 cache.go
// 顶部说明)。本文件让同一查询键的并发请求只触发一次上游读取, 其余调用方
// 等待并共享结果。
package server

import "sync"

// flightGroup 按 key 合并并发调用: 同键的并发 do 只执行一次 fn。
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*inboxFlight
}

// inboxFlight 是一次进行中/刚完成的回源调用。
type inboxFlight struct {
	done  chan struct{}
	value InboxResult
	err   error
}

// do 以 key 合并并发调用; 同键已在进行中时等待其完成并共享结果。
//
// 结果缓存由 fn 自行负责(本函数只保证「只执行一次」)。fn 的返回值对
// 所有等待者可见: 值在 close(done) 前写入, 等待者经 channel 建立
// happens-before。
func (g *flightGroup) do(key string, fn func() (InboxResult, error)) (InboxResult, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*inboxFlight)
	}
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.value, call.err
	}
	call := &inboxFlight{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.value, call.err = fn()
	close(call.done)

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	return call.value, call.err
}
