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
	// panicked/panicVal 记录 fn 的 panic: 等待者经 close(done) 建立的
	// happens-before 读到后重放同一 panic, 不得静默返回零值。两者都必须在
	// close(done) 之前写入。
	panicked bool
	panicVal any
}

// do 以 key 合并并发调用; 同键已在进行中时等待其完成并共享结果。
//
// 结果缓存由 fn 自行负责(本函数只保证「只执行一次」)。fn 的返回值对
// 所有等待者可见: 值在 close(done) 前写入, 等待者经 channel 建立
// happens-before。
//
// fn panic 时同样必须删除条目并 close(done)——panic 不得泄漏未关闭的
// done: 否则该键会永久残留一个永不关闭的通道, 之后所有同键调用在
// <-call.done 上永久阻塞(只能重启进程)。leader 的 panic 继续向 leader
// 传播; 等待者读到 done 关闭后重放同一 panic。
func (g *flightGroup) do(key string, fn func() (InboxResult, error)) (InboxResult, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*inboxFlight)
	}
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		// panicked/panicVal 在 close(done) 前写入, 此处已建立 happens-before。
		if call.panicked {
			panic(call.panicVal)
		}
		return call.value, call.err
	}
	call := &inboxFlight{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	// 内层匿名函数 + recover: 捕获 fn 的 panic 存入 flight, 保证收尾
	// (delete + close(done)) 在 panic 路径上同样执行。
	func() {
		defer func() {
			if r := recover(); r != nil {
				call.panicked = true
				call.panicVal = r
			}
		}()
		call.value, call.err = fn()
	}()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)

	// leader 的 panic 继续传播(不得吞掉)。
	if call.panicked {
		panic(call.panicVal)
	}
	return call.value, call.err
}
