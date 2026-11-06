package main

import (
	"testing"
	"time"
)

// TestParseIMAPTimeoutDefaultIs30s 验证未配置时默认 IMAP 超时为 30s
// (从旧的 15s 提升)。
func TestParseIMAPTimeoutDefaultIs30s(t *testing.T) {
	d, err := parseIMAPTimeout("")
	if err != nil {
		t.Fatalf("默认值不应报错: %v", err)
	}
	if d != 30*time.Second {
		t.Fatalf("默认 IMAP 超时应为 30s, 实际 %v", d)
	}
}

// TestParseIMAPTimeoutAcceptsDuration 验证合法 duration 被接受。
func TestParseIMAPTimeoutAcceptsDuration(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"45s", 45 * time.Second},
		{"1m", time.Minute},
		{"2m30s", 150 * time.Second},
		{"10m", 10 * time.Minute},
	} {
		got, err := parseIMAPTimeout(tc.raw)
		if err != nil {
			t.Fatalf("parseIMAPTimeout(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parseIMAPTimeout(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// TestIMAPTimeoutEnvWiring 验证 main 真的读的是 ICLOUD_HME_IMAP_TIMEOUT
// 这个环境变量(名字写错会立刻被这条测试拦住)。
func TestIMAPTimeoutEnvWiring(t *testing.T) {
	t.Setenv("ICLOUD_HME_IMAP_TIMEOUT", "45s")
	d, err := imapTimeoutFromEnv()
	if err != nil {
		t.Fatalf("imapTimeoutFromEnv: %v", err)
	}
	if d != 45*time.Second {
		t.Fatalf("环境变量应生效为 45s, 实际 %v", d)
	}

	t.Setenv("ICLOUD_HME_IMAP_TIMEOUT", "")
	d, err = imapTimeoutFromEnv()
	if err != nil {
		t.Fatalf("未设置时不应报错: %v", err)
	}
	if d != 30*time.Second {
		t.Fatalf("未设置时应为默认 30s, 实际 %v", d)
	}
}

// TestParseIMAPTimeoutRejectsInvalid 验证非法值与越界值被拒绝(而不是静默用默认值)。
func TestParseIMAPTimeoutRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"abc", "30", "0s", "1s", "20m", "-5s"} {
		if _, err := parseIMAPTimeout(raw); err == nil {
			t.Fatalf("parseIMAPTimeout(%q) 应返回错误", raw)
		}
	}
}
