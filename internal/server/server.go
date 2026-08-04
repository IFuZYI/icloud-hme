// Package server 提供 HTTP API,基于 Gin。
//
// 两个核心接口:
//
//	POST /api/create  — 在指定账号下创建一个 Hide My Email 别名
//	GET  /api/inbox   — 读取指定账号(或指定别名)收到的邮件
//
// 辅助接口(用于多账号管理):账号增删查、别名列表、设置 App 密码。
//
// 安全模型:除 /api/auth/login 与 /api/auth/session 外,所有 /api 路由都需要
// 管理员会话;非 GET/HEAD/OPTIONS 请求还需校验 CSRF。
package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/account"
	"icloud-hme/internal/auth"
)

// Config 是 Server 的启动配置。
type Config struct {
	Debug         bool
	AdminPassword string
	SessionTTL    time.Duration
	SecureCookie  bool
}

// Server 封装 Gin 引擎、账号后端与认证。
type Server struct {
	be      Backend
	auth    *auth.Manager
	limiter *auth.Limiter
	cfg     Config
	r       *gin.Engine
}

// New 创建 Server。mgr 为账号管理器,cfg 为安全配置。
func New(mgr *account.Manager, cfg Config) (*Server, error) {
	if _, err := auth.NewManager(auth.Options{
		Password: cfg.AdminPassword,
		TTL:      cfg.SessionTTL,
	}); err != nil {
		return nil, err
	}
	return newWithBackend(&managerBackend{mgr: mgr}, cfg), nil
}

// newWithBackend 创建 Server 并注入 Backend(测试使用内存 fake)。
func newWithBackend(be Backend, cfg Config) *Server {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	s := &Server{
		be:      be,
		limiter: auth.NewLimiter(nil, 15*time.Minute, 5, 10000),
		cfg:     cfg,
	}
	s.auth, _ = auth.NewManager(auth.Options{
		Password: cfg.AdminPassword,
		TTL:      cfg.SessionTTL,
	})
	s.r = gin.New()
	s.r.Use(gin.Logger(), gin.Recovery(), securityHeadersMiddleware())
	// 不信任任意代理头,登录限流使用真实连接 IP
	_ = s.r.SetTrustedProxies(nil)
	s.register()
	return s
}

// Run 启动 HTTP 服务。
func (s *Server) Run(addr string) error {
	return s.r.Run(addr)
}

// Handler 返回底层 gin 引擎(便于测试)。
func (s *Server) Handler() http.Handler { return s.r }

func (s *Server) register() {
	api := s.r.Group("/api")
	api.Use(apiCacheControlMiddleware())
	{
		// ===== 认证(公开) =====
		api.POST("/auth/login", s.handleLogin)
		api.GET("/auth/session", s.handleSession)

		// ===== 受保护路由:统一 requireSession =====
		authed := api.Group("")
		authed.Use(requireSession(s.auth))
		{
			authed.POST("/auth/logout", csrfCheck(s.auth), s.handleLogout)

			// ===== 账号管理 =====
			authed.GET("/accounts", s.listAccountsHandler)
			authed.POST("/accounts", csrfCheck(s.auth), s.addAccountHandler)
			authed.PATCH("/accounts/:id", csrfCheck(s.auth), s.updateAccountHandler)
			authed.PUT("/accounts/:id/proxy", csrfCheck(s.auth), s.updateProxyHandler)
			authed.PUT("/accounts/:id/cookies", csrfCheck(s.auth), s.updateCookiesHandler)
			authed.POST("/accounts/:id/password", csrfCheck(s.auth), s.setAppPasswordHandler)
			authed.POST("/accounts/:id/login", csrfCheck(s.auth), s.loginAccountHandler)
			authed.DELETE("/accounts/:id", csrfCheck(s.auth), s.removeAccountHandler)

			// ===== 核心接口 1: 创建邮箱 =====
			authed.POST("/create", csrfCheck(s.auth), s.createAliasHandler)

			// ===== 核心接口 2: 读取邮件 =====
			authed.GET("/inbox", s.listInboxHandler)

			// ===== 别名管理 =====
			authed.GET("/aliases", s.listAliasesHandler)
			authed.POST("/aliases/:id/deactivate", csrfCheck(s.auth), s.deactivateAliasHandler)
			authed.POST("/aliases/:id/reactivate", csrfCheck(s.auth), s.reactivateAliasHandler)
			authed.DELETE("/aliases/:id", csrfCheck(s.auth), s.deleteAliasHandler)

			// ===== 系统 =====
			authed.POST("/reload", csrfCheck(s.auth), s.reloadConfigHandler)
		}
	}
	// API 404 返回 JSON,绝不让 NoRoute 把拼错的 API 路径变成 HTML
	s.r.NoRoute(func(c *gin.Context) {
		failCode(c, http.StatusNotFound, "VALIDATION_ERROR", "接口不存在")
	})
}

// ====================================================================
// 核心接口 1: 创建邮箱
//   POST /api/create
//   body: {"account_id": "acc_xxx", "label": "可选标签"}
//   返回: 新创建的 HME 邮箱地址
// ====================================================================

type createAliasReq struct {
	AccountID string `json:"account_id"`
	Label     string `json:"label"`
}

func (s *Server) createAliasHandler(c *gin.Context) {
	var req createAliasReq
	if err := c.ShouldBindJSON(&req); err != nil || req.AccountID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: account_id 必填")
		return
	}
	if len([]rune(req.Label)) > 200 {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: label 最长 200 字符")
		return
	}

	result, err := s.be.CreateAlias(req.AccountID, req.Label)
	if err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{
		"email":      result.Email,
		"label":      result.Label,
		"created_at": result.CreatedAt,
		"account_id": req.AccountID,
	})
}

// ====================================================================
// 核心接口 2: 读取邮件
//   GET /api/inbox?account_id=acc_xxx[&alias=xxx@icloud.com][&limit=20][&days=7]
//
//   - 不传 alias: 返回该账号收件箱最近邮件
//   - 传 alias:   只返回发给该 HME 别名的邮件
//
//   认证优先级: IMAP (App Password) 优先 > Web API (Cookie) 回退
// ====================================================================

func (s *Server) listInboxHandler(c *gin.Context) {
	accountID := c.Query("account_id")
	if accountID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数缺失: account_id")
		return
	}
	alias := strings.TrimSpace(c.Query("alias"))
	limit, err := parseInboxInt(c.DefaultQuery("limit", "20"), 1, 100)
	if err != nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: limit 需为 1-100 的整数")
		return
	}
	days, err := parseInboxInt(c.DefaultQuery("days", "7"), 1, 90)
	if err != nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: days 需为 1-90 的整数")
		return
	}

	result, err := s.be.ListInbox(InboxQuery{
		AccountID: accountID,
		Alias:     alias,
		Limit:     limit,
		Days:      days,
	})
	if err != nil {
		backendFail(c, err)
		return
	}
	ok(c, result)
}

// parseInboxInt 解析整数参数,非法或越界返回错误(不再静默变成 0)。
func parseInboxInt(raw string, min, max int) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, errors.New("invalid integer")
	}
	if v < min || v > max {
		return 0, errors.New("out of range")
	}
	return v, nil
}

// ====================================================================
// 辅助接口:别名
// ====================================================================

func (s *Server) listAliasesHandler(c *gin.Context) {
	accountID := c.Query("account_id")
	if accountID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数缺失: account_id")
		return
	}
	aliases, err := s.be.ListAliases(accountID)
	if err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{
		"account_id": accountID,
		"count":      len(aliases),
		"aliases":    aliases,
	})
}

type aliasActionReq struct {
	AccountID string `json:"account_id"`
}

// validateAliasAction 校验别名操作的匿名 ID 与请求体。
func validateAliasAction(c *gin.Context) (accountID, anonymousID string, valid bool) {
	anonymousID = c.Param("id")
	var req aliasActionReq
	if err := c.ShouldBindJSON(&req); err != nil || req.AccountID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: account_id 必填")
		return "", "", false
	}
	if anonymousID == "" || len(anonymousID) > 256 {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: anonymous id 无效")
		return "", "", false
	}
	return req.AccountID, anonymousID, true
}

func (s *Server) deactivateAliasHandler(c *gin.Context) {
	accountID, anonymousID, valid := validateAliasAction(c)
	if !valid {
		return
	}
	success, err := s.be.SetAliasActive(accountID, anonymousID, false)
	if err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{"anonymous_id": anonymousID, "success": success})
}

func (s *Server) reactivateAliasHandler(c *gin.Context) {
	accountID, anonymousID, valid := validateAliasAction(c)
	if !valid {
		return
	}
	success, err := s.be.SetAliasActive(accountID, anonymousID, true)
	if err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{"anonymous_id": anonymousID, "success": success})
}

func (s *Server) deleteAliasHandler(c *gin.Context) {
	accountID, anonymousID, valid := validateAliasAction(c)
	if !valid {
		return
	}
	if err := s.be.DeleteAlias(accountID, anonymousID); err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{"anonymous_id": anonymousID})
}

// ====================================================================
// 系统
// ====================================================================

func (s *Server) reloadConfigHandler(c *gin.Context) {
	if err := s.be.Reload(); err != nil {
		backendFail(c, err)
		return
	}
	ok(c, gin.H{"message": "配置已重新加载"})
}
