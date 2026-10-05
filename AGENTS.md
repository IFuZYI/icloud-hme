# icloud-hme 开发指南（AGENTS.md）

iCloud Hide My Email (HME) 多账号管理平台：单个 Go 二进制（Gin API + `go:embed` 内嵌 React SPA），管理多账号下的 HME 别名创建/批量操作与邮件读取（IMAP 优先、iCloud Web API 回退）。全仓库中文优先——代码注释、用户可见错误、文档、提交信息均为中文。

## 工具链

- Go 1.26+（`go.mod` 声明 `go 1.26`）、Node.js 22+（CI 用 22；本机 Node 26 已验证可构建）。
- 当前开发机**未安装 Go**（`go` 不在 PATH）：Go 测试/构建前需先装 Go 1.26+，或改用容器构建（见下）。
- 前端依赖未装时先 `npm --prefix web ci`（锁定版本，勿用 `npm install`）。

## 命令（均已从仓库配置中核实）

前端（仓库根执行；`npm --prefix web` 等价于在 `web/` 内执行）：

```bash
npm --prefix web ci            # 安装依赖
npm --prefix web run check     # 一键检查：lint + vitest + tsc/vite build
npm --prefix web run test:run  # 只跑测试
npm --prefix web run lint      # ESLint
npm --prefix web run dev       # Vite dev server，/api 代理到 127.0.0.1:8081
```

Go：

```bash
export ICLOUD_HME_ADMIN_PASSWORD='your-strong-password'  # 必填，≥8 字符，缺失拒启
go run main.go -debug          # 本地启动（默认 :8081，数据目录 ./data）
go test ./...                  # 全量测试
go test -race ./...            # 竞态检测
go vet ./...                   # 静态检查
```

完整构建（前端检查 → Go 测试/竞态/vet → Linux amd64 二进制到 `build/icloud-hme`）：

```bash
./build.sh
```

Docker：`docker compose up -d --build`，需 `.env` 提供 `ICLOUD_HME_ADMIN_PASSWORD`（compose 强制校验，缺失直接报错）。

## 架构与改动入口

- Go：`main.go` 装配 → `internal/server`（路由/handler/`Backend` 接口）→ `internal/account`（账号聚合与持久化）→ `internal/hme`（iCloud Web API 客户端）、`internal/mail`（IMAP/Web Mail/MIME/连接池）、`internal/auth`（会话/CSRF/限流）、`internal/srp`、`internal/logging`。
- 前端 `web/src/`：`pages/` 页面、`components/` 组件、`api/client.ts` 唯一 fetch 入口、`test/` MSW+Vitest 设施。
- 改代码前先读 `ARCHITECTURE.md`：含「需求 → 首选改动位置 → 需同步检查」映射表与路由/DTO 清单，是权威改动指南。`API.md` 部分落后于实现，以 `internal/server/server.go` 路由注册为准。

## 硬性约定

- handler 只依赖 `server.Backend` 接口（生产 `managerBackend`、测试 fake），不直接调用 iCloud/IMAP 客户端。
- HTTP 响应统一 `{success,data}` / `{success,code,message}`，复用 `ok`/`failCode`/`backendFail`，不另建结构；稳定错误码集合见 `API.md` 顶部。
- 账号响应只允许 `account.Summary`（脱敏 DTO）；Cookie、App Password、代理原文不得出现在日志/API 响应/前端状态。
- iCloud Cookie 刷新后必须经 `Manager.SaveCookies` 持久化。
- 任务状态与日志写入用 `writeJSONAtomic`（0600 权限，防中断产生半个 JSON）。
- 前端请求一律走 `api/client.ts` 的 `request()`（自动带 CSRF 头、统一错误处理），不要绕过直接 `fetch()`。
- 提交信息：`type: 中文描述`（feat/fix/perf/docs/chore/ci/refactor…）；近期提交用 `[verified]` 前缀标记已通过完整验证的改动。

## 测试

- Go：表驱动测试 + 中文 case 名与断言消息；handler 测试经 `newWithBackend(fake, cfg)` 注入内存 fake（见 `internal/server/*_test.go`）。
- 前端：Vitest + Testing Library + MSW；`test/setup.ts` 配了 `onUnhandledRequest: 'error'`——新增接口必须先在 `test/handlers.ts` 加 mock，否则相关测试直接失败。
- `npm run lint` 有 2 个已知 `react-refresh/only-export-components` 警告（AuthProvider/ToastProvider），无错误，属预期，不要当新问题处理。

## 陷阱

- `internal/webui/dist/` 是 Vite 构建产物（gitignore，仅 `placeholder.txt` 入库）：改前端后要验证最终二进制，必须先 `npm --prefix web run build` 再 `go build`；未构建时 Go 仍可编译，但界面返回 503「资源未构建」。
- 管理员会话仅存内存：重启即需重新登录；所有写接口需 `X-CSRF-Token`（前端由 `client.ts` 自动携带）。
- 创建限流是全局红线：同一账号两次创建尝试至少间隔 20 分钟、每天最多 50 个（人工+自动共享）。改创建路径时勿绕过预留/冷却逻辑（`auto_task.go` 的 `reserve*Creation`）。
- 排查问题：`ICLOUD_HME_LOG_LEVEL=debug`；日志滚动写到 `<data>/logs/app.log`（10MB×5）。
- `data/` 含真实凭据（Cookie/App Password/代理），永不提交。

## 资料地图

- `README.md`（功能/部署/认证方式）、`ARCHITECTURE.md`（结构+改动指南）、`API.md`（HTTP 契约，部分滞后）、`accounts.json.template`（数据文件格式）、`web/README.md`（前端）。
- `reference/`：两个同域上游项目的只读快照（Go / Python，各有独立 `.git` 与构建），仅供对照借鉴，不属于主仓库构建；说明见 `reference/README.md`，改进候选清单见 `reference/IMPROVEMENTS.md`。
- `IDEA.md`：一句话产品定位。`reference/` 与 `IDEA.md` 目前未纳入 git（untracked）。
