package server

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
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

func (s *Server) listAliasTasksHandler(c *gin.Context)    { ok(c, s.task.list()) }
func (s *Server) listAliasTaskLogsHandler(c *gin.Context) { ok(c, s.task.listLogs()) }

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
	deleted, remaining, err := s.task.cleanupLogs(*req.OlderThanDays)
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
