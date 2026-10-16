// Package server - API 秘钥管理 HTTP 接口(仅限管理员会话)。
//
// 全部接口挂在 authed 组下, 由 requirePermission 拒绝秘钥访问
// (sessionOnlyRoutes): 秘钥不得管理秘钥或给自己提权。
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/apikey"
)

// listKeysHandler 返回全部秘钥(含明文, 面板可查看/复制)。
//
//	GET /api/keys
func (s *Server) listKeysHandler(c *gin.Context) {
	keys := s.keys.List()
	ok(c, gin.H{
		"count": len(keys),
		"keys":  keys,
	})
}

// createKeyReq 是创建秘钥的请求体。
type createKeyReq struct {
	Name        string   `json:"name"`
	Key         string   `json:"key"`
	Permissions []string `json:"permissions"`
	// AccountIDs 是账号使用范围(空 = 全部账号)。
	AccountIDs []string `json:"account_ids"`
}

// createKeyHandler 创建秘钥。
//
//	POST /api/keys  {"name":"脚本A","key":"可留空","permissions":["inbox.list"],"account_ids":["acc_1"]}
func (s *Server) createKeyHandler(c *gin.Context) {
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 请求体格式无效")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: name 必填")
		return
	}
	if bad, valid := validatePermissions(req.Permissions); !valid {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 未知权限 "+bad)
		return
	}
	if bad, valid := s.validateAccountIDs(req.AccountIDs); !valid {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 账号不存在 "+bad)
		return
	}

	rec, err := s.keys.Create(apikey.CreateInput{
		Name:        req.Name,
		Key:         req.Key,
		Permissions: req.Permissions,
		AccountIDs:  req.AccountIDs,
	})
	switch {
	case errors.Is(err, apikey.ErrKeyExists):
		failCode(c, http.StatusConflict, "KEY_EXISTS", "该秘钥已存在,请换一个")
		return
	case errors.Is(err, apikey.ErrTooManyKeys):
		failCode(c, http.StatusConflict, "KEY_LIMIT_REACHED", "秘钥数量已达上限")
		return
	case err != nil:
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	ok(c, rec)
}

// updateKeyReq 是更新秘钥的请求体; nil 字段保持不变。
type updateKeyReq struct {
	Name        *string   `json:"name"`
	Permissions *[]string `json:"permissions"`
	// AccountIDs 为 nil 时不变; 指向空数组清空(= 全部账号)。
	AccountIDs *[]string `json:"account_ids"`
	Enabled    *bool     `json:"enabled"`
}

// updateKeyHandler 更新秘钥的名称/权限/账号范围/启用状态(秘钥值不可变)。
//
//	PATCH /api/keys/:id
func (s *Server) updateKeyHandler(c *gin.Context) {
	id := c.Param("id")
	var req updateKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 请求体格式无效")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		// 与创建路径一致: 名称必填, 不允许被清空(前端表单也要求非空)。
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: name 不能为空")
		return
	}
	if req.Permissions != nil {
		if bad, valid := validatePermissions(*req.Permissions); !valid {
			failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 未知权限 "+bad)
			return
		}
	}
	if req.AccountIDs != nil {
		if bad, valid := s.validateAccountIDs(*req.AccountIDs); !valid {
			failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: 账号不存在 "+bad)
			return
		}
	}

	rec, err := s.keys.Update(id, apikey.UpdateInput{
		Name:        req.Name,
		Permissions: req.Permissions,
		AccountIDs:  req.AccountIDs,
		Enabled:     req.Enabled,
	})
	switch {
	case errors.Is(err, apikey.ErrKeyNotFound):
		failCode(c, http.StatusNotFound, "KEY_NOT_FOUND", "秘钥不存在")
		return
	case err != nil:
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	ok(c, rec)
}

// deleteKeyHandler 删除秘钥。
//
//	DELETE /api/keys/:id
func (s *Server) deleteKeyHandler(c *gin.Context) {
	id := c.Param("id")
	if err := s.keys.Delete(id); err != nil {
		if errors.Is(err, apikey.ErrKeyNotFound) {
			failCode(c, http.StatusNotFound, "KEY_NOT_FOUND", "秘钥不存在")
			return
		}
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", "删除秘钥失败")
		return
	}
	ok(c, gin.H{"deleted": true})
}

// listPermissionsHandler 返回权限目录(分组 + 条目), 供前端渲染勾选。
//
//	GET /api/permissions
func (s *Server) listPermissionsHandler(c *gin.Context) {
	ok(c, gin.H{
		"groups":      permissionGroups,
		"permissions": permissionCatalog,
	})
}

// validateAccountIDs 校验账号范围里的每个 ID 都对应一个已存在账号。
//
// 拒绝未知账号: 防止秘钥绑定了不存在/已删除的账号 ID, 导致面板显示
// 幽灵条目、或范围判定出现意外放行。空列表合法(不限账号)。
// 第二个返回值 false 时, 第一个返回值为第一个不存在的 ID。
func (s *Server) validateAccountIDs(ids []string) (string, bool) {
	if len(ids) == 0 {
		return "", true
	}
	known := map[string]bool{}
	for _, acc := range s.be.ListAccounts() {
		known[acc.ID] = true
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue // 空串由 normalize 阶段丢弃, 不算未知账号
		}
		if !known[id] {
			return id, false
		}
	}
	return "", true
}
