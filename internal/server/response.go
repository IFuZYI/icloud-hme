// Package server - 统一响应格式与稳定错误码。
package server

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// apiResp 是统一 API 响应。
type apiResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// ok 返回统一成功响应。
func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, apiResp{Success: true, Data: data})
}

// failCode 返回统一失败响应。
//
// 每个失败响应都会记一条 debug 日志(含状态码、错误码、路径与客户端 IP),
// 便于线上排查诸如「莫名跳回登录页」这类由某个 401/403 触发的问题。
func failCode(c *gin.Context, status int, code, message string) {
	slog.Debug("API 返回失败",
		"status", status, "code", code, "message", message,
		"method", c.Request.Method, "path", c.Request.URL.Path, "ip", c.ClientIP())
	c.AbortWithStatusJSON(status, apiResp{Success: false, Code: code, Message: message})
}

// backendFail 把 Backend 错误映射为统一失败响应。
func backendFail(c *gin.Context, err error) {
	be := asBackendError(err)
	failCode(c, be.Status, be.Code, be.Message)
}
