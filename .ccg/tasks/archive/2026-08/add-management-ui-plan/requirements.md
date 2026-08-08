# 管理界面增强需求

## 用户目标

为现有 `icloud-hme` Go/Gin 服务制定一份可直接交给 DeepSeek 执行的管理界面实施计划。本次任务只产出计划，不修改业务代码。

## 对“界面管理”的落地解释

- 在服务根路径 `/` 提供中文单管理员 Web 控制台。
- 覆盖现有账号、Cookie、iCloud 登录、App Password、别名和收件箱能力。
- 保持发布物为一个自包含 Go 二进制；前端产物在构建时通过 `go:embed` 打入二进制。
- 前端与 API 同源部署，不增加 CORS，不依赖 CDN、远程字体或外部分析脚本。
- 桌面浏览器优先，同时保证窄屏下能够完成全部操作。

## 安全基线

- 所有业务 API 必须先通过管理员会话认证；管理员密码只从 `ICLOUD_HME_ADMIN_PASSWORD` 环境变量读取。
- 浏览器会话使用随机、HttpOnly、SameSite=Strict Cookie；所有状态变更请求验证 CSRF Token。
- 登录失败需要限流；API 和静态页面需要合理的安全响应头。
- API 响应不得返回 Cookie、App Password、iCloud 登录密码或含凭据的代理 URL。
- 前端不得把上述秘密写入 localStorage、sessionStorage、URL、日志或错误消息，提交后立即清空输入状态。
- 邮件摘要仅作为文本渲染，禁止使用 `dangerouslySetInnerHTML`。

## 功能范围

1. 管理员登录、会话恢复、退出。
2. 账号列表、添加、编辑基本信息、删除。
3. 更新 Cookie、设置/替换 App Password、使用 iCloud 密码和可选 OTP 登录。
4. 按账号列出别名、创建别名、停用、重新激活、删除。
5. 按账号和可选别名查看邮件摘要，控制 `limit` 与 `days`。
6. 对加载、空数据、失败、重试、破坏性确认和操作成功提供明确反馈。
7. 更新本地构建、Docker、GitHub Release、CI 和用户文档。

## 明确不做

- 多管理员、RBAC、OAuth/SSO、找回密码。
- 新数据库、跨实例共享会话、审计日志平台。
- 邮件正文/附件查看、富 HTML 渲染、发信。
- 修改或重新设计 iCloud/SRP/IMAP 协议实现。
- 跨域部署前后端或兼容旧浏览器。

## 已发现的现状风险

- `internal/server/server.go` 当前未保护任何 `/api` 路由。
- `loginAccount` 会把 `client.Cookies` 原样返回。
- `account.Manager.ListAccounts` 只清空 `Cookies`，仍可能序列化 `AppPassword` 和带账号密码的 `Proxy`。
- `AddAccount` 未接收 `icloud_email`，导致无 Cookie 新账号难以直接进入密码登录流程。
- `listInbox` 忽略无效整数输入，未限制 `limit`、`days` 的范围。
- `Manager` 在释放互斥锁后继续读写共享 `Account` 指针和 Cookie map；管理界面增加并发请求后需要通过 `go test -race` 验证。
- 当前没有 Go 测试、前端工程或 CI 检查；Docker/Release 也没有 Node 构建阶段。
- `.gitignore` 忽略所有 `docs/` 与 `*.js`，新增计划和前端配置必须显式处理。

## 假设与兼容性决策

- 接受一次安全性驱动的破坏性变更：升级后所有业务 API 需要登录会话。
- 现有业务路由和字段命名尽量保持不变；不趁机重写为新的 REST 版本。
- 进程重启后会话失效是可接受的；会话只保存在内存。
- `ICLOUD_HME_SECURE_COOKIE=false` 仅用于本机 HTTP；经 TLS 反向代理部署时必须设为 `true`。
- 构建源码需要 Node.js 22.12+；已发布二进制运行时不需要 Node.js。

## 计划质量限制

- 2026-08-04 尝试按仓库规范并行调用 Gemini 与 Claude 做分析，但外部模型读取仓库的权限被安全策略拒绝；未绕过该限制。
- 最终计划由本地源码审查、官方技术文档核验和自审完成。
