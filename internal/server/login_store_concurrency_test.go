package server

import (
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

// 同一 session_id 的并发请求必须串行化: 两个并发请求会同时驱动同一个
// hme.Client(authState 无锁读写、对 Apple 双发请求), 双击/网络重试即可触发。
// 登录会话经 store 包装后, 所有方法调用自动互斥。
func TestLoginStoreSerializesConcurrentSessionCalls(t *testing.T) {
	store := newLoginStore()
	var active, maxActive int
	var mu sync.Mutex
	sess := &countingSession{
		onCall: func() {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
		},
	}
	id := store.put("acc_1", sess)

	_, s, err := store.peek(id)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 模拟四个并发请求各自调用同一会话的上游方法。
			_, _ = s.TrustedPhones()
		}()
	}
	wg.Wait()
	if maxActive > 1 {
		t.Fatalf("同一会话并发进入业务调用 %d 次, 必须串行", maxActive)
	}
}

// countingSession 记录并发进入次数。
type countingSession struct {
	fakeLoginSession
	onCall func()
}

func (c *countingSession) TrustedPhones() ([]hme.TrustedPhone, error) {
	c.onCall()
	return nil, nil
}
