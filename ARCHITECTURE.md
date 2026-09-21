# 项目结构与改动指南

> 本文以当前实现为准，用于定位改动边界。HTTP 契约的权威实现是 `internal/server/server.go` 的路由注册及对应 handler；前端类型入口为 `web/src/api/types.ts`。

## 1. 系统概览

`icloud-hme` 是单二进制的本地管理服务：Go/Gin 提供受保护的 JSON API，并内嵌 React SPA。它管理多个 iCloud 账号，调用 iCloud Web 接口管理 Hide My Email (HME) 别名，并通过 IMAP（优先）或 iCloud Web Mail API（回退）读取邮件。

```text
浏览器 (React SPA)
  │  Cookie hme_session + X-CSRF-Token（所有写操作）
  ▼
Gin Server /api ─────────────────────────────────────┐
  ├─ auth.Manager / auth.Limiter                       │ 管理员认证、会话、限流
  ├─ server.Backend（handler 的唯一业务依赖）           │
  │   └─ managerBackend                                │
  │       └─ account.Manager                           │ 多账号、配置与连接池
  │           ├─ hme.Client ───────── iCloud HME Web API
  │           ├─ mail.Client ──────── IMAP / 外部 IMAP
  │           └─ mail.WebClient ───── iCloud Web Mail API
  └─ autoTaskManager ─────────────────────────────────┘ 自动创建与审计日志

持久化：data/accounts.json、data/alias_task.json、data/alias_task_logs.json
```

## 2. 目录与职责

```text
.
├── main.go                         启动参数、环境安全配置、依赖装配
├── internal/
│   ├── account/                    账号聚合、持久化、公开 DTO、客户端工厂
│   ├── auth/                       管理员密码派生、内存会话、CSRF、登录限流
│   ├── hme/                        iCloud HME Cookie/SRP 客户端
│   ├── mail/                       IMAP、Web Mail、MIME 解析与连接池
│   ├── server/                     Gin 路由、handler、Backend、自动任务
│   ├── srp/                        Apple 登录使用的 SRP 协议实现
│   └── webui/                      通过 go:embed 分发前端产物与 SPA fallback
├── web/
│   ├── src/api/                    唯一 fetch 封装和 API DTO
│   ├── src/auth/                   会话探测、登录状态和 CSRF token 内存管理
│   ├── src/components/             可复用弹窗、菜单、通知和表单组件
│   ├── src/pages/                  账号、别名、任务、日志、收件箱页面
│   └── src/test/                   MSW、Vitest 共享测试设施
├── data/                           运行期数据，包含敏感凭据；不应提交
├── API.md                          对外 API 文档（部分内容落后于当前实现，见第 5 节）
├── Dockerfile                      前端检查/构建 + Go 检查/编译的多阶段镜像
├── docker-compose.yml              生产运行参数、挂载、安全限制与健康检查
└── build.sh                        本地全量检查及 Linux amd64 构建
```

## 3. 关键对象与改动入口

| 需求 | 首选改动位置 | 需要同步检查 |
|---|---|---|
| 新增/修改 HTTP 接口 | `internal/server/server.go`、对应 `*_handlers.go` | `Backend` 接口、fake backend、`web/src/api/types.ts`、`API.md`、handler 测试 |
| 修改账号字段或校验 | `internal/account/manager.go`、`internal/account/public.go` | `Summary` 脱敏、`accounts.json` 兼容、账号表单与测试 |
| 修改 HME 别名行为 | `internal/server/backend.go`、`internal/hme/client.go` | Cookie 刷新是否保存、上游错误映射、别名页 |
| 修改读信/分页/摘要 | `internal/server/backend.go`、`internal/mail/client.go` | `InboxResult`、`web/src/pages/InboxPage.tsx`、IMAP/Web API 降级语义 |
| 修改邮件 HTML/MIME 解析 | `internal/mail/preview.go`、`internal/mail/client.go` | `attachment_test.go`、`mime_edge_test.go`、preview 系列测试 |
| 修改自动创建任务 | `internal/server/auto_task.go`、`auto_task_logs.go`、`alias_task_handlers.go` | 原子持久化、停止通道、任务/日志测试、任务页 |
| 修改登录/会话安全 | `internal/auth/`、`internal/server/auth.go`、`middleware.go` | CSRF、Cookie 属性、限流、前端 `AuthProvider` |
| 修改前端 API 行为 | `web/src/api/client.ts`、`web/src/api/types.ts` | MSW handlers、相应页面测试；不要绕过 `request()` 直接 `fetch()` |
| 修改账号管理体验 | `web/src/pages/AccountsPage.tsx`、`web/src/components/` | 保持账号摘要脱敏；菜单操作通过 `MoreActionsDropdown` 分组，Cookie 输入复用 `SmartCookieInput` |

### 核心边界

- **HTTP handler 不直接依赖 iCloud/IMAP 客户端**：只调用 `server.Backend`。生产实现是 `managerBackend`，测试使用 fake backend。
- **`account.Account` 是内部敏感模型**：包含 Cookie、App Password、代理和外部邮箱密码；HTTP 响应只能使用 `account.Summary`。
- **所有 iCloud Cookie 刷新后必须持久化**：HME 业务调用后通过 `Manager.SaveCookies` 保存客户端持有的最新 Cookie。
- **自动任务状态写入必须原子化**：任务状态和日志分别由 `writeJSONAtomic` 写入，避免中断后产生半个 JSON 文件；任务分自主（每天 5–50 个自动分摊时刻，每次 1 个）与定时（每 20–1440 分钟创建 1–20 个）两类，同账号创建尝试至少间隔 20 分钟，且每天最多 50 个。
- **收件箱列表是两阶段加载**：先取 IMAP 信封，再由 `POST /api/inbox/previews` 分批补正文摘要；不要把正文抓取重新放回首屏列表路径。
- **账号页面局部状态优先**：列表刷新、弹窗目标与异步提交状态都位于 `AccountsPage` 及其弹窗内；当前规模无需引入 Redux 等全局状态库。

## 4. 认证与响应约定

### 管理员会话

| 项目 | 约定 |
|---|---|
| 登录 | `POST /api/auth/login`，唯一无需已有会话的写接口 |
| 会话 Cookie | `hme_session`；`HttpOnly`、`SameSite=Strict`、`Path=/`；TLS 反代时通过 `ICLOUD_HME_SECURE_COOKIE=true` 启用 `Secure` |
| CSRF | 除 GET/HEAD/OPTIONS 外，已认证 API 必须携带 `X-CSRF-Token` |
| 前端状态 | CSRF token 只保存在 `AuthProvider`/`api/client.ts` 的内存中 |
| 登录限流 | 按真实连接 IP，15 分钟内最多 5 次失败 |

### 统一响应

```json
{"success": true, "data": {}}
```

```json
{"success": false, "code": "VALIDATION_ERROR", "message": "参数错误"}
```

稳定错误映射集中在 `internal/server/response.go` 与 `backend.go`。新增接口应复用 `ok`、`failCode` 和 `backendFail`，不要另建响应结构。

## 5. 当前 HTTP 路由清单

除登录和会话查询外，以下所有 `/api` 路由均需要管理员会话；标记 **写** 的路由还需要 CSRF 头。

### 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 管理员登录；设置 Cookie，返回 csrf token（公开） |
| GET | `/api/auth/session` | 查询当前会话（公开路由，但需要有效 Cookie） |
| POST | `/api/auth/logout` | 退出登录（写） |

### 账号

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/accounts` | 返回脱敏的 `AccountSummary[]` |
| POST | `/api/accounts` | 新建账号（写） |
| PATCH | `/api/accounts/:id` | 修改名称、iCloud 邮箱或区域（写） |
| PUT | `/api/accounts/:id/proxy` | 设置/清除代理（写） |
| PUT | `/api/accounts/:id/cookies` | 更新并校验 Cookie（写） |
| POST | `/api/accounts/:id/password` | 设置并验证 iCloud App Password（写） |
| PUT | `/api/accounts/:id/mailbox` | 设置并验证外部 IMAP 收件邮箱（写） |
| POST | `/api/accounts/:id/login` | 使用 iCloud 密码与可选 OTP 获取 Cookie（写） |
| DELETE | `/api/accounts/:id` | 删除账号（写） |

### 别名与自动任务

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/alias-labels` | 获取人工创建与自动任务共用的标签名称库 |
| POST | `/api/create` | 使用名称库标签创建一个 HME 别名（写）；账号每天最多 50 个 |
| GET | `/api/aliases?account_id=…` | 获取账号别名列表 |
| POST | `/api/aliases/batch` | 批量停用、启用或删除，1–200 项（写） |
| POST | `/api/aliases/:id/deactivate` | 停用别名（写） |
| POST | `/api/aliases/:id/reactivate` | 启用别名（写） |
| DELETE | `/api/aliases/:id` | 删除别名（写） |
| GET | `/api/alias-tasks` | 查询自动创建任务 |
| POST | `/api/alias-tasks` | 新建任务（写） |
| PATCH | `/api/alias-tasks/:id` | 更新任务（写） |
| POST | `/api/alias-tasks/:id/toggle` | 启用/暂停任务（写） |
| DELETE | `/api/alias-tasks/:id` | 删除任务（写） |
| GET | `/api/alias-task-logs` | 倒序读取自动任务和批量操作日志 |

### 收件箱与系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/inbox` | 信封分页列表；`account_id` 必填，`limit=1..50`（默认 20），`offset=0..10000`，`days=0..3650`（0 为不限） |
| POST | `/api/inbox/previews?account_id=…` | 批量补摘要，body 为 `{"ids":[…]}`，最多 20 个 ID（写） |
| GET | `/api/inbox/:message_id?account_id=…` | 读取完整正文，仅走 IMAP |
| DELETE | `/api/inbox/:message_id?account_id=…` | 删除邮件，仅走 IMAP（写） |
| POST | `/api/reload` | 从磁盘重新加载 `accounts.json`（写） |

### 重要 DTO

| DTO | 定义位置 | 用途 |
|---|---|---|
| `AccountSummary` | `internal/account/public.go`、`web/src/api/types.ts` | 所有账号接口的安全响应，不含秘密 |
| `hme.Alias` / `Alias` | `internal/hme/client.go`、`web/src/api/types.ts` | HME 别名，字段使用 iCloud camelCase |
| `InboxResult` | `internal/server/backend.go`、`web/src/api/types.ts` | 邮件分页结果，包含 `total`、`offset`、`method` 和可选 `warning` |
| `mail.Message` / `FullMessage` | `internal/mail/client.go`、`web/src/api/types.ts` | 邮件摘要和完整正文 |
| `AliasTask` / `AliasTaskLog` | `internal/server/auto_task.go` | 自动创建状态（任务类型 mode、标签模式 label_mode、周期、目标、每日计数）与审计日志 |

### 现有文档差异

`API.md` 尚未覆盖当前已实现的外部邮箱、邮件详情/删除、预览批量接口和收件箱分页字段；其旧版 `limit`/`days` 约束也与当前实现不同。后续以本节和路由实现为准；任何接口改动应同步更新 `API.md`，避免再次分叉。

## 6. 外部调用与降级链路

### HME 别名

1. `account.Manager.HMEClient` 从账号快照创建 `hme.Client`。
2. `hme.Client.ValidateSession` 调用 iCloud setup validate，解析 HME service URL 与 DSID。
3. 别名操作经 `maildomainws` 的 HME 端点执行。
4. 服务端收到 Set-Cookie 后，`managerBackend` 保存更新后的 Cookie。

### 收件箱

```text
GET /api/inbox
  ├─ 有 IMAP 凭据/外部邮箱：连接池 IMAP → 仅取信封 → method=imap
  │    └─ POST /api/inbox/previews：按 UID 取部分正文
  └─ IMAP 不可用：Cookie Web API → method=web_api + warning
       └─ Web API 无服务端分页/日期筛选，服务端拉取后内存分页
```

注意：Web API 的别名筛选依赖主题/发件人等局部匹配，无法可靠按收件人过滤；完整邮件读取和删除只支持 IMAP 路径。

## 7. 构建、测试与发布

| 命令 | 作用 |
|---|---|
| `npm --prefix web run check` | 前端 ESLint、Vitest、TypeScript 与 Vite 构建 |
| `go test ./...` | Go 单元/集成测试 |
| `go test -race ./...` | Go 竞态检查（`build.sh` 使用） |
| `go vet ./...` | Go 静态检查 |
| `./build.sh` | 安装前端依赖、完整检查、构建 Linux amd64 二进制 |
| `docker compose up -d --build` | 构建并启动受限容器 |

前端构建产物固定输出到 `internal/webui/dist`，随后被 `go:embed` 编入二进制。修改前端后若要验证最终二进制，必须先运行前端构建再执行 Go build。

CI 分为 web 与 go 两个 job：web job 上传构建产物，go job 下载该产物后执行 race test、vet 与 build。

## 8. 数据与安全边界

- `data/accounts.json`、`alias_task.json`、`alias_task_logs.json` 是运行数据；账号配置中可能包含 Cookie、代理、App Password 与外部邮箱授权码。
- 账号配置文件权限为 `0600`；容器以只读根文件系统运行，仅 `/app/data` 可写。
- 日志、API 响应和前端状态不得输出/保存 Cookie、代理原文、App Password 或邮箱授权码。
- `internal/webui` 只服务 GET/HEAD；未知非 API 路径走 SPA fallback，未知 `/api/*` 返回 JSON 404。

## 9. 当前规模与测试基线

- 源码、测试、配置与文档（排除锁文件及内嵌前端产物）：约 **19,222 行**。
- 前端非测试源码：约 **5,610 行**。
- 测试文件：**35 个**。
- 最近验证：`go test ./...`、`go vet ./...`、`npm --prefix web run check` 均通过；前端 ESLint 有 2 个 `react-refresh/only-export-components` 警告，无错误。
