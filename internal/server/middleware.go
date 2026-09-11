// Package server - 安全中间件:请求上限、安全响应头、CSRF 校验。
package server

import (
	"net/http"

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
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "缺少会话")
			return
		}
		token := c.GetHeader("X-CSRF-Token")
		if token == "" || !mgr.ValidateCSRF(sessionID, token) {
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "CSRF 校验失败")
			return
		}
		c.Next()
	}
}
