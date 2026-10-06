// Package server - 安全中间件:请求上限、安全响应头、CSRF 校验。
package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes 是 JSON 请求体上限。
const maxBodyBytes = 1 << 20 // 1 MiB

// bodyLimitMiddleware 拒绝超过 maxBodyBytes 的请求体。
//
// 所有 API 请求都是小 JSON(最大的是批量别名 200 项的 id 列表),
// 1 MiB 上限对正常使用绰绰有余; 超限直接 413, 避免超大 body 进入解析。
// Content-Length 不可信(可分块传输), 因此同时用 MaxBytesReader 兜底:
// handler 读取超限时得到错误, 这里统一转成 413 响应。
func bodyLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBodyBytes {
			failCode(c, http.StatusRequestEntityTooLarge, "VALIDATION_ERROR", "请求体过大")
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		}
		c.Next()
	}
}

// securityHeaders 是全局安全响应头。
//
// style-src 必须含 'unsafe-inline': antd v6 经 CSS-in-JS 在运行时注入
// <style> 标签, 且 React 的 style={{...}} 内联属性也受 style-src 管辖——
// 只有 'self' 时两者全被拦截, antd 组件(DatePicker 等)会退化成无样式
// 裸元素。script-src 保持 'self'(内联脚本仍被禁止), 风险可控。
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
	"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
}

// requestLogMiddleware 记录每个 HTTP 请求的结果。2xx/3xx 记 debug,
// 4xx 记 info,5xx 记 error;便于按 ICLOUD_HME_LOG_LEVEL 调节详细程度。
//
// 认证相关的 4xx(401/403)在 debug 级别时最有排查价值——例如前端「莫名跳回
// 登录页」通常对应某个 401,打开 debug 即可看到是哪个请求、哪个 IP 触发。
func requestLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"status", status,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"ip", c.ClientIP(),
			"latency_ms", time.Since(start).Milliseconds(),
		}
		switch {
		case status >= http.StatusInternalServerError:
			slog.Error("HTTP 请求", attrs...)
		case status >= http.StatusBadRequest:
			slog.Info("HTTP 请求", attrs...)
		default:
			slog.Debug("HTTP 请求", attrs...)
		}
	}
}

// securityHeadersMiddleware 设置全局安全响应头。
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		for k, v := range securityHeaders {
			c.Header(k, v)
		}
		c.Next()
	}
}

// apiCacheControlMiddleware 给 API 响应设置 no-store。
func apiCacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

// csrfCheck 校验状态变更请求的 CSRF token。
func csrfCheck(mgr *authManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := sessionIDFromCookie(c)
		if sessionID == "" {
			slog.Debug("CSRF 校验失败: 缺少会话",
				"path", c.Request.URL.Path, "method", c.Request.Method, "ip", c.ClientIP())
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "缺少会话")
			return
		}
		token := c.GetHeader("X-CSRF-Token")
		if token == "" || !mgr.ValidateCSRF(sessionID, token) {
			slog.Debug("CSRF 校验失败: token 缺失或不匹配",
				"path", c.Request.URL.Path, "method", c.Request.Method,
				"ip", c.ClientIP(), "has_token", token != "")
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "CSRF 校验失败")
			return
		}
		c.Next()
	}
}
