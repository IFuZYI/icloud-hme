package server

import (
	"testing"
	"time"
)

// TestIMAPTimeoutDefaultsTo30s 验证未经配置时 IMAP 预算的默认值是 30s
// (旧默认 15s)。
func TestIMAPTimeoutDefaultsTo30s(t *testing.T) {
	if imapOverallTimeout != 30*time.Second {
		t.Fatalf("imapOverallTimeout 默认应为 30s, 实际 %v", imapOverallTimeout)
	}
	if imapEnvelopeBudgetFloor != 30*time.Second {
		t.Fatalf("imapEnvelopeBudgetFloor 默认应为 30s, 实际 %v", imapEnvelopeBudgetFloor)
	}
	if got := imapEnvelopeBudget(1); got != 30*time.Second {
		t.Fatalf("小 limit 时信封预算应取下限 30s, 实际 %v", got)
	}
}

// TestIMAPTimeoutFromConfig 验证 Config.IMAPTimeout 生效: 信封下限、正文预算
// 与总预算都跟随该值。
func TestIMAPTimeoutFromConfig(t *testing.T) {
	prevOverall := imapOverallTimeout
	prevFloor := imapEnvelopeBudgetFloor
	t.Cleanup(func() {
		imapOverallTimeout = prevOverall
		imapEnvelopeBudgetFloor = prevFloor
	})

	f := &fakeBackend{}
	newWithBackend(f, Config{IMAPTimeout: 45 * time.Second})

	if imapOverallTimeout != 45*time.Second {
		t.Fatalf("imapOverallTimeout 应为 45s, 实际 %v", imapOverallTimeout)
	}
	if imapEnvelopeBudgetFloor != 45*time.Second {
		t.Fatalf("imapEnvelopeBudgetFloor 应为 45s, 实际 %v", imapEnvelopeBudgetFloor)
	}
	if got := imapEnvelopeBudget(1); got != 45*time.Second {
		t.Fatalf("信封预算下限应为 45s, 实际 %v", got)
	}
}

// TestIMAPTimeoutLargeConfigNotClamped 验证显式配置较大超时时, 上限跟着放大,
// 不会被旧的 45s/60s 硬上限压回去。
func TestIMAPTimeoutLargeConfigNotClamped(t *testing.T) {
	prevOverall := imapOverallTimeout
	prevFloor := imapEnvelopeBudgetFloor
	t.Cleanup(func() {
		imapOverallTimeout = prevOverall
		imapEnvelopeBudgetFloor = prevFloor
	})

	f := &fakeBackend{}
	newWithBackend(f, Config{IMAPTimeout: time.Minute})

	if got := imapEnvelopeBudget(1000); got <= 45*time.Second {
		t.Fatalf("1m 配置下信封预算不应被 45s 压回, 实际 %v", got)
	}
}

// TestIMAPTimeoutZeroConfigResetsPreviousServer 验证前一个 Server 配过超时后,
// 新 Server 未配置时会回到默认 30s(不会继承旧值)。
func TestIMAPTimeoutZeroConfigResetsPreviousServer(t *testing.T) {
	prevOverall := imapOverallTimeout
	prevFloor := imapEnvelopeBudgetFloor
	t.Cleanup(func() {
		imapOverallTimeout = prevOverall
		imapEnvelopeBudgetFloor = prevFloor
	})

	newWithBackend(&fakeBackend{}, Config{IMAPTimeout: 2 * time.Minute})
	newWithBackend(&fakeBackend{}, Config{})

	if imapOverallTimeout != 30*time.Second || imapEnvelopeBudgetFloor != 30*time.Second {
		t.Fatalf("未配置的新实例应回到 30s, 实际 overall=%v floor=%v", imapOverallTimeout, imapEnvelopeBudgetFloor)
	}
}
