// Package server - API 秘钥的权限目录与路由映射。
//
// 权限模型: 每个权限 ID 对应一组接口; 秘钥勾选后即可调用对应接口。
// 目录是唯一权威来源:
//   - permissionCatalog 定义全部权限(ID/标签/分组);
//   - protectedRouteList 声明每个受保护路由需要的权限;
//   - sessionOnlyRoutes 列出「仅限管理员会话」的路由(秘钥一律拒绝)。
//
// 完整性由测试双向钉住(TestRoutePermissionTableComplete): gin 实际注册的
// 路由与上述两张表必须完全一致——新增路由忘声明权限、或删除路由留下死
// 条目, 都会让测试失败。
package server

// Permission 是权限目录中的一条。
type Permission struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// PermissionGroup 是权限分组(前端按组渲染勾选)。
type PermissionGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// permissionGroups 是分组表(顺序即前端展示顺序)。
var permissionGroups = []PermissionGroup{
	{ID: "accounts", Label: "账号"},
	{ID: "aliases", Label: "别名"},
	{ID: "tasks", Label: "自动任务"},
	{ID: "inbox", Label: "收件箱"},
	{ID: "system", Label: "系统"},
}

// permissionCatalog 是权限目录(顺序即前端展示顺序)。
//
// 读写分离: list 类权限只读; write 类权限允许状态变更。按最小权限原则,
// 脚本通常只需要 inbox 组的读权限。
var permissionCatalog = []Permission{
	// 账号
	{ID: "accounts.list", Label: "查看账号列表", Group: "accounts"},
	{ID: "accounts.write", Label: "修改账号（增删改、凭据、登录）", Group: "accounts"},
	// 别名
	{ID: "aliases.list", Label: "查看别名", Group: "aliases"},
	{ID: "aliases.create", Label: "创建别名", Group: "aliases"},
	{ID: "aliases.write", Label: "管理别名（停用/启用/删除）", Group: "aliases"},
	// 自动任务
	{ID: "tasks.list", Label: "查看自动任务与日志", Group: "tasks"},
	{ID: "tasks.write", Label: "管理自动任务与日志", Group: "tasks"},
	// 收件箱
	{ID: "inbox.list", Label: "读取收件箱列表", Group: "inbox"},
	{ID: "inbox.get", Label: "读取邮件详情", Group: "inbox"},
	{ID: "inbox.previews", Label: "读取邮件摘要", Group: "inbox"},
	{ID: "inbox.delete", Label: "删除邮件", Group: "inbox"},
	// 系统
	{ID: "system.reload", Label: "重载配置", Group: "system"},
}

// routePermission 是一条「路由 → 权限」映射。Path 不含 /api 前缀
// (与 gin 注册时相对分组的形式一致)。
type routePermission struct {
	Method     string
	Path       string
	Permission string
}

// protectedRouteList 声明每个受保护路由需要的权限。
//
// 新增路由必须在此声明(或加入 sessionOnlyRoutes), 否则完整性测试失败。
var protectedRouteList = []routePermission{
	// 账号管理
	{"GET", "/accounts", "accounts.list"},
	{"POST", "/accounts", "accounts.write"},
	{"PATCH", "/accounts/:id", "accounts.write"},
	{"PUT", "/accounts/:id/proxy", "accounts.write"},
	{"PUT", "/accounts/:id/cookies", "accounts.write"},
	{"POST", "/accounts/:id/password", "accounts.write"},
	{"PUT", "/accounts/:id/mailbox", "accounts.write"},
	{"POST", "/accounts/:id/login", "accounts.write"},
	{"POST", "/accounts/:id/check", "accounts.write"},
	{"POST", "/accounts/:id/login/begin", "accounts.write"},
	{"POST", "/accounts/:id/login/otp", "accounts.write"},
	{"POST", "/accounts/:id/login/sms", "accounts.write"},
	{"GET", "/accounts/:id/login/phones", "accounts.list"},
	{"POST", "/accounts/:id/login/resend", "accounts.write"},
	{"DELETE", "/accounts/:id", "accounts.write"},

	// 别名
	{"GET", "/alias-labels", "aliases.list"},
	{"POST", "/create", "aliases.create"},
	{"GET", "/aliases", "aliases.list"},
	{"POST", "/aliases/batch", "aliases.write"},
	{"POST", "/aliases/:id/deactivate", "aliases.write"},
	{"POST", "/aliases/:id/reactivate", "aliases.write"},
	{"DELETE", "/aliases/:id", "aliases.write"},

	// 自动任务
	{"GET", "/alias-tasks", "tasks.list"},
	{"GET", "/alias-task-logs", "tasks.list"},
	{"POST", "/alias-task-logs/cleanup", "tasks.write"},
	{"POST", "/alias-tasks", "tasks.write"},
	{"PATCH", "/alias-tasks/:id", "tasks.write"},
	{"POST", "/alias-tasks/:id/toggle", "tasks.write"},
	{"DELETE", "/alias-tasks/:id", "tasks.write"},

	// 收件箱
	{"GET", "/inbox", "inbox.list"},
	{"POST", "/inbox/previews", "inbox.previews"},
	{"GET", "/inbox/:message_id", "inbox.get"},
	{"DELETE", "/inbox/:message_id", "inbox.delete"},

	// 系统
	{"POST", "/reload", "system.reload"},
}

// sessionOnlyRoutes 是「仅限管理员会话」的路由(完整路径, 含 /api)。
// 秘钥访问这些接口一律 403: 秘钥不得管理秘钥或给自己提权。
var sessionOnlyRoutes = []string{
	"POST /api/auth/logout",
	"GET /api/keys",
	"POST /api/keys",
	"PATCH /api/keys/:id",
	"DELETE /api/keys/:id",
	"GET /api/permissions",
}

// routePermissionFor 按 (method, 完整路径) 查路由所需权限。
// 第二个返回值为 false 表示该路由不在权限表内(即仅限会话)。
func routePermissionFor(method, fullPath string) (string, bool) {
	for _, r := range protectedRouteList {
		if r.Method == method && "/api"+r.Path == fullPath {
			return r.Permission, true
		}
	}
	return "", false
}

// permissionExists 判断权限 ID 是否在目录中(创建/更新秘钥时校验)。
func permissionExists(id string) bool {
	for _, p := range permissionCatalog {
		if p.ID == id {
			return true
		}
	}
	return false
}

// validatePermissions 校验权限列表: 全部 ID 必须在目录中, 否则返回第一个
// 非法 ID。空列表合法(无任何权限, 失败关闭)。
func validatePermissions(perms []string) (string, bool) {
	for _, p := range perms {
		if !permissionExists(p) {
			return p, false
		}
	}
	return "", true
}
