package server

import (
	"sync/atomic"
	"testing"
	"time"
)

// ====================================================================
// 并发回源合并(singleflight)的 panic 语义: 条目必须回收, panic 必须重放
// ====================================================================
//
// 背景(独立审查实测): fn panic 时若不删除条目、不关闭 done, 该键会永久
// 残留一个永不关闭的 done 通道——之后所有同键调用 `<-call.done` 永久阻塞,
// 该查询键彻底卡死(只能重启进程)。
//
// 契约:
//  1. 无论 fn 正常返回还是 panic, flight 条目都被删除、done 都被关闭;
//  2. leader(执行 fn 的调用方)的 panic 继续向 leader 传播;
//  3. 等待者经 done 建立 happens-before 后重放同一 panic, 不得静默返回零值。
//
// 修复前本文件两个测试均失败(RED): 第二次同键调用/waiter 永久阻塞,
// 由 2 秒超时保护接住(测试不会真的挂死)。

// panic 后同键条目必须被回收, 后续同键调用必须能正常完成。
// 修复前: 第二次调用卡在 <-call.done(残留的未关闭通道)直到超时。
func TestFlightGroupPanicDoesNotWedgeKey(t *testing.T) {
	var g flightGroup

	// leader: fn panic。leader 侧的 panic 必须继续传播(测试用 recover 接住)。
	recovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()
		_, _ = g.do("k", func() (InboxResult, error) {
			panic("上游回源 panic")
		})
	}()
	if !recovered {
		t.Fatal("leader 的 panic 必须继续向 leader 传播, 不得被吞掉")
	}

	// 条目必须已删除(修复前: 残留条目 → 该键永久卡死)。
	g.mu.Lock()
	n := len(g.calls)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("panic 后 flight 条目应被删除, 实际残留 %d 条", n)
	}

	// 第二次同键调用: 必须在 2 秒内完成并返回正确值。
	// 修复前: 此处永久阻塞, 由超时保护判定失败。
	type outcome struct {
		v   InboxResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := g.do("k", func() (InboxResult, error) {
			return InboxResult{Total: 7}, nil
		})
		done <- outcome{v: v, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("panic 后同键第二次调用 err = %v, want nil", got.err)
		}
		if got.v.Total != 7 {
			t.Fatalf("panic 后同键第二次调用 Total = %d, want 7", got.v.Total)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic 后同键第二次调用被永久阻塞: panic 泄漏了未关闭的 done")
	}
}

// waiter 必须重放 leader 的 panic(非 nil), 且不得阻塞。
// 修复前: waiter 卡在 <-call.done 直到超时。
func TestFlightGroupPanicPropagatesToWaiters(t *testing.T) {
	var g flightGroup

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	// leader 的 fn: 第一次调用阻塞在 release 上(给 waiter 留出加入窗口),
	// 放行后 panic; 若 waiter 未加入而成为新 leader(异常路径), 则正常返回,
	// 测试据 calls 与 waiter 的 recover 结果判定未重放。
	fn := func() (InboxResult, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			panic("上游回源 panic: 连接被重置")
		}
		return InboxResult{Total: 99}, nil
	}

	var leaderPanic any
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		defer func() { leaderPanic = recover() }()
		_, _ = g.do("k", fn)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader 未在 2 秒内进入 fn")
	}

	// waiter: 同键调用, 应等到 leader 结束后重放其 panic。
	waiterReady := make(chan struct{})
	var waiterPanic any
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		defer func() { waiterPanic = recover() }()
		close(waiterReady)
		_, _ = g.do("k", fn)
	}()
	<-waiterReady
	// 等 waiter 真正进入等待(join 已存在的 flight 条目)后再放行 leader,
	// 保证本测试断言的是「等待者重放」而非「新 leader 自己执行」。
	time.Sleep(100 * time.Millisecond)
	close(release)

	select {
	case <-waiterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter 未在 2 秒内返回: panic 未关闭 done, 等待者被永久阻塞")
	}
	<-leaderDone

	if leaderPanic == nil {
		t.Fatal("leader 的 panic 必须继续传播, 不得被吞掉")
	}
	if waiterPanic == nil {
		t.Fatal("waiter 必须重放 leader 的 panic, 不得静默返回零值/nil error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("同键并发只应执行一次 fn, 实际执行 %d 次", got)
	}
}
