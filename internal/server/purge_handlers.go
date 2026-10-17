package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/mail"
)

// ====================================================================
// 邮件清理 HTTP 接口
// ====================================================================
//
// 权限: inbox.purge(与单封删除 inbox.delete 分离——批量清空风险量级不同,
// 按最小权限原则独立授权)。
//
// 账号范围: 全部接口的目标账号经中间件 resourceAccountFor 判定:
//   - preview/create 用 body account_id;
//   - purge-tasks/:id 经任务真实归属账号(不接受请求自述)。
// 范围外 → 403 ACCOUNT_NOT_ALLOWED(任务路由伪装 404 防探测)。

// purgePreviewReq 是预览请求体。
type purgePreviewReq struct {
	AccountID  string `json:"account_id"`
	RangeStart string `json:"range_start"`
	RangeEnd   string `json:"range_end"`
}

// parsePurgeRange 解析并校验清理区间。
func parsePurgeRange(start, end string) (time.Time, time.Time, string) {
	st, err := time.Parse(time.RFC3339, strings.TrimSpace(start))
	if err != nil {
		return time.Time{}, time.Time{}, "range_start 格式无效(需 RFC3339)"
	}
	en, err := time.Parse(time.RFC3339, strings.TrimSpace(end))
	if err != nil {
		return time.Time{}, time.Time{}, "range_end 格式无效(需 RFC3339)"
	}
	if !en.After(st) {
		return time.Time{}, time.Time{}, "range_end 必须晚于 range_start"
	}
	return st, en, ""
}

// purgePreviewHandler 预览清理范围(仅统计, 不删除)。
//
//	POST /api/inbox/purge/preview
//	{"account_id":"acc_x","range_start":"...","range_end":"..."}
func (s *Server) purgePreviewHandler(c *gin.Context) {
	// 非 managerBackend 时执行器未注入: 干净报错, 不要靠 Recovery 兜 500。
	if s.purge.withClient == nil {
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", "清理执行器未配置 IMAP 客户端")
		return
	}
	var req purgePreviewReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AccountID) == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: account_id 必填")
		return
	}
	st, en, msg := parsePurgeRange(req.RangeStart, req.RangeEnd)
	if msg != "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: "+msg)
		return
	}

	type folderCount struct {
		Folder string `json:"folder"`
		Count  int    `json:"count"`
	}
	counts := make([]folderCount, 0, len(purgeFolders))
	total := 0
	// IMAP 操作预算: 卡死的上游不再让 HTTP 无限挂起。
	err := withIMAPTimeout(purgeOpTimeout, func() error {
		return s.purge.withClient(req.AccountID, func(mc *mail.Client) error {
			for _, folder := range purgeFolders {
				uids, missing, err := mc.SearchRange(folder, mail.DateRange{Start: st, End: en})
				if err != nil {
					return err
				}
				if missing {
					continue
				}
				counts = append(counts, folderCount{Folder: folder, Count: len(uids)})
				total += len(uids)
			}
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, errIMAPTimeout) {
			failCode(c, http.StatusBadGateway, "UPSTREAM_FAILURE", "读取超时，请稍后重试")
			return
		}
		backendFail(c, err)
		return
	}
	// 估算耗时: 每块 (purgeChunkDelay + 抖动均值) 秒, 块数 = ceil(total/chunk)。
	chunk := purgeChunkSize
	if chunk <= 0 {
		chunk = 500
	}
	chunks := (total + chunk - 1) / chunk
	perChunk := purgeChunkDelay + purgeChunkJitter/2
	estimated := int(time.Duration(chunks) * perChunk / time.Second)

	ok(c, gin.H{
		"total":             total,
		"folders":           counts,
		"estimated_seconds": estimated,
		"range_start":       st.Format(time.RFC3339),
		"range_end":         en.Format(time.RFC3339),
	})
}

// purgeCreateHandler 创建清理任务。
//
//	POST /api/inbox/purge  {"account_id":"acc_x","range_start":"...","range_end":"..."}
func (s *Server) purgeCreateHandler(c *gin.Context) {
	var req purgePreviewReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AccountID) == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: account_id 必填")
		return
	}
	st, en, msg := parsePurgeRange(req.RangeStart, req.RangeEnd)
	if msg != "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "参数错误: "+msg)
		return
	}
	// 账号名冗余(列表展示)。
	name := req.AccountID
	for _, a := range s.be.ListAccounts() {
		if a.ID == req.AccountID {
			name = a.Name
			break
		}
	}
	tk, err := s.purge.create(req.AccountID, name, st, en)
	if err != nil {
		if strings.Contains(err.Error(), "已有清理任务") {
			failCode(c, http.StatusConflict, "PURGE_IN_PROGRESS", err.Error())
			return
		}
		failCode(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	// 预览确定总数(供进度条分母), 然后异步执行。
	s.purge.setTotalFromPreview(tk.ID, st, en)
	s.purge.run(tk.ID)
	// 审计日志: 开始条目。
	if s.purge.logFn != nil {
		s.purge.logFn(tk.ID, "info", "清理任务开始: %s %s ~ %s",
			name, st.Format("2006-01-02"), en.Format("2006-01-02"))
	}
	ok(c, tk)
}

// listPurgeTasksHandler 列出清理任务(按秘钥账号范围过滤)。
//
//	GET /api/purge-tasks
func (s *Server) listPurgeTasksHandler(c *gin.Context) {
	tasks := s.purge.list()
	ok(c, filterPurgeTasksByKeyScope(c, tasks))
}

// purgeTaskActionHandler 处理 pause/resume/cancel。
func (s *Server) purgeTaskActionHandler(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var err error
		switch action {
		case "pause":
			err = s.purge.pause(id)
		case "resume":
			err = s.purge.resume(id)
			if err == nil {
				s.purge.run(id) // 继续执行
			}
		case "cancel":
			err = s.purge.cancel(id)
		}
		if err != nil {
			if strings.Contains(err.Error(), "不存在") {
				failCode(c, http.StatusNotFound, "PURGE_NOT_FOUND", "清理任务不存在")
				return
			}
			// 同账号互斥: resume 时账号已被别的任务占用 → 409(与 create 一致)。
			if strings.Contains(err.Error(), "已有清理任务") {
				failCode(c, http.StatusConflict, "PURGE_IN_PROGRESS", err.Error())
				return
			}
			failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		tk, _ := s.purge.get(id)
		ok(c, tk)
	}
}

// filterPurgeTasksByKeyScope 按秘钥账号范围过滤任务列表。
func filterPurgeTasksByKeyScope(c *gin.Context, tasks []PurgeTask) []PurgeTask {
	scope, restricted := keyScopeFromContext(c)
	if !restricted {
		return tasks
	}
	out := make([]PurgeTask, 0, len(tasks))
	for _, t := range tasks {
		if containsString(scope, t.AccountID) {
			out = append(out, t)
		}
	}
	return out
}
