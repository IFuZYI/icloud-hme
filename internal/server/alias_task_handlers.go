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
