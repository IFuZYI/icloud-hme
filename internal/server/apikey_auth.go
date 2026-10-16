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
//
// 第二个返回值 present 表示「请求声明要使用秘钥」(头存在, 即使值为空)。
// 调用方据此时不回退会话: 带空秘钥头的请求一律 401, 避免静默降级为
// Cookie 调用(削弱失败关闭语义)。
func apiKeyFromRequest(c *gin.Context) (key string, present bool) {
	// 用 HasHeader 判断存在性(大小写不敏感), GetHeader 读值。
	// 不能直接索引 Request.Header map: 键经过 textproto 规范化(canonical),
	// 手写 "X-API-Key" 与之不匹配。
	if c.Request != nil && hasHeader(c, apiKeyHeader) {
		return strings.TrimSpace(c.GetHeader(apiKeyHeader)), true
	}
	if c.Request != nil && hasHeader(c, "Authorization") {
		auth := c.GetHeader("Authorization")
		// "Bearer <key>": 按第一个空格切分 scheme 与凭据。
		if i := strings.IndexByte(auth, ' '); i > 0 {
			if strings.EqualFold(strings.TrimSpace(auth[:i]), bearerScheme) {
				return strings.TrimSpace(auth[i+1:]), true
			}
		}
		// Authorization 头存在但非 Bearer(或纯 "Bearer"): 也算声明用秘钥。
		if strings.EqualFold(strings.TrimSpace(auth), bearerScheme) {
			return "", true
		}
	}
	return "", false
}

// hasHeader 大小写不敏感地判断请求头是否存在(遍历规范化后的键)。
func hasHeader(c *gin.Context, name string) bool {
	for k := range c.Request.Header {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// requireAuth 双模认证中间件: 秘钥优先, 否则回退会话 Cookie。
//
// 携带秘钥头(即使值无效)时不再回退会话——避免「脚本带着过期秘钥却因
// 浏览器 Cookie 偶然可用而静默通过」的混淆行为, 失败即失败。
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, present := apiKeyFromRequest(c)
		if present {
			// 声明用秘钥(含空值): 一律走秘钥校验, 失败即 401, 不回退会话。
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
		//
		// 例外: 范围受限秘钥不得创建新账号(POST /api/accounts)——新账号不在
		// 任何范围内, 属范围外行为。
		if _, restricted := keyScopeFromContext(c); restricted {
			if c.Request.Method == http.MethodPost && c.FullPath() == "/api/accounts" {
				slog.Debug("范围受限秘钥被拒创建账号", "key_id", key.ID)
				failCode(c, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "API 秘钥无权创建账号")
				return
			}
		}
		// 账号使用范围: 校验请求声明的「每个」账号来源, 任一越界即拒。
		for _, accountID := range s.resourceAccountFor(c) {
			if key.AllowsAccount(accountID) {
				continue
			}
			slog.Debug("秘钥账号范围不足",
				"path", c.Request.URL.Path, "method", c.Request.Method,
				"key_id", key.ID, "account_id", accountID)
			// 任务类路由: 范围外与不存在统一 404, 且响应体必须与 handler
			// 抛出的完全一致(状态码/错误码/消息), 否则消息文本构成存在性 oracle。
			if strings.HasPrefix(c.FullPath(), "/api/alias-tasks/") {
				failCode(c, http.StatusNotFound, "TASK_NOT_FOUND", "alias task not found: 任务不存在")
				return
			}
			failCode(c, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "API 秘钥无权访问该账号")
			return
		}
		c.Next()
	}
}

// resourceAccountFor 解析请求涉及的全部目标账号 ID, 覆盖两类来源:
//
//  1. 资源归属账号(优先)——任务路由(/api/alias-tasks/:id)的 :id 是任务 ID,
//     必须查其真实 AccountID; 请求声明的 account_id 一律忽略, 否则攻击者
//     可用「范围内 account_id + 他账号 task_id」越权(实测 DELETE -> 200)。
//  2. 请求直接携带的账号 ID(query/body account_id, 账号路由 :id)。
//
// 关键: 无 :id 的路由(query/body 携带 account_id)必须校验**每个**来源,
// 不能只取其一——否则「query 范围内 + body 范围外」可越权(handler 用 body,
// 中间件只看 query)。
//
// 返回切片为空: 该请求不针对特定账号(如 /reload), 或资源不存在; 不做范围
// 校验。
func (s *Server) resourceAccountFor(c *gin.Context) []string {
	// 任务路由: 无条件以任务真实归属为准(不接受请求声明的 account_id)。
	if strings.HasPrefix(c.FullPath(), "/api/alias-tasks/") {
		if taskID := strings.TrimSpace(c.Param("id")); taskID != "" {
			if tk, ok := s.task.get(taskID); ok {
				return []string{tk.AccountID}
			}
		}
		// 任务不存在: 返回空交给 handler 404, 不落回 query/body 的
		// 自述账号(否则又给了越权面)。
		return nil
	}
	return accountScopeTargets(c)
}

// accountScopeTargets 收集请求声明的全部账号 ID 来源(去重、去空)。
//
// 顺序: query account_id → JSON body account_id → 账号路由路径 :id。
// 返回切片而非单值: 调用方必须逐一校验(任一越界即拒), 避免多来源不一致
// 造成的旁路。
func accountScopeTargets(c *gin.Context) []string {
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, x := range out {
			if x == v {
				return
			}
		}
		out = append(out, v)
	}
	add(c.Query("account_id"))
	if accountRouteHasPathID(c.FullPath()) {
		add(c.Param("id"))
	}
	add(accountIDFromBody(c))
	return out
}

// keyScopeFromContext 返回当前秘钥的账号范围(白名单)。
//
// 第二个返回值 false 表示「会话请求或空范围」= 不受限(返回全部)。
func keyScopeFromContext(c *gin.Context) ([]string, bool) {
	if c.GetString(ctxAuthKind) != authKindAPIKey {
		return nil, false
	}
	key, ok := c.MustGet(ctxAPIKey).(apikey.Key)
	if !ok || len(key.AccountIDs) == 0 {
		return nil, false
	}
	return key.AccountIDs, true
}

// filterTasksByKeyScope 按秘钥账号范围过滤任务列表(会话或空范围返回原列表)。
func filterTasksByKeyScope(c *gin.Context, tasks []AliasTask) []AliasTask {
	scope, restricted := keyScopeFromContext(c)
	if !restricted {
		return tasks
	}
	out := make([]AliasTask, 0, len(tasks))
	for _, t := range tasks {
		if containsString(scope, t.AccountID) {
			out = append(out, t)
		}
	}
	return out
}

// filterTaskLogsByKeyScope 按秘钥账号范围过滤日志(经 TaskID → 任务归属账号)。
func filterTaskLogsByKeyScope(c *gin.Context, s *Server, logs []AliasTaskLog) []AliasTaskLog {
	scope, restricted := keyScopeFromContext(c)
	if !restricted {
		return logs
	}
	out := make([]AliasTaskLog, 0, len(logs))
	for _, l := range logs {
		tk, ok := s.task.get(l.TaskID)
		if !ok {
			continue // 归属未知: 失败关闭, 不返回
		}
		if containsString(scope, tk.AccountID) {
			out = append(out, l)
		}
	}
	return out
}

// containsString 判断列表中是否含目标值。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// accountIDFromBody 尝试从 JSON body 读取 account_id, 不消费请求体
// (读取后原样写回, 供后续 handler 再次绑定)。
//
// 不依赖 Content-Type: gin 的 ShouldBindJSON 忽略 Content-Type 照常绑定
// JSON, 若此处按 Content-Type 跳过提取, 攻击者可用 text/plain 携带 JSON
// body 绕过账号范围校验(实测 403->200 越权)。只要 body 能被解析出
// account_id 就必须校验。
func accountIDFromBody(c *gin.Context) string {
	if c.Request.Body == nil {
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
