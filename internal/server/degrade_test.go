package server

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 回显给客户端的上游错误必须有长度上限, 并保持 UTF-8 完整。
// (capUpstreamReason 只在 summarizeIMAPFailure 内被调用, 后者保证入参非 nil:
// ListInbox 在 poolErr == nil 时已经提前返回。)
func TestCapUpstreamReasonBoundsLength(t *testing.T) {
	short := errors.New("IMAP 登录失败: 授权码无效")
	if got := capUpstreamReason(short); got != short.Error() {
		t.Fatalf("capUpstreamReason(short) = %q, want unchanged", got)
	}

	long := errors.New(strings.Repeat("错", upstreamReasonLimit+50))
	got := capUpstreamReason(long)
	if n := len([]rune(got)); n != upstreamReasonLimit+1 { // +1 为省略号
		t.Fatalf("rune len = %d, want %d (+ellipsis)", n, upstreamReasonLimit+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want trailing ellipsis, got %q", got)
	}
}

// "未配置凭据" 是正常配置(仅用 Cookie), 不应把内部错误原文回显。
func TestSummarizeIMAPFailureClassifiesMissingCreds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no app password", errors.New("账号未设置 App 专用密码"), "账号未配置 IMAP 凭据，改用 Cookie 读取"},
		{"no icloud email", errors.New("账号未设置 iCloud 邮箱 (当前: a@qq.com)"), "账号未配置 IMAP 凭据，改用 Cookie 读取"},
	}
	for _, c := range cases {
		if got := summarizeIMAPFailure(c.err); got != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// 真实故障保留截断后的说明, 且不泄露未截断的长文本
	raw := errors.New("IMAP 登录失败 — 请检查邮箱账号、授权码和服务器地址: " + strings.Repeat("x", 500))
	got := summarizeIMAPFailure(raw)
	if len([]rune(got)) > upstreamReasonLimit+1 {
		t.Fatalf("reason not capped: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "IMAP 登录失败") {
		t.Fatalf("real failure reason lost: %q", got)
	}
}

// 超时预算: 卡住的 IMAP 必须在预算内返回 errIMAPTimeout, 而不是无限等待。
func TestWithIMAPTimeoutExpires(t *testing.T) {
	start := time.Now()
	err := withIMAPTimeout(50*time.Millisecond, func() error {
		time.Sleep(2 * time.Second) // 模拟卡死的 IMAP 读取
		return nil
	})
	if !errors.Is(err, errIMAPTimeout) {
		t.Fatalf("err = %v, want errIMAPTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("withIMAPTimeout blocked for %v, want ~50ms", elapsed)
	}
}

// 预算内完成时原样返回 fn 的结果(含错误)。
func TestWithIMAPTimeoutPassthrough(t *testing.T) {
	sentinel := errors.New("boom")
	if err := withIMAPTimeout(time.Second, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if err := withIMAPTimeout(time.Second, func() error { return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
