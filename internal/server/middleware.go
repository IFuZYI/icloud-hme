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

// securityHeaders 是全局安全响应头。
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
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
