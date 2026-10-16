package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

func aliasTaskFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errAliasTaskValidation):
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, errAliasTaskNotFound):
		failCode(c, http.StatusNotFound, "TASK_NOT_FOUND", err.Error())
	default:
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

// listAliasTasksHandler 处理 GET /api/alias-tasks。
//
// 秘钥路径: 只返回秘钥账号范围内的任务(防止受限秘钥读到其它账号的任务配置)。
// 会话路径: 返回全部。
func (s *Server) listAliasTasksHandler(c *gin.Context) {
	tasks := s.task.list()
	ok(c, filterTasksByKeyScope(c, tasks))
}

// listAliasTaskLogsHandler 处理 GET /api/alias-task-logs。
//
// 秘钥路径: 只返回秘钥账号范围内任务的日志; 会话路径返回全部。
func (s *Server) listAliasTaskLogsHandler(c *gin.Context) {
	logs := s.task.listLogs()
	ok(c, filterTaskLogsByKeyScope(c, s, logs))
}

// cleanupAliasTaskLogsHandler 处理 POST /api/alias-task-logs/cleanup。
//
// 按时间清理任务日志: 删除严格早于「now - older_than_days 天」的条目。
// older_than_days 为 1-3650 的整数(前端提供 1 天前/1 周前/1 个月前 三档)。
func (s *Server) cleanupAliasTaskLogsHandler(c *gin.Context) {
	var req struct {
		OlderThanDays *int `json:"older_than_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.OlderThanDays == nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: older_than_days 必填")
		return
	}
	if *req.OlderThanDays < 1 || *req.OlderThanDays > 3650 {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: older_than_days 需为 1-3650 的整数")
		return
	}
	scope, _ := keyScopeFromContext(c)
	deleted, remaining, err := s.task.cleanupLogsScoped(*req.OlderThanDays, scope)
	if err != nil {
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", "日志清理失败: "+err.Error())
		return
	}
	ok(c, gin.H{"deleted": deleted, "remaining": remaining})
}
func (s *Server) createAliasTaskHandler(c *gin.Context) {
	var in aliasTaskInput
	if c.ShouldBindJSON(&in) != nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误")
		return
	}
	t, e := s.task.create(in)
	if e != nil {
		aliasTaskFail(c, e)
		return
	}
	c.JSON(http.StatusCreated, apiResp{Success: true, Data: t})
}
func (s *Server) updateAliasTaskHandler(c *gin.Context) {
	var in aliasTaskInput
	if c.ShouldBindJSON(&in) != nil {
		failCode(c, 400, "VALIDATION_ERROR", "参数错误")
		return
	}
	// 范围受限秘钥不得改变任务归属账号: body 的 account_id 若存在且不在
	// 秘钥白名单内, 拒绝——否则可把范围内任务迁移到范围外账号。
	if scope, restricted := keyScopeFromContext(c); restricted {
		if in.AccountID != "" && !containsString(scope, in.AccountID) {
			slog.Debug("范围受限秘钥试图迁移任务归属", "account_id", in.AccountID)
			failCode(c, http.StatusForbidden, "ACCOUNT_NOT_ALLOWED", "API 秘钥无权将任务迁移到该账号")
			return
		}
	}
	t, e := s.task.update(c.Param("id"), in)
	if e != nil {
		aliasTaskFail(c, e)
		return
	}
	ok(c, t)
}
func (s *Server) toggleAliasTaskHandler(c *gin.Context) {
	t, e := s.task.toggle(c.Param("id"))
	if e != nil {
		aliasTaskFail(c, e)
		return
	}
	ok(c, t)
}
func (s *Server) deleteAliasTaskHandler(c *gin.Context) {
	if e := s.task.remove(c.Param("id")); e != nil {
		aliasTaskFail(c, e)
		return
	}
	ok(c, gin.H{"deleted": true})
}
