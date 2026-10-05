package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/hme"
)

// ====================================================================
// iCloud 两段式登录
//
//	POST /api/accounts/:id/login/begin   body: {"password": "..."}
//	  → {"status":"done"}                        无需 2FA,登录完成
//	  → {"status":"otp_required","session_id"}   需要 2FA,等待验证码
//	POST /api/accounts/:id/login/otp     body: {"session_id","code","method"?,"phone_id"?}
//	  → {"status":"done"}                        验证通过,登录完成
//	POST /api/accounts/:id/login/sms     body: {"session_id","phone_id"}   发送短信验证码
//	GET  /api/accounts/:id/login/phones?session_id=...                      受信任手机号列表
//	POST /api/accounts/:id/login/resend  body: {"session_id"}               重推验证码
//
// 旧的一次性接口 POST /api/accounts/:id/login 保留兼容(内部走两段式)。
// ====================================================================

type loginBeginReq struct {
	Password string `json:"password"`
}

func (s *Server) loginBeginHandler(c *gin.Context) {
	id := c.Param("id")
	var req loginBeginReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: password 必填")
		return
	}
	if s.newLoginSession == nil {
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", "登录功能不可用")
		return
	}
	session, err := s.newLoginSession(id)
	if err != nil {
		backendFail(c, mapLoginSessionErr(err))
		return
	}
	if err := session.Begin(req.Password); err != nil {
		if errors.Is(err, hme.ErrOTPRequired) {
			// 需要 2FA:尽力自动推送一次验证码,然后保存会话等待提交。
			if resendErr := session.ResendOTP(); resendErr != nil {
				slog.Info("自动推送 2FA 验证码失败", "account", id, "err", resendErr.Error())
			}
			sessionID := s.logins.put(id, session)
			ok(c, gin.H{"status": "otp_required", "session_id": sessionID})
			return
		}
		backendFail(c, classifyLoginErr(err))
		return
	}
	// 无需 2FA:直接落库完成。
	sum, err := session.Summary()
	if err != nil {
		backendFail(c, mapLoginSessionErr(err))
		return
	}
	ok(c, gin.H{"status": "done", "account": sum})
}

type loginOTPReq struct {
	SessionID string `json:"session_id"`
	Code      string `json:"code"`
	Method    string `json:"method"`   // "device"(默认) 或 "sms"
	PhoneID   int    `json:"phone_id"` // method=sms 时必填
}

func (s *Server) loginOTPHandler(c *gin.Context) {
	id := c.Param("id")
	var req loginOTPReq
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.Code == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: session_id, code 必填")
		return
	}
	session, ok2 := s.requireLoginSession(c, id, req.SessionID)
	if !ok2 {
		return
	}

	var err error
	if req.Method == "sms" {
		err = session.CompleteSMS(req.PhoneID, req.Code)
	} else {
		err = session.CompleteOTP(req.Code)
	}
	if err != nil {
		// 校验失败保留会话,允许用户重输验证码;只有成功才消费。
		backendFail(c, classifyLoginErr(err))
		return
	}
	if _, _, err := s.logins.consume(req.SessionID); err != nil {
		failCode(c, http.StatusGone, "LOGIN_SESSION_EXPIRED", err.Error())
		return
	}
	sum, err := session.Summary()
	if err != nil {
		backendFail(c, mapLoginSessionErr(err))
		return
	}
	ok(c, gin.H{"status": "done", "account": sum})
}

// requireLoginSession 取出登录会话并校验归属(四个 handler 的公共前置)。
// 失败时已写入响应, 返回 false。
func (s *Server) requireLoginSession(c *gin.Context, accountID, sessionID string) (LoginSession, bool) {
	owner, session, err := s.logins.peek(sessionID)
	if err != nil {
		failCode(c, http.StatusGone, "LOGIN_SESSION_EXPIRED", err.Error())
		return nil, false
	}
	if owner != accountID {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "登录会话与账号不匹配")
		return nil, false
	}
	return session, true
}

type loginSMSReq struct {
	SessionID string `json:"session_id"`
	PhoneID   int    `json:"phone_id"`
}

func (s *Server) loginSMSHandler(c *gin.Context) {
	id := c.Param("id")
	var req loginSMSReq
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" || req.PhoneID <= 0 {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: session_id, phone_id 必填")
		return
	}
	session, ok2 := s.requireLoginSession(c, id, req.SessionID)
	if !ok2 {
		return
	}
	if err := session.SendSMS(req.PhoneID); err != nil {
		backendFail(c, classifyLoginErr(err))
		return
	}
	ok(c, gin.H{"sent": true})
}

func (s *Server) loginPhonesHandler(c *gin.Context) {
	id := c.Param("id")
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if sessionID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数缺失: session_id")
		return
	}
	session, ok2 := s.requireLoginSession(c, id, sessionID)
	if !ok2 {
		return
	}
	phones, err := session.TrustedPhones()
	if err != nil {
		backendFail(c, classifyLoginErr(err))
		return
	}
	type phoneDTO struct {
		ID                 int    `json:"id"`
		NumberWithDialCode string `json:"number_with_dial_code"`
	}
	out := make([]phoneDTO, 0, len(phones))
	for _, p := range phones {
		out = append(out, phoneDTO{ID: p.ID, NumberWithDialCode: p.NumberWithDialCode})
	}
	ok(c, gin.H{"phones": out})
}

type loginResendReq struct {
	SessionID string `json:"session_id"`
}

func (s *Server) loginResendHandler(c *gin.Context) {
	id := c.Param("id")
	var req loginResendReq
	if err := c.ShouldBindJSON(&req); err != nil || req.SessionID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: session_id 必填")
		return
	}
	session, ok2 := s.requireLoginSession(c, id, req.SessionID)
	if !ok2 {
		return
	}
	if err := session.ResendOTP(); err != nil {
		backendFail(c, classifyLoginErr(err))
		return
	}
	ok(c, gin.H{"sent": true})
}

// mapLoginSessionErr 把账号级错误映射为稳定错误码。
// 分类统一收敛到 classifyLoginErr, 避免多处各写一套「账号不存在」分支。
func mapLoginSessionErr(err error) error {
	if err == nil {
		return nil
	}
	return classifyLoginErr(err)
}
