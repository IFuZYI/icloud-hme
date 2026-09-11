package auth

import (
	"testing"
	"time"
)

// TestLimiterAllowsUpToMax 验证固定窗口内最多允许 maxFailures 次失败。
func TestLimiterAllowsUpToMax(t *testing.T) {
	now := fixedNow
	l := NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10000)
	key := "203.0.113.10"
	for i := 1; i <= 5; i++ {
		allowed, _ := l.Allow(key)
		if !allowed {
			t.Fatalf("第 %d 次失败应允许", i)
		}
	}
	// 第 6 次应被拒绝,retryAfter 不超过窗口
	allowed, retryAfter := l.Allow(key)
	if allowed {
		t.Fatal("第 6 次失败应被限流")
	}
	if retryAfter <= 0 || retryAfter > 15*time.Minute {
		t.Fatalf("retryAfter 应在 (0, 15m] 内,得到 %v", retryAfter)
	}
}

// TestLimiterWindowReset 验证时钟越过窗口后恢复。
func TestLimiterWindowReset(t *testing.T) {
	now := fixedNow
	l := NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10000)
	key := "203.0.113.11"
	for i := 1; i <= 5; i++ {
		l.Allow(key)
	}
	if allowed, _ := l.Allow(key); allowed {
		t.Fatal("窗口内应被限流")
	}
	// 越过窗口
	now = now.Add(16 * time.Minute)
	if allowed, _ := l.Allow(key); !allowed {
		t.Fatal("越过窗口后应恢复")
	}
}

// TestLimiterSuccessResets 验证调用 Success 后恢复。
func TestLimiterSuccessResets(t *testing.T) {
	now := fixedNow
	l := NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10000)
	key := "203.0.113.12"
	for i := 1; i <= 5; i++ {
		l.Allow(key)
	}
	if allowed, _ := l.Allow(key); allowed {
		t.Fatal("失败达到上限应被限流")
	}
	l.Success(key)
	if allowed, _ := l.Allow(key); !allowed {
		t.Fatal("Success 后应恢复")
	}
}

// TestLimiterIndependentKeys 验证不同 key 互不影响。
func TestLimiterIndependentKeys(t *testing.T) {
	now := fixedNow
	l := NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10000)
	for i := 1; i <= 5; i++ {
		l.Allow("203.0.113.20")
	}
	if allowed, _ := l.Allow("203.0.113.20"); allowed {
		t.Fatal("key A 应被限流")
	}
	if allowed, _ := l.Allow("203.0.113.21"); !allowed {
		t.Fatal("key B 不应受 key A 影响")
	}
}

// TestLimiterMaxKeys 验证达到 key 上限时淘汰最早窗口且不 panic。
func TestLimiterMaxKeys(t *testing.T) {
	now := fixedNow
	l := NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10)
	keys := make([]string, 0, 15)
	for i := 0; i < 15; i++ {
		key := "host-" + string(rune('a'+i))
		keys = append(keys, key)
		l.Allow(key)
		now = now.Add(time.Minute)
	}
	l.mu.Lock()
	count := len(l.failures)
	l.mu.Unlock()
	if count > 10 {
		t.Fatalf("key 数 %d 超过上限 10", count)
	}
	// 最早的 key 应被淘汰,新 key 可用
	if allowed, _ := l.Allow("fresh-host"); !allowed {
		t.Fatal("新 key 应可用")
	}
}
