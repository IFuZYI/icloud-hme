# iCloud HME 管理界面 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. 如果 DeepSeek 环境没有这些技能，仍须严格按复选框顺序执行，并在每个提交点停下来检查测试结果与 `git diff`。

**Goal:** 为现有 iCloud Hide My Email Go 服务增加安全、可测试、随单二进制分发的中文管理界面，覆盖账号、凭据、别名与收件箱管理。

**Architecture:** 浏览器端使用 React + TypeScript + Vite 构建单页应用，生产产物写入 `internal/webui/dist/` 并由 Go `embed.FS` 内嵌；开发时由 Vite 把 `/api` 代理到 Gin。Gin 在业务路由前增加单管理员内存会话、登录限流和 CSRF 校验，账号接口统一返回无秘密的 DTO，现有 iCloud/SRP/IMAP 客户端保持不动。

**Tech Stack:** Go 1.26、Gin 1.12、React 19.2、Vite 8.1、TypeScript、Vitest 4、Testing Library、MSW、原生 CSS、Docker multi-stage build。

## Global Constraints

- 保持 Go module 名称 `icloud-hme` 和最低 Go 版本 `1.26` 不变。
- 前端构建机使用 Node.js `22.12+`；`package-lock.json` 必须提交，CI 和 Docker 一律使用 `npm ci`。
- 不引入 UI 组件库、CSS 框架、远程 CDN、远程字体或遥测；样式使用项目内原生 CSS。
- 生产仍交付单个 Go 二进制；Node.js 仅是源码构建依赖，不是运行时依赖。
- 所有业务 API 与管理页面同源；不添加 CORS 中间件。
- UI 文案、API 用户错误和新增注释使用中文；代码标识符使用英文。
- 所有 `/api` 业务路由都必须认证；状态变更方法还必须验证 CSRF。
- Cookie、App Password、iCloud 密码、OTP、管理员密码、含凭据的代理 URL 不得出现在响应、日志、URL 或 Web Storage 中。
- 只渲染邮件摘要纯文本；禁止 `dangerouslySetInnerHTML`。
- 保留现有业务路由及既有字段命名，除认证要求和明确列出的安全修复外不做 API 风格重写。
- 每个任务独立提交；不得把格式化、无关重构或 iCloud 协议改动混入提交。

---

## 1. 范围、现状与关键决策

### 1.1 当前仓库事实

- `main.go` 创建 `account.Manager`，然后调用 `server.New(mgr, debug)` 启动 `:8081`。
- `internal/server/server.go` 注册账号、创建别名、收件箱、别名操作和 reload API，目前没有认证、CSRF、请求大小限制或安全响应头。
- `internal/account/manager.go` 把账号和秘密写入权限为 `0600` 的 `accounts.json`；`ListAccounts` 只清空 Cookies，仍会序列化 `AppPassword` 与原始 `Proxy`。
- `loginAccount` 当前直接返回 `client.Cookies`，管理界面上线前必须移除。
- 仓库没有 `_test.go`、前端目录或常规 CI；Docker、Release 和 `build.sh` 都只运行 Go 构建。
- `.gitignore` 当前忽略 `docs/` 和所有 `*.js`，新增前端应全部使用 `.ts/.tsx/.mjs`，并显式放行本计划或调整忽略规则。

### 1.2 产品边界

管理台包含四个用户流程：管理员登录；账号与凭据；HME 别名；邮件摘要。它是单管理员、本地/自托管工具，不实现用户系统、RBAC、数据库会话、邮件正文、附件、富 HTML、发信或跨域部署。

### 1.3 安全兼容性决策

这是一次有意的破坏性安全升级：部署者必须设置 `ICLOUD_HME_ADMIN_PASSWORD`，现有匿名 API 调用将收到 `401 AUTH_REQUIRED`。管理员会话只存内存，进程重启即失效；TLS 反向代理部署必须设置 `ICLOUD_HME_SECURE_COOKIE=true`。

### 1.4 前端选择依据

页面包含多种异步表单、账号上下文、筛选表格、会话恢复、破坏性确认和错误状态，React + TypeScript 比手写 DOM 更容易让后续代理保持状态与类型一致。仍然只使用 React、React Router 和原生 CSS，不引入大型管理模板；Vite 官方当前提供 React TypeScript 模板，Vite 8.1 要求 Node 20.19+ 或 22.12+，本计划统一采用 Node 22.12+。

参考：

- [React 版本说明](https://react.dev/versions)
- [Vite 8.1 公告](https://vite.dev/blog/announcing-vite8-1)
- [Vite Getting Started 与 Node 要求](https://vite.dev/guide/)
- [Vitest 4 与使用指南](https://vitest.dev/blog/vitest-4)

---

## 2. 目标结构与文件职责

```text
icloud-hme/
├── main.go                               # 读取安全配置并组装 Server
├── internal/
│   ├── account/
│   │   ├── manager.go                    # 现有持久化；补并发快照与 UI 所需写接口
│   │   ├── public.go                     # 唯一的账号公开 DTO/校验入口
│   │   └── public_test.go
│   ├── auth/
│   │   ├── manager.go                    # 密码校验、会话、CSRF、过期清理
│   │   ├── limiter.go                    # 按客户端 IP 的登录失败限流
│   │   ├── manager_test.go
│   │   └── limiter_test.go
│   ├── server/
│   │   ├── server.go                     # 路由分组与现有业务 handler
│   │   ├── auth.go                       # 登录/会话/退出 handler 与中间件
│   │   ├── middleware.go                 # CSRF、请求上限、安全头
│   │   ├── response.go                   # apiResp、稳定错误码、统一响应
│   │   ├── backend.go                    # 可替换业务接口与 Manager 适配器
│   │   ├── account_handlers.go           # 账号、Cookie、代理、密码 handler
│   │   ├── server_test.go
│   │   ├── auth_test.go
│   │   └── account_handlers_test.go
│   └── webui/
│       ├── embed.go                      # embed.FS、缓存头和 SPA fallback
│       ├── embed_test.go
│       └── dist/placeholder.txt          # 无前端构建时保证 Go 可编译
├── web/
│   ├── package.json / package-lock.json
│   ├── vite.config.ts / tsconfig*.json / eslint.config.mjs
│   ├── index.html
│   ├── public/placeholder.txt            # 构建后保留 embed 占位锚点
│   └── src/
│       ├── main.tsx / App.tsx / styles.css
│       ├── api/client.ts / api/types.ts
│       ├── auth/AuthProvider.tsx
│       ├── components/                   # Shell、Dialog、Toast、状态组件
│       ├── pages/                        # Login、Accounts、Aliases、Inbox
│       └── test/                         # Vitest setup 与 MSW fixtures
├── .github/workflows/ci.yml              # Go + Web 常规检查
├── .github/workflows/release.yml         # 发布前构建前端
├── Dockerfile                            # Node -> Go -> Alpine 三阶段
├── build.sh                              # 本地完整构建
├── README.md / API.md                    # 登录、构建、部署与契约
└── .gitignore                            # node_modules 与生成的 dist
```

职责规则：`account.Account` 只用于持久化和内部客户端构造；HTTP 层只能序列化 `account.Summary`。`auth.Manager` 不依赖 Gin；Gin 中间件只负责 Cookie/Header 与 HTTP 状态映射。前端只通过 `web/src/api/client.ts` 发请求，页面不得直接调用 `fetch`。

---

## 3. 冻结的 HTTP 契约

### 3.1 统一响应

成功：

```json
{"success":true,"data":{}}
```

失败：

```json
{"success":false,"code":"VALIDATION_ERROR","message":"参数错误"}
```

允许的稳定错误码：`AUTH_REQUIRED`、`INVALID_CREDENTIALS`、`RATE_LIMITED`、`CSRF_INVALID`、`VALIDATION_ERROR`、`ACCOUNT_NOT_FOUND`、`OTP_REQUIRED`、`OTP_INVALID`、`UPSTREAM_UNAUTHORIZED`、`UPSTREAM_FAILURE`、`INTERNAL_ERROR`。用户消息不得拼接上游响应体或秘密；详细错误只在服务端日志中记录，且先脱敏。

### 3.2 认证端点

| 方法与路径 | 认证 | 请求/响应 |
|---|---|---|
| `POST /api/auth/login` | 否 | 请求 `{"password":"管理员密码"}`；成功设置 `hme_session` Cookie，响应 `{"csrf_token":"随机值","expires_at":"RFC3339"}` |
| `GET /api/auth/session` | Cookie | 响应 `{"csrf_token":"随机值","expires_at":"RFC3339"}`；无效时 401 |
| `POST /api/auth/logout` | Cookie + CSRF | 删除服务端会话并清除 Cookie，响应 `{"logged_out":true}` |

Cookie 固定属性：名称 `hme_session`、`Path=/`、`HttpOnly`、`SameSite=Strict`、不设置 `Domain`；`Secure` 由 `ICLOUD_HME_SECURE_COOKIE` 控制。前端所有请求使用 `credentials: "same-origin"`，POST/PUT/PATCH/DELETE 发送 `X-CSRF-Token`。

### 3.3 公开账号 DTO

任何账号响应只能包含：

```json
{
  "id":"acc_12345678",
  "name":"主号",
  "real_email":"owner@example.com",
  "icloud_email":"owner@icloud.com",
  "host":"icloud.com",
  "status":"active",
  "alias_total":15,
  "alias_active":12,
  "has_cookies":true,
  "has_app_password":true,
  "has_proxy":false,
  "last_validated":"2026-08-04T09:00:00+08:00",
  "status_message":"",
  "created_at":"2026-08-01T09:00:00+08:00"
}
```

禁止出现字段：`cookies`、`app_password`、`proxy`。代理只暴露 `has_proxy`；编辑页面不回显代理值。
`status_message` 只能由 status 映射为固定文案（pending 为“等待配置或验证凭据”，error 为“凭据验证失败”）；不得把内部 `LastError` 或上游错误原文放入 Summary。

### 3.4 账号端点

- `GET /api/accounts`：返回 `Summary[]`，排序为 active → pending → error，同状态按 name、id。
- `POST /api/accounts`：请求 `{name, icloud_email, host, proxy, cookies}`；`cookies` 可省略且为原始 JSON 文本或 Cookie Header 文本；返回 `Summary`。
- `PATCH /api/accounts/:id`：只接受可选的 `name`、`icloud_email`、`host`；至少一个字段存在。
- `PUT /api/accounts/:id/proxy`：请求 `{"proxy":"http://user:pass@host:port"}`；空字符串表示清除；响应只返回更新后的 `Summary`。
- `PUT /api/accounts/:id/cookies`：`cookies` 同时兼容字符串和对象；前端发送字符串；响应只返回 `Summary`。
- `POST /api/accounts/:id/password`：保持现有请求 `{icloud_email, app_password}`；响应返回 `Summary`。
- `POST /api/accounts/:id/login`：保持 `{password, otp_code?}`；需要 OTP 时返回 409/`OTP_REQUIRED`，验证码错误返回 401/`OTP_INVALID`，成功只返回 `Summary`，绝不返回 Cookies。
- `DELETE /api/accounts/:id`：保持现有行为；不存在返回 404/`ACCOUNT_NOT_FOUND`。

校验：name 去空白后 1–64 字符；host 只能是 `icloud.com` 或 `icloud.com.cn`；邮箱用 `net/mail.ParseAddress` 并要求地址值等于输入；proxy 为空或为 `http`、`https`、`socks5` URL；请求体上限 1 MiB。

### 3.5 现有业务端点

保留 `POST /api/create`、`GET /api/aliases`、三个 alias action、`GET /api/inbox` 和 `POST /api/reload`。新增服务端校验：alias label 最长 200 字符；anonymous ID 必须非空且 URL 解码后长度不超过 256；inbox `limit` 为 1–100，`days` 为 1–90，非法整数直接返回 400，不再静默变成 0。

现有字段风格不统一是兼容性事实：账号和 inbox 外层使用 snake_case，alias 对象使用 `anonymousId`/`createdAt`，创建结果使用 `created_at`。前端类型必须准确反映，不在本功能中顺手改名。

---

### Task 1: 建立安全账号 DTO、可编辑输入与并发快照

**Files:**
- Create: `internal/account/public.go`
- Create: `internal/account/public_test.go`
- Modify: `internal/account/manager.go`

**Interfaces:**
- Produces: `func (a *Account) Summary() Summary`
- Produces: `func (m *Manager) ListSummaries() []Summary`
- Produces: `func (m *Manager) AddAccountWithInput(AddAccountInput) (Summary, error)`
- Produces: `func (m *Manager) UpdateMetadata(string, UpdateAccountInput) (Summary, error)`
- Produces: `func (m *Manager) UpdateProxy(string, string) (Summary, error)`
- Existing `AddAccount(name, cookieInput, host, proxy)` remains as a compatibility wrapper until all callers migrate.

- [ ] **Step 1: 写 DTO 脱敏失败测试**

在 `public_test.go` 构造同时含 Cookie、App Password 和带凭据代理的 `Account`，序列化 `Summary()`，逐一断言三个秘密字符串都不存在，并断言 `has_cookies/has_app_password/has_proxy` 为 true：

```go
func TestSummaryDoesNotSerializeSecrets(t *testing.T) {
	acc := &Account{
		ID: "acc_test", Name: "主号",
		Cookies: map[string]string{"token": "cookie-secret"},
		AppPassword: "app-secret",
		Proxy: "http://user:proxy-secret@example.com:8080",
	}
	summary := acc.Summary()
	raw, err := json.Marshal(summary)
	if err != nil { t.Fatal(err) }
	for _, secret := range []string{"cookie-secret", "app-secret", "proxy-secret"} {
		if strings.Contains(string(raw), secret) { t.Fatalf("响应泄露秘密 %q: %s", secret, raw) }
	}
	if !summary.HasCookies || !summary.HasAppPassword || !summary.HasProxy {
		t.Fatalf("凭据状态错误: %+v", summary)
	}
}
```

- [ ] **Step 2: 写输入校验与稳定排序失败测试**

覆盖空 name、非法 host、非法邮箱、非法代理 scheme、代理清除，以及 active/pending/error + name/id 稳定排序。使用 `t.TempDir()` 创建 Manager；无 Cookie 的添加路径不得访问网络。

- [ ] **Step 3: 运行测试确认失败原因正确**

Run: `go test ./internal/account -run 'TestSummary|TestAddAccountWithInput|TestUpdateMetadata|TestListSummaries' -v`

Expected: FAIL，原因是 `Summary`、输入类型或新方法未定义，而不是 Go 环境或网络失败。

- [ ] **Step 4: 实现明确的公开类型与输入类型**

`public.go` 中只定义以下字段；不要嵌入 `Account`：

```go
type Summary struct {
	ID string `json:"id"`
	Name string `json:"name"`
	RealEmail string `json:"real_email"`
	ICloudEmail string `json:"icloud_email"`
	Host string `json:"host"`
	Status string `json:"status"`
	AliasTotal int `json:"alias_total"`
	AliasActive int `json:"alias_active"`
	HasCookies bool `json:"has_cookies"`
	HasAppPassword bool `json:"has_app_password"`
	HasProxy bool `json:"has_proxy"`
	LastValidated string `json:"last_validated"`
	StatusMessage string `json:"status_message,omitempty"`
	CreatedAt string `json:"created_at"`
}

type AddAccountInput struct {
	Name string
	ICloudEmail string
	CookieInput string
	Host string
	Proxy string
}

type UpdateAccountInput struct {
	Name *string
	ICloudEmail *string
	Host *string
}
```

实现前述校验和排序；对代理错误只返回“代理地址格式无效”，不得把原始 URL 写入错误。
`Summary()` 必须忽略内部 `LastError`，并按上一节映射固定的 `StatusMessage`，避免 URL、token 或代理凭据从上游错误泄露。

- [ ] **Step 5: 修正 Manager 的共享数据访问**

将互斥锁改为 `sync.RWMutex`；所有从 map 取出的 Account 在解锁前做结构体副本和 Cookies map 深拷贝；网络调用只使用快照；网络完成后重新加锁，以 id 查找当前对象后写回。`GetAccount`、`HMEClient`、`MailClient`、`WebMailClient`、`UpdateCookies`、`SetAppPassword`、`HMEClientWithPassword` 全部遵循该规则。

- [ ] **Step 6: 运行包测试与竞态检测**

Run: `go test -race ./internal/account -v`

Expected: PASS，且无 `DATA RACE`。

- [ ] **Step 7: 提交**

```bash
git add internal/account/manager.go internal/account/public.go internal/account/public_test.go
git commit -m "refactor: add safe account summaries"
```

---

### Task 2: 实现独立的管理员会话、CSRF 与登录限流

**Files:**
- Create: `internal/auth/manager.go`
- Create: `internal/auth/manager_test.go`
- Create: `internal/auth/limiter.go`
- Create: `internal/auth/limiter_test.go`

**Interfaces:**
- Produces: `auth.Options`、`auth.Session`、`auth.Manager`
- Produces: `Login(password) (sessionID string, session Session, ok bool)`
- Produces: `Validate(sessionID) (Session, bool)`、`ValidateCSRF(sessionID, token) bool`、`Logout(sessionID)`
- Produces: `Limiter.Allow(key) (allowed bool, retryAfter time.Duration)`、`Limiter.Success(key)`

- [ ] **Step 1: 写会话生命周期失败测试**

用可注入时钟和 `bytes.NewReader` 形式的确定性随机源覆盖：错误密码；正确密码；两个登录 token 不相同；会话过期；正确/错误 CSRF；退出立即失效。断言原始 session ID 与 CSRF 不作为 map key 保存。

- [ ] **Step 2: 运行会话测试确认失败**

Run: `go test ./internal/auth -run TestManager -v`

Expected: FAIL，原因是 auth package 尚未实现。

- [ ] **Step 3: 实现会话 Manager**

使用以下公共类型和默认值：

```go
type Options struct {
	Password string
	TTL time.Duration
	Now func() time.Time
	Random io.Reader
}

type Session struct {
	CSRFToken string
	ExpiresAt time.Time
}

const DefaultTTL = 12 * time.Hour
```

构造器拒绝空密码和短于 12 字符的密码；启动时用随机 salt + `argon2.IDKey` 保存管理员密码派生值，登录时常量时间比较。session ID 与 CSRF 各用 `crypto/rand` 生成 32 字节并编码为 base64url；会话 map 只用 session ID 的 SHA-256 作为键，记录值保存 CSRF 与过期时间且永不序列化或记录日志，CSRF 使用常量时间比较。每次公开操作先清理过期会话，所有 map 访问受 mutex 保护。
Argon2id 参数固定为 time=1、memory=64*1024 KiB、threads=4、keyLen=32；最多保留 32 个有效会话，新登录超过上限时淘汰最早过期的会话，防止内存无界增长。

- [ ] **Step 4: 写限流失败测试**

固定窗口为 15 分钟，同一 key 最多 5 次失败；第 6 次返回不超过 15 分钟的 retryAfter；时钟越过窗口或调用 `Success` 后恢复。不同 key 互不影响。

- [ ] **Step 5: 实现限流并运行竞态测试**

Run: `go test -race ./internal/auth -v`

Limiter 每次访问时删除已过窗口的 key，最多保留 10,000 个 key；达到上限时先淘汰最早窗口，避免伪造来源地址造成内存 DoS。
Expected: PASS，无竞态。

- [ ] **Step 6: 提交**

```bash
git add internal/auth
git commit -m "feat: add admin session security"
```

---

### Task 3: 把认证、安全中间件和安全账号契约接入 Gin

**Files:**
- Create: `internal/server/auth.go`
- Create: `internal/server/middleware.go`
- Create: `internal/server/response.go`
- Create: `internal/server/account_handlers.go`
- Create: `internal/server/backend.go`
- Create: `internal/server/backend_test.go`
- Create: `internal/server/auth_test.go`
- Create: `internal/server/account_handlers_test.go`
- Modify: `internal/server/server.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: Task 1 的 `account.Summary` 与写方法；Task 2 的 `auth.Manager` 和 `auth.Limiter`
- Produces: `Backend` 接口和生产 `managerBackend`，handler 测试使用内存 fake，禁止访问 Apple 网络
- Produces: `server.Config` 与 `func New(*account.Manager, Config) (*Server, error)`
- Produces: unexported `newWithBackend(Backend, Config)` 仅供同包测试注入 fake；生产 `New` 始终包装真实 Manager
- Produces: 第 3 节冻结的认证和账号 HTTP 契约

`backend.go` 的边界固定为高层业务动作，不能把具体 `*hme.Client` 或 `*mail.Client` 暴露给 handler：

```go
type InboxQuery struct {
	AccountID string
	Alias string
	Limit int
	Days int
}

type InboxResult struct {
	AccountID string `json:"account_id"`
	Alias string `json:"alias,omitempty"`
	Count int `json:"count"`
	Messages []mail.Message `json:"messages"`
	Method string `json:"method"`
}

type Backend interface {
	ListAccounts() []account.Summary
	AddAccount(account.AddAccountInput) (account.Summary, error)
	UpdateAccount(string, account.UpdateAccountInput) (account.Summary, error)
	UpdateProxy(string, string) (account.Summary, error)
	UpdateCookies(string, string) (account.Summary, error)
	SetAppPassword(string, string, string) (account.Summary, error)
	LoginAccount(string, string, string) (account.Summary, error)
	RemoveAccount(string) bool
	CreateAlias(string, string) (*hme.CreateResult, error)
	ListAliases(string) ([]hme.Alias, error)
	SetAliasActive(string, string, bool) (bool, error)
	DeleteAlias(string, string) error
	ListInbox(InboxQuery) (InboxResult, error)
	Reload() error
}
```

- [ ] **Step 1: 写认证边界失败测试**

用 `httptest` + 实现 `Backend` 的内存 fake 创建 Server；另用 `account.NewManager(t.TempDir())` 验证生产适配器的本地账号方法。覆盖：

1. 不带 Cookie 调 `GET /api/accounts` 返回 401/`AUTH_REQUIRED`。
2. 错误管理员密码返回 401/`INVALID_CREDENTIALS`，且不设置 Cookie。
3. 正确登录设置 HttpOnly/SameSite=Strict Cookie，响应包含 CSRF 与过期时间。
4. Cookie 有效时 GET 成功；无 CSRF 的 POST 返回 403/`CSRF_INVALID`；正确 Header 成功。
5. 退出后同一 Cookie 立即 401。
6. 同 IP 连续 6 次失败时第 6 次返回 429/`RATE_LIMITED` 和 `Retry-After`。

- [ ] **Step 2: 写秘密响应失败测试**

把含 `Cookies`、`AppPassword`、带凭据 `Proxy` 的账号写进测试数据目录后，调用账号列表并断言响应字节不包含秘密；再把 data 解码为 `map[string]any`，精确断言不存在键 `cookies`、`app_password`、`proxy`（不能用子串判断，因为合法键 `has_cookies`/`has_proxy` 会命中）。为账号 iCloud 登录成功响应抽出只接收 `account.Summary` 的响应 helper，并做相同断言，避免测试访问 Apple 网络。

- [ ] **Step 3: 运行 server 测试确认失败**

Run: `go test ./internal/server -run 'TestAuth|TestAccountResponse' -v`

Expected: FAIL，原因是新 Config、路由或错误码不存在。

- [ ] **Step 4: 拆分响应与账号 handler**

把统一响应移到 `response.go`：

```go
type apiResp struct {
	Success bool `json:"success"`
	Code string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Data any `json:"data,omitempty"`
}

func failCode(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, apiResp{Success: false, Code: code, Message: message})
}
```

把账号 handler 搬入 `account_handlers.go`，只返回 `Summary`。`PUT cookies` 使用自定义 JSON 解码同时接受字符串和 `map[string]string`，两种输入最终都交给 `account.ParseCookieInput`；不得把原始输入放入错误。
把现有 handler 内的 Manager/HME/Mail 编排原样移到 `managerBackend`；handler 只做绑定、校验、调用 Backend 和响应映射。fake Backend 为每个方法提供确定性返回与调用记录，使账号密码、alias 和 inbox handler 测试完全离线。

- [ ] **Step 5: 注册公开与受保护路由组**

`/api/auth/login` 公开；`/api/auth/session` 只验证会话；所有其他 `/api` 路由统一挂 `requireSession`，非 GET/HEAD/OPTIONS 再挂 CSRF 校验。不要逐路由手工复制认证逻辑。Gin 启动后调用 `SetTrustedProxies(nil)`，登录限流 key 使用 `RemoteAddr` 解析出的真实连接 IP，不信任任意 `X-Forwarded-For`。

- [ ] **Step 6: 增加 HTTP 防护**

全局设置 `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、`Permissions-Policy: camera=(), microphone=(), geolocation=()`。API 额外设置 `Cache-Control: no-store`。JSON body 用 `http.MaxBytesReader` 限为 1 MiB。

- [ ] **Step 7: 修复参数与上游错误映射**

实现第 3 节校验；OTP 缺失通过现有错误文本“需要提供 OTP”映射为 409/`OTP_REQUIRED`，验证码失败映射为 401/`OTP_INVALID`，iCloud 401/403 映射为 401/`UPSTREAM_UNAUTHORIZED`，其余上游错误为 502/`UPSTREAM_FAILURE`。日志只记录 error code、account id、route 和状态，不记录 request body。

- [ ] **Step 8: 修改启动配置**

定义：

```go
type Config struct {
	Debug bool
	AdminPassword string
	SessionTTL time.Duration
	SecureCookie bool
}
```

`main.go` 从 `ICLOUD_HME_ADMIN_PASSWORD` 读取密码，空值或短于 12 字符直接 `log.Fatal`；解析 `ICLOUD_HME_SESSION_TTL`，默认 `12h`，范围 `15m`–`168h`；解析 `ICLOUD_HME_SECURE_COOKIE`，默认 false。构造 Server 后执行 `os.Unsetenv("ICLOUD_HME_ADMIN_PASSWORD")`。不要新增密码命令行参数，避免出现在进程列表。

- [ ] **Step 9: 运行后端检查**

Run: `gofmt -w main.go internal/account internal/auth internal/server`

Run: `go test -race ./internal/account ./internal/auth ./internal/server -v`

Run: `go vet ./...`

Expected: 全部 PASS；无秘密出现在测试响应日志。

- [ ] **Step 10: 提交**

```bash
git add main.go internal/server
git commit -m "feat: secure management api"
```

---

### Task 4: 内嵌静态资源并正确处理 SPA 路由

**Files:**
- Create: `internal/webui/embed.go`
- Create: `internal/webui/embed_test.go`
- Create: `internal/webui/dist/placeholder.txt`
- Modify: `internal/server/server.go`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `func Embedded() (fs.FS, error)`
- Produces: `func Handler(fsys fs.FS) http.Handler`
- Server 在未匹配 API 的 GET/HEAD 请求上使用该 Handler

- [ ] **Step 1: 写静态资源失败测试**

用 `fstest.MapFS` 注入 `index.html`、`assets/app-abc.js`，覆盖 `/`、`/accounts` SPA fallback、真实 asset、缺失 asset、`POST /accounts` 和 `/api/not-found`。断言 index 为 `no-cache`，哈希 asset 为 `public, max-age=31536000, immutable`，API 404 始终返回 JSON 而不是 HTML。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/webui ./internal/server -run 'TestEmbedded|TestSPA' -v`

Expected: FAIL，原因是 webui package 尚未定义。

- [ ] **Step 3: 实现 embed 与 fallback**

`embed.go` 使用：

```go
//go:embed dist/*
var embedded embed.FS

func Embedded() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
```

Handler 只允许 GET/HEAD；路径先 `path.Clean` 并拒绝 `..`；存在文件就按扩展名服务，不存在且路径不含文件扩展名时返回 `index.html`；生产 dist 未生成时返回清晰的 503 中文纯文本，而不是 panic。`placeholder.txt` 仅用于让干净 checkout 的 `go build` 可编译。

- [ ] **Step 4: 接入 Gin NoRoute**

业务 API 404 在 `/api` 组内用 JSON handler 兜底；根路径和 SPA 路径交给 webui。绝不允许 `NoRoute` 把拼错的 API 路径变成 200 HTML。

- [ ] **Step 5: 更新忽略规则**

新增 `/web/node_modules/`、`/internal/webui/dist/*`，再用 `!/internal/webui/dist/placeholder.txt` 放行占位文件。保留前端源码；若保留全局 `*.js` 忽略，ESLint 配置必须使用 `.mjs`。

- [ ] **Step 6: 运行测试并提交**

Run: `go test ./internal/webui ./internal/server -v`

```bash
git add .gitignore internal/webui internal/server/server.go
git commit -m "feat: embed management ui assets"
```

---

### Task 5: 创建 React/TypeScript 工程、API 类型和基础设计系统

**Files:**
- Create: `web/package.json`
- Create: `web/package-lock.json`
- Create: `web/index.html`
- Create: `web/public/placeholder.txt`
- Create: `web/tsconfig.json`
- Create: `web/tsconfig.app.json`
- Create: `web/tsconfig.node.json`
- Create: `web/vite.config.ts`
- Create: `web/eslint.config.mjs`
- Create: `web/src/main.tsx`
- Create: `web/src/App.tsx`
- Create: `web/src/styles.css`
- Create: `web/src/api/types.ts`
- Create: `web/src/api/client.ts`
- Create: `web/src/api/client.test.ts`
- Create: `web/src/test/setup.ts`
- Create: `web/src/test/server.ts`
- Create: `web/src/test/handlers.ts`

**Interfaces:**
- Produces: `request<T>(path, init?) => Promise<T>`
- Produces: 完整的 `ApiResponse<T>`、`ApiError`、`AccountSummary`、`Alias`、`InboxMessage` 类型
- Produces: 全页面共享的 CSS token 和可访问的基础布局

- [ ] **Step 1: 用官方模板创建并锁定依赖**

从仓库根运行：

```bash
npm create vite@latest web -- --template react-ts --no-interactive
npm --prefix web install react-router-dom
npm --prefix web install -D vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event msw eslint-plugin-jsx-a11y
```

检查生成的 `package-lock.json` 已锁定 React 19.2、Vite 8.1 和兼容版本；不要手工删除 lockfile。

- [ ] **Step 2: 配置统一脚本与输出目录**

`package.json` 的 scripts 必须包含：

```json
{
  "dev":"vite",
  "build":"tsc -b && vite build",
  "lint":"eslint .",
  "test":"vitest",
  "test:run":"vitest run",
  "check":"npm run lint && npm run test:run && npm run build"
}
```

`vite.config.ts` 把 `build.outDir` 设为 `../internal/webui/dist`、`emptyOutDir` 设为 true；dev server 的 `/api` 代理指向 `http://127.0.0.1:8081`；Vitest 使用 `jsdom` 和 `src/test/setup.ts`。
`web/public/placeholder.txt` 与 `internal/webui/dist/placeholder.txt` 使用相同固定内容；Vite 清空 outDir 后会从 public 目录重新复制该文件，保证构建不会把 tracked 占位文件标记为删除。

- [ ] **Step 3: 先写 API client 失败测试**

覆盖成功解包、非 2xx 的 `code/message/status`、非 JSON 响应、401 回调、GET 不加 CSRF、POST 自动加 CSRF、`credentials: same-origin`、AbortSignal。MSW fixture 必须使用第 3 节的真实字段名。

- [ ] **Step 4: 实现唯一 fetch 入口**

```ts
export interface ApiResponse<T> {
  success: boolean
  data?: T
  code?: string
  message?: string
}

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) { super(message) }
}
```

`request<T>` 统一设置 Accept，含 JSON body 时设置 Content-Type；使用内存中的 CSRF getter；只把服务器安全 message 交给 UI；网络失败显示“网络连接失败，请检查服务状态”。页面代码不得直接调用 fetch。

- [ ] **Step 5: 建立基础视觉与可访问性规则**

定义白/浅灰背景、蓝色主色、红色危险色、4/8/12/16/24/32 间距、8px 圆角、系统字体。实现 skip link、清晰 focus ring、44px 最小点击高度、`prefers-reduced-motion`、表格窄屏横向滚动。所有 input 有可见 label，状态不能只用颜色表达。

- [ ] **Step 6: 运行前端基线检查**

Run: `npm --prefix web run lint`

Run: `npm --prefix web run test:run`

Run: `npm --prefix web run build`

Expected: 全部 PASS；`internal/webui/dist/index.html` 和哈希 assets 已生成。

- [ ] **Step 7: 提交**

```bash
git add web internal/webui/dist/placeholder.txt
git commit -m "feat: scaffold management web app"
```

生成的 dist 仍被忽略，不提交；提交 `package-lock.json`。

---

### Task 6: 实现登录、会话恢复、退出和应用壳

**Files:**
- Create: `web/src/auth/AuthProvider.tsx`
- Create: `web/src/auth/AuthProvider.test.tsx`
- Create: `web/src/pages/LoginPage.tsx`
- Create: `web/src/pages/LoginPage.test.tsx`
- Create: `web/src/components/AppShell.tsx`
- Create: `web/src/components/ToastProvider.tsx`
- Create: `web/src/components/AsyncState.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/api/client.ts`

**Interfaces:**
- Produces: `useAuth()`，状态为 `checking | anonymous | authenticated`
- Produces: `login(password)`、`logout()`、内存 CSRF getter
- Produces: 受保护路由 `/accounts`、`/aliases`、`/inbox`

- [ ] **Step 1: 写认证 UI 失败测试**

覆盖首次 GET session 的 loading；401 跳登录；登录成功进入 `/accounts`；刷新会话恢复；错误密码显示服务器消息但不回显密码；任意 API 401 清空认证；退出调用 CSRF 请求并回登录页。

- [ ] **Step 2: 运行测试确认失败**

Run: `npm --prefix web run test:run -- AuthProvider LoginPage`

Expected: FAIL，原因是组件/Provider 未定义。

- [ ] **Step 3: 实现 AuthProvider**

CSRF 仅放 React 内存状态；初始化调用 `GET /api/auth/session`；登录成功保存 csrf_token；退出无论请求结果如何都清空本地状态。不要把管理员密码或 CSRF 写入 Web Storage。401 全局回调不得在 session 探测本身形成重定向循环。

- [ ] **Step 4: 实现登录页和 AppShell**

登录页只有产品名、密码、提交按钮和错误区；管理员密码使用 `type=password` 与 `autoComplete=current-password`。AppShell 提供账号/别名/收件箱导航和退出按钮；当前路由使用 `aria-current=page`；移动端导航可换行但不得隐藏核心入口。

- [ ] **Step 5: 实现统一反馈组件**

AsyncState 明确区分 loading、empty、error + retry；Toast 使用 `role=status`，错误区域使用 `role=alert`；Toast 不显示请求 body 或秘密。网络请求期间禁用重复提交。

- [ ] **Step 6: 运行检查并提交**

Run: `npm --prefix web run check`

```bash
git add web/src
git commit -m "feat: add admin login experience"
```

---

### Task 7: 实现账号与凭据管理页面

**Files:**
- Create: `web/src/pages/AccountsPage.tsx`
- Create: `web/src/pages/AccountsPage.test.tsx`
- Create: `web/src/components/AccountFormDialog.tsx`
- Create: `web/src/components/CookieDialog.tsx`
- Create: `web/src/components/ICloudLoginDialog.tsx`
- Create: `web/src/components/AppPasswordDialog.tsx`
- Create: `web/src/components/ProxyDialog.tsx`
- Create: `web/src/components/ConfirmDialog.tsx`
- Modify: `web/src/api/types.ts`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: 第 3.4 节账号 API、Task 5 request、Task 6 Toast/Dialog 规则
- Produces: 完整账号工作流；后续页面通过 URL query `account_id` 共享账号上下文

- [ ] **Step 1: 写列表和添加失败测试**

MSW 返回 active/pending/error 三个账号，断言状态文字、凭据配置标志、邮箱和 alias 计数可见，秘密字段不可见。测试添加表单 name/icloud_email/host 校验、可选 Cookie/Proxy、请求期间禁用、成功刷新、失败保留非秘密字段。

- [ ] **Step 2: 写凭据和删除失败测试**

覆盖 Cookie raw string 提交后 textarea 清空；iCloud 登录收到 `OTP_REQUIRED` 后只显示 OTP 输入并可重试；App Password/代理提交后清空；代理从不回显；删除前要求输入账号 name 精确匹配；取消不发请求。

- [ ] **Step 3: 运行测试确认失败**

Run: `npm --prefix web run test:run -- AccountsPage`

Expected: FAIL，原因是账号页面和 dialogs 不存在。

- [ ] **Step 4: 实现账号列表与编辑**

桌面用表格、窄屏允许水平滚动；列为名称/邮箱/状态/别名/凭据状态/最近验证/操作。状态显示中文文本。添加与基本信息编辑分别调用 POST/PATCH；Host 只能选择全球区或中国区。成功后重新拉取列表，不做易错的局部字段拼接。

- [ ] **Step 5: 实现秘密输入 dialogs**

每个秘密单独组件并保持最短生命周期：打开时空值，关闭和 finally 时清空。Cookie 使用 `textarea`、`spellCheck=false`；App Password 与 iCloud 密码为 password input；OTP `inputMode=numeric`、限制 6 位。浏览器端只做长度/格式提示，最终校验由后端完成。

- [ ] **Step 6: 实现破坏性确认和账号上下文**

删除 dialog 明确说明本地账号配置会被移除但不会删除 Apple 账号；输入名称匹配后才启用按钮。跳转别名/收件箱时把账号 id 写入 query string；只允许非秘密 id 进入 URL。

- [ ] **Step 7: 运行检查并提交**

Run: `npm --prefix web run check`

```bash
git add web/src/pages/AccountsPage* web/src/components web/src/api/types.ts web/src/App.tsx
git commit -m "feat: add account management ui"
```

---

### Task 8: 实现别名生命周期管理页面

**Files:**
- Create: `web/src/pages/AliasesPage.tsx`
- Create: `web/src/pages/AliasesPage.test.tsx`
- Create: `web/src/components/CreateAliasDialog.tsx`
- Modify: `web/src/api/types.ts`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `GET /api/accounts`、`GET /api/aliases?account_id=`、`POST /api/create` 和现有 alias actions
- Produces: 账号选择、客户端搜索/状态筛选、创建/停用/激活/删除

- [ ] **Step 1: 写加载、筛选和创建失败测试**

覆盖无账号引导、账号切换、loading/empty/error/retry、按 email/label 大小写不敏感搜索、active 状态筛选、创建标签为空/200 字符边界、创建成功后刷新并可复制邮箱。

- [ ] **Step 2: 写状态操作失败测试**

停用与激活必须显示目标邮箱并二次确认；删除要求输入完整 alias email，使用 `anonymousId` 构造 URL 且 `encodeURIComponent`；操作中只禁用目标行，失败后保留列表并显示错误。

- [ ] **Step 3: 运行测试确认失败**

Run: `npm --prefix web run test:run -- AliasesPage`

Expected: FAIL，原因是页面不存在。

- [ ] **Step 4: 实现页面**

URL query 的 `account_id` 优先；不存在或无权限的 id 回退到第一个账号并替换 URL。列表列为邮箱、标签、状态、创建时间、操作；日期用 `Intl.DateTimeFormat('zh-CN')`，解析失败显示原文本。复制功能失败时给出可选中文本，不静默失败。

- [ ] **Step 5: 保证删除和文本渲染安全**

所有 alias 字段按 React 文本节点渲染，不设置 HTML。删除按钮使用危险色且不与停用按钮并排成相同样式；成功后刷新服务端数据。

- [ ] **Step 6: 运行检查并提交**

Run: `npm --prefix web run check`

```bash
git add web/src/pages/AliasesPage* web/src/components/CreateAliasDialog.tsx web/src/api/types.ts web/src/App.tsx
git commit -m "feat: add alias management ui"
```

---

### Task 9: 实现收件箱摘要页面

**Files:**
- Create: `web/src/pages/InboxPage.tsx`
- Create: `web/src/pages/InboxPage.test.tsx`
- Modify: `web/src/api/types.ts`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `GET /api/inbox?account_id=&alias=&limit=&days=` 与可选 aliases 列表
- Produces: 账号/别名/limit/days 筛选和纯文本邮件摘要列表

- [ ] **Step 1: 写筛选与响应状态失败测试**

覆盖账号必选；alias 可空；limit 1/20/100；days 1/7/90；提交时 query 经过 `URLSearchParams`；展示 `method=imap` 或 `web_api`；空列表、网络错误、401；账号变化时清空旧邮件而不是显示串号数据。

- [ ] **Step 2: 写 XSS 与并发失败测试**

fixture 的 subject/preview 包含 `<img src=x onerror=alert(1)>`，断言它作为文本出现且没有 img 节点。快速连续切换两次筛选，第一请求晚返回时不得覆盖第二请求；使用 AbortController。

- [ ] **Step 3: 运行测试确认失败**

Run: `npm --prefix web run test:run -- InboxPage`

Expected: FAIL，原因是页面不存在。

- [ ] **Step 4: 实现筛选与摘要列表**

筛选状态提交后写入 URL query，便于刷新恢复；只保存账号 id、alias、limit、days。邮件项显示 subject、from、to、date、preview 和读取方式；空 subject 显示“（无主题）”。不提供正文展开或 HTML 解析。

- [ ] **Step 5: 运行检查并提交**

Run: `npm --prefix web run check`

```bash
git add web/src/pages/InboxPage* web/src/api/types.ts web/src/App.tsx
git commit -m "feat: add inbox management ui"
```

---

### Task 10: 完成构建、CI、文档与端到端验收

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `Dockerfile`
- Modify: `build.sh`
- Modify: `README.md`
- Modify: `API.md`
- Modify: `accounts.json.template`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: Task 4 的 dist 目录、Task 5 的 npm scripts、全部 Go/前端测试
- Produces: 可重复的本地、CI、Docker 和 Release 单二进制构建

- [ ] **Step 1: 先证明 Go 构建会嵌入前端**

Run: `npm --prefix web ci && npm --prefix web run build && go build -o build/icloud-hme-ui-test .`

启动时使用测试数据目录和 12 字符以上密码，访问 `/`，断言响应 HTML 引用了哈希 asset；直接访问 `/aliases?account_id=acc_test` 也返回 index；`/api/not-found` 返回 JSON 404。

- [ ] **Step 2: 改造 Docker 为三阶段构建**

第一阶段 `node:22-alpine` 在 `/src/web` 执行 `npm ci` 和 `npm run build`，输出 `/src/internal/webui/dist`。第二阶段 `golang:1.26-alpine` 先复制 Go module 下载依赖，再复制源码和第一阶段 dist 后执行 Go build。第三阶段保持 Alpine 运行时；Dockerfile 不写入默认管理员密码。

- [ ] **Step 3: 更新本地构建脚本**

`build.sh` 在 Go build 前依次执行 `npm --prefix web ci`、`npm --prefix web run test:run`、`npm --prefix web run build`、`go test ./...`。继续输出 Linux amd64 二进制；清理目标只能是解析后的仓库内 `build/`。

- [ ] **Step 4: 增加常规 CI**

`ci.yml` 在 pull_request 和 main push 上运行：Node 22 + npm cache；`npm ci`；lint；Vitest；Vite build；setup-go 1.26；`go test -race ./...`；`go vet ./...`；`go build ./...`。前端 build 必须先于任何需要 embed index 的验收，但占位文件保证纯 Go 编译不会因 glob 为空失败。

- [ ] **Step 5: 更新 Release 工作流**

每个 binary matrix job 在 Go build 前 setup-node 22、`npm ci`、`npm run build`；Docker job沿用新 Dockerfile；Release artifact 名称保持现状。用一次非发布分支手工 workflow 验证所有五个二进制都含 UI 后再打 tag。

- [ ] **Step 6: 更新文档和模板**

README 加入：管理界面入口、必填环境变量、最小密码长度、Cookie secure 配置、源码双工具链构建、Docker `-e ICLOUD_HME_ADMIN_PASSWORD=change-this-before-running-2026` 示例（明确标注不可照抄）、升级后 API 需要登录的 breaking change。API.md 写入第 3 节全部契约和 curl 的 cookie jar + CSRF 流程。`accounts.json.template` 改成 Manager 当前实际接受的 map wrapper 格式，移除过时的 cookies 数组与 `app_passwords` 数组。

- [ ] **Step 7: 执行自动化质量门**

```bash
npm --prefix web ci
npm --prefix web run lint
npm --prefix web run test:run
npm --prefix web run build
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

Expected: 每条命令 exit 0；`git diff` 只含本计划文件表中的变更；`rg -n "cookie-secret|app-secret|proxy-secret"` 只命中测试 fixture，不命中生产响应或日志。

- [ ] **Step 8: 执行人工验收矩阵**

1. 不设置 `ICLOUD_HME_ADMIN_PASSWORD`：进程拒绝启动并给出中文配置提示。
2. 错误密码 5 次可重试，第 6 次 429；正确登录后恢复。
3. 刷新 `/accounts` 会话恢复；退出后浏览器返回登录页。
4. 添加无 Cookie 账号后可通过 iCloud 密码登录；OTP_REQUIRED 流程可继续。
5. 浏览器 Network 面板的任何响应都看不到 Cookie、App Password 或代理凭据。
6. Cookie/App Password/代理输入在 dialog 关闭和提交后被清空，Web Storage 为空。
7. 创建、停用、激活、删除 alias 均更新列表；删除要求输入邮箱。
8. inbox 的 IMAP/Web API 两种 method 均能显示；恶意 HTML fixture 只显示文本。
9. 375px 宽度可完成所有流程；键盘 Tab、Escape、Enter 可操作 dialog，焦点关闭后回到触发按钮。
10. `docker build` 后容器只需二进制和 data volume；Node 不在最终镜像中。

- [ ] **Step 9: 最终安全检查**

确认 Gin 不信任任意代理；生产 TLS 部署文档要求 secure cookie；CSP 无 `unsafe-inline`/`unsafe-eval`；所有 mutation 都有 CSRF 测试；没有 CORS `*`；没有密码 flag；没有原始秘密响应；没有 `dangerouslySetInnerHTML`。

- [ ] **Step 10: 提交**

```bash
git add .github Dockerfile build.sh README.md API.md accounts.json.template .gitignore
git commit -m "build: ship embedded management ui"
```

---

## 4. DeepSeek 执行约束

1. 从 Task 1 开始顺序执行；不要先画页面再补认证。
2. 每个 Task 先写失败测试，保存失败输出，再写最小实现。
3. 每个 Task 只修改其 Files 清单；发现必须跨范围时先更新本计划和文件清单。
4. 任何测试需要 Apple 网络都说明测试缝设计错误；单元测试必须用 temp data、fake clock、fake random、httptest 或 MSW。
5. 不修改 `internal/hme`、`internal/mail`、`internal/srp`，除非验收证明现有公开接口无法支持需求，并先获得人工确认。
6. 不提交 `internal/webui/dist` 生成物；只提交 `placeholder.txt` 和 `web/package-lock.json`。
7. 每次提交前运行该 Task 指定测试和 `git diff --check`；Task 10 再跑全量质量门。
8. 发现当前 API 文档与源码冲突时，以源码为基线、以第 3 节目标契约为最终状态，并在 API.md 明确升级差异。

## 5. 完成定义

只有同时满足以下条件才算完成：所有自动化命令通过；发布二进制离线包含 UI；四个用户流程验收通过；API 必须登录且 mutation 必须 CSRF；响应与浏览器存储无秘密；Docker/Release/README/API 文档同步；`git diff` 无范围外修改。
