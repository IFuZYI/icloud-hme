// Package server - 双模认证: 管理员会话 或 API 秘钥。
//
// 会话路径(浏览器)行为完全不变: Cookie hme_session + 写操作 CSRF。
// 秘钥路径(脚本)以 X-API-Key(或 Authorization: Bearer)鉴权, 天然无
// Cookie 语义, 因此豁免 CSRF; 每个秘钥按 permissionCatalog 独立授权。
//
// 失败关闭:
//   - 无效/停用秘钥 → 401 INVALID_API_KEY(与未知秘钥同文案, 不泄露存在性);
//   - 权限不足 → 403 PERMISSION_DENIED;
//   - 仅限会话的路由(sessionOnlyRoutes)对秘钥一律 403。
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/apikey"
)

const (
	// ctxAuthKind 记录本次请求的认证方式(session/apikey), 供 csrfCheck 与
	// 审计日志读取。
	ctxAuthKind = "auth_kind"
	// ctxAPIKey 存放校验通过的秘钥记录(权限判定用)。
	ctxAPIKey = "api_key"

	authKindSession = "session"
	authKindAPIKey  = "apikey"

	// apiKeyHeader 是秘钥请求头(首选)。
	apiKeyHeader = "X-API-Key"
	// bearerScheme 是 Authorization 头的 scheme(RFC 7235: 大小写不敏感)。
	bearerScheme = "bearer"
)

// apiKeyFromRequest 提取请求中的秘钥: X-API-Key 优先, 其次 Authorization: Bearer。
// scheme 大小写不敏感(RFC 7235), 兼容各类客户端发出的 "bearer"/"BEARER"。
func apiKeyFromRequest(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader(apiKeyHeader)); v != "" {
		return v
	}
	auth := c.GetHeader("Authorization")
	// "Bearer <key>": 按第一个空格切分 scheme 与凭据。
	if i := strings.IndexByte(auth, ' '); i > 0 {
		if strings.EqualFold(strings.TrimSpace(auth[:i]), bearerScheme) {
			return strings.TrimSpace(auth[i+1:])
		}
	}
	return ""
}

// requireAuth 双模认证中间件: 秘钥优先, 否则回退会话 Cookie。
//
// 携带秘钥头(即使值无效)时不再回退会话——避免「脚本带着过期秘钥却因
// 浏览器 Cookie 偶然可用而静默通过」的混淆行为, 失败即失败。
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if raw := apiKeyFromRequest(c); raw != "" {
			key, ok := s.keys.Validate(raw, c.ClientIP())
			if !ok {
				slog.Debug("秘钥鉴权失败",
					"path", c.Request.URL.Path, "method", c.Request.Method, "ip", c.ClientIP())
				failCode(c, http.StatusUnauthorized, "INVALID_API_KEY", "API 秘钥无效或已停用")
				return
			}
			c.Set(ctxAuthKind, authKindAPIKey)
			c.Set(ctxAPIKey, key)
			c.Next()
			return
		}

		// 会话路径: 与旧 requireSession 行为一致。
		sessionID := sessionIDFromCookie(c)
		if sessionID == "" {
			slog.Debug("会话校验失败: 请求缺少会话 Cookie",
				"path", c.Request.URL.Path, "method", c.Request.Method, "ip", c.ClientIP())
			failCode(c, http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录")
			return
		}
		if _, ok := s.auth.Validate(sessionID); !ok {
			slog.Debug("会话校验失败: 会话不存在或已过期",
				"path", c.Request.URL.Path, "method", c.Request.Method, "ip", c.ClientIP())
			failCode(c, http.StatusUnauthorized, "AUTH_REQUIRED", "会话已失效,请重新登录")
			return
		}
		c.Set(ctxAuthKind, authKindSession)
		c.Set("session_id", sessionID)
		c.Next()
	}
}

// requirePermission 对秘钥请求做权限判定与账号范围判定;
// 会话请求直接放行(管理员拥有全部权限)。
//
// 判定依据是 gin 解析出的路由模板(c.FullPath()), 不是原始 URL——
// 避免路径穿越/参数混淆绕过权限表。
func (s *Server) requirePermission() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString(ctxAuthKind) != authKindAPIKey {
			c.Next()
			return
		}
		key, _ := c.MustGet(ctxAPIKey).(apikey.Key)

		required, ok := routePermissionFor(c.Request.Method, c.FullPath())
		if !ok {
			// 仅限会话的路由(秘钥管理/退出登录等): 秘钥一律拒绝。
			slog.Debug("秘钥访问仅限会话的路由被拒",
				"path", c.Request.URL.Path, "method", c.Request.Method, "key_id", key.ID)
			failCode(c, http.StatusForbidden, "PERMISSION_DENIED", "该接口仅限管理员会话访问")
			return
		}
		if !key.HasPermission(required) {
			slog.Debug("秘钥权限不足",
				"path", c.Request.URL.Path, "method", c.Request.Method,
				"key_id", key.ID, "required", required)
			failCode(c, http.StatusForbidden, "PERMISSION_DENIED", "API 秘钥权限不足")
			return
		}

		// 账号使用范围: 秘钥限定了账号白名单时, 请求目标账号必须在范围内。
		// 空范围 = 全部账号放行(向后兼容); 未限定账号的接口(如 /reload)
		// 不触发本检查。
		if accountID := accountScopeTarget(c); accountID != "" && !key.AllowsAccount(accountID) {
			slog.Debug("秘钥账号范围不足",
				"path", c.Request.URL.Path, "method", c.Request.Method,
				"key_id", key.ID, "account_id", accountID)
			failCode(c, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "API 秘钥无权访问该账号")
			return
		}
		c.Next()
	}
}

// accountScopeTarget 提取请求要访问的目标账号 ID(失败关闭时返回 "")。
//
// 优先级: query account_id > JSON body account_id > 路径 :id(仅账号路由)。
// 返回 "": 该请求不针对特定账号(如 /reload), 或无法确定——此时不做范围
// 校验(由下方 scopeHasAccountTarget 保证「账号路由必有目标」由测试钉住)。
func accountScopeTarget(c *gin.Context) string {
	if v := strings.TrimSpace(c.Query("account_id")); v != "" {
		return v
	}
	if accountRouteHasPathID(c.FullPath()) {
		if v := strings.TrimSpace(c.Param("id")); v != "" {
			return v
		}
	}
	return accountIDFromBody(c)
}

// accountIDFromBody 尝试从 JSON body 读取 account_id, 不消费请求体
// (读取后原样写回, 供后续 handler 再次绑定)。
func accountIDFromBody(c *gin.Context) string {
	if c.Request.Body == nil || !strings.Contains(c.GetHeader("Content-Type"), "application/json") {
		return ""
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return ""
	}
	// 无论解析结果如何都还原 body, 避免影响 handler 的 ShouldBindJSON。
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var probe struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.AccountID)
}

// accountRouteHasPathID 判断路由模板的 :id 是否是账号 ID。
//
// 仅账号路由(/api/accounts/:id...)的 :id 是 account_id;
// /alias-tasks/:id、/aliases/:id 的 :id 是任务/别名 ID, 不在此列——
// 这些资源的账号归属另行判定(见 auto_task / 别名作用域), 不在此处误判。
func accountRouteHasPathID(fullPath string) bool {
	return strings.HasPrefix(fullPath, "/api/accounts/")
}
