package server

import (
	"errors"
	"testing"
)

// TestClassifyLoginErrCredentials 验证凭据类错误不再被误报为「会话失效」。
//
// 回归背景(dogfood 实测复现): 错误密码或不存在账号时, Apple 返回 401/403,
// 原实现命中 isSessionError 的 401/403 分支 → 用户看到
// 「iCloud 会话失效,请更新 Cookie」。但登录流程根本不使用 Cookie,
// 该提示把用户引向错误的操作(去更新 Cookie 而非检查密码)。
func TestClassifyLoginErrCredentials(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		wantCode string
	}{
		// 凭据被拒(Apple 对错误密码/不存在账号的两种返回形态)
		{"密码错误(403 明确文案)", "auth complete: 用户名或密码错误", "INVALID_CREDENTIALS"},
		{"complete 401 带 JSON", `auth complete: auth complete 失败: HTTP 401: {"errorCode":-20101}`, "INVALID_CREDENTIALS"},
		{"complete 403 无文案", "auth complete 失败: HTTP 403", "INVALID_CREDENTIALS"},
		{"init 401", "auth init: HTTP 401: unauthorized", "INVALID_CREDENTIALS"},
		// 输入/账号问题
		{"隐私条款", "auth complete: 需要先在 appleid.apple.com 同意隐私条款", "VALIDATION_ERROR"},
		{"账号不存在", "账号不存在: acc_x", "ACCOUNT_NOT_FOUND"},
		{"未设置邮箱", "账号未设置邮箱地址", "VALIDATION_ERROR"},
		// 2FA 流
		{"需要 OTP", "账号启用了双重认证,需要提供 OTP", "OTP_REQUIRED"},
		{"验证码错误", "2FA 验证失败: HTTP 401", "OTP_INVALID"},
		// 收尾阶段(凭据已通过)的故障: 可重试的上游问题, 不误导为密码错
		{"trust 失败", "get trust: trust 失败: HTTP 404", "UPSTREAM_FAILURE"},
		{"web 认证失败", "authenticate web: auth web 失败: HTTP 403 (host=icloud.com)", "UPSTREAM_FAILURE"},
		// 真正的会话问题(登录成功但 Cookie 缺失 / validate 失败)
		{"无会话 Cookie", "登录成功但未获取到会话 Cookie", "UPSTREAM_UNAUTHORIZED"},
		{"validate 401", "HTTP 401: invalid session", "UPSTREAM_UNAUTHORIZED"},
		// 其余上游故障兜底
		{"start 401", "auth start: unexpected status: 401", "UPSTREAM_FAILURE"},
		{"网络故障", "auth federate: 连接失败: timeout", "UPSTREAM_FAILURE"},
		{"503", "auth init: HTTP 503: Service Unavailable", "UPSTREAM_FAILURE"},
	}
	for _, tc := range cases {
		got := classifyLoginErr(errors.New(tc.msg))
		if got.Code != tc.wantCode {
			t.Errorf("%s: classifyLoginErr(%q) = %s (%s), 期望 %s",
				tc.name, tc.msg, got.Code, got.Message, tc.wantCode)
		}
	}
}

// TestClassifyLoginErrWrappedOTPRequired 验证被 fmt.Errorf 包装的
// ErrOTPRequired 也能被 errors.Is 识别(不依赖文案匹配)。
func TestClassifyLoginErrWrappedOTPRequired(t *testing.T) {
	// hme.ErrOTPRequired 的文案是「账号启用了双重认证,需要提供 2FA 验证码」,
	// 与旧分支匹配的「需要提供 OTP」不同——必须用 errors.Is 兜住。
	wrapped := errors.New("账号启用了双重认证,需要提供 2FA 验证码")
	got := classifyLoginErr(wrapped)
	if got.Code != "OTP_REQUIRED" {
		t.Fatalf("wrapped ErrOTPRequired → %s, 期望 OTP_REQUIRED", got.Code)
	}
}

// TestClassifyLoginErrCredentialMessage 验证凭据错误对用户的文案明确指向
// 「邮箱或密码」, 而不是模糊的「稍后重试」或误导的「更新 Cookie」。
func TestClassifyLoginErrCredentialMessage(t *testing.T) {
	got := classifyLoginErr(errors.New("auth complete: 用户名或密码错误"))
	if got.Status != 401 {
		t.Fatalf("状态码 = %d, 期望 401", got.Status)
	}
	if got.Message != "iCloud 邮箱或密码错误" {
		t.Fatalf("文案 = %q, 期望明确指向邮箱或密码", got.Message)
	}
}

// TestIsSessionErrorNarrowed 验证 isSessionError 不再把「认证」一词
// 误判为会话问题——ErrOTPRequired 的文案含「双重认证」, 会误报。
func TestIsSessionErrorNarrowed(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"账号启用了双重认证,需要提供 2FA 验证码", false},
		{"HTTP 421: Misdirected Request", true},
		{"mail 421 session expired", true},
		{"HTTP 502: Bad Gateway", false},
		{"连接失败: timeout", false},
	}
	for _, tc := range cases {
		if got := isSessionError(tc.msg); got != tc.want {
			t.Errorf("isSessionError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
