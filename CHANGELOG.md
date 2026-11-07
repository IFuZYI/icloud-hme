# 更新日志

本项目遵循语义化版本。破坏性变更会在条目标题标注 **BREAKING**。

## 未发布

账号状态新增 `expired`：HME 业务接口判定会话失效时自动回写，前端徽章显示「Cookie 失效」。

### 后端

- 新增 `Account.Status = "expired"` 与 `Manager.MarkInvalid`：`/v2/hme/list`、创建/停用/删除别名返回 401/421 时把状态落库为 `expired` 并记录 `last_error`，同时清空该账号的别名/收件箱缓存。此前业务 401 只回给当次请求，账号会长期显示「正常」。
- `CheckAccount`（`POST /api/accounts/:id/check`）在 `/validate` 之后追加一次 HME 业务探活（`ListAliases`）：会话级失败（401/421）判 `expired`，超时/5xx 等上游故障判 `error`；并用同一次响应刷新别名计数，去掉二次回源。
- `Summary` 新增 `status_detail`（仅 `expired` 填充）：脱敏后的失效原因，URL 内嵌凭据替换为 `***@`，截断 200 字符；`last_error` 原文仍不出网。

### 前端

- 账号状态徽章新增「Cookie 失效」（`expired`），悬停显示 `status_detail`。

### 邮件正文

- 修复 HTML-only 邮件丢链接：纯文本化时把 `<a href="X">Y</a>` 展开为 `Y <X>`（仅保留 http/https，`mailto:`/`javascript:`/`data:` 丢弃），验证/重置密码等「按钮即链接」的邮件可直接看到并复制地址。
- 收件箱正文把 `锚文本 <URL>` 折叠成「锚文本」超链接（URL 只进 `href`/`title`，正文不再出现长链接），裸 URL 显示为 `域名/路径` 短标签；新增 `web/src/utils/linkify.ts` 与 16 个单测；`.mail-body a` 增加链接样式与长 URL 折行。

### 文档

- API.md 补充 `status` 四态与 `status_detail` 字段说明。

## v0.2.2

移动端可用性、代理配置体验与依赖安全修复。无 API 破坏性变更。

### 前端

- 账号页改为响应式卡片布局：≤760px 隐藏表格与低频列，状态、代理标识、限额在卡片内以紧凑行展示；主要操作保留在卡片上，编辑/删除收进「更多」菜单。
- 移动端导航与操作触控目标统一到 44px（顶栏、菜单项、卡片按钮、分页器）；输入框在窄屏使用 16px 字号，避免 iOS 聚焦时自动缩放。
- 修复窄屏横向溢出：秘钥、日志、别名页的长文本与操作行可换行，表格容器不再撑破视口。
- 修复 ≤520px 导航被后置规则覆盖的问题：320–360px 下 6 个菜单项曾溢出 28–68px，现按 ≤520/≤348 的收紧规则完整显示。
- 代理弹窗：修正复选框被全局 `input` 规则撑大的问题；弹窗背景改为不透明表面；标签区分「账号代理地址」「IMAP 代理地址」并显示「已配置」状态；清除已配置代理必须显式勾选，勾选优先于输入值；未改动时保存按钮禁用。
- 账号卡片明确区分 `代理`、`IMAP代理`、`IMAP直连` 三种状态，避免把「未配置」误读为「已直连」。
- 账号「更多」弹层增加 `max-height` 与内部滚动，短屏（横屏手机/小窗）不再裁掉底部菜单项；761–1023px 平板的菜单项同样为 44px。
- 秘钥卡片操作行恢复右对齐；别名复制按钮在无 hover 的触屏上常显，不再留下不可见的 44px 热区。

### 依赖与构建

- 升级 `github.com/andybalholm/brotli` v1.2.2 → v1.2.7、`github.com/klauspost/compress` v1.19.0 → v1.20.2、`golang.org/x/net` v0.57.0 → v0.62.0、`golang.org/x/crypto` v0.55.0 → v0.58.0、`golang.org/x/sys` v0.47.0 → v0.49.0、`golang.org/x/text` v0.41.0 → v0.43.0，修复 `govulncheck -mode=binary` 报告的运行时代码可达漏洞；扫描仍列 1 条仅模块级、不可达的 `GO-2026-5932`（x/crypto，待正式补丁版）。
- CI、Release 二进制与 Docker 镜像构建统一固定 Go `1.26.10`（`Dockerfile` builder 由浮动的 `golang:1.27-alpine` 改为 `golang:1.26.10-alpine`），并用 `toolchain go1.26.10` 约束源码构建；`govulncheck -mode=binary` 对 1.26.10 构建报告 0 个可达漏洞。
- 前端生产依赖 `npm audit --omit=dev` 为 0 漏洞；开发依赖（vite/vitest/eslint 工具链）仍有公告，仅影响本地开发服务器与测试，不进入发布产物。

### 文档

- 新增本更新日志；README 的固定版本示例更新为 `0.2.2`。

## v0.2.1

- 修复代理清除语义：`PUT /api/accounts/:id/proxies` 的 `proxy`/`imap_proxy` 支持 `nil`（不改动）与 `""`（清除）三态；只提交用户编辑过的字段，清除勾选优先于输入值。
- 仅修改 IMAP 代理时不再误清账号代理的时区缓存；代理实际变化时才重建连接池。
- 补充代理持久化往返、落盘失败回滚、POST 转发与响应禁止字段测试。

## v0.2.0

- 新增 IMAP 专用代理 `imap_proxy`：与账号 `proxy` 相互独立，留空表示 IMAP 直连（不回退账号代理）；需要单一出口时两项填同一地址。
- 新增 `ICLOUD_HME_IMAP_TIMEOUT`：IMAP 总预算，默认 `30s`，范围 `5s–10m`，非法或越界时启动失败；信封下限与正文预算随配置放大。
- IMAP 坏连接（含 `connection closed`）丢弃后在新连接上重试一次，避免死连接反复失败导致回退 Web API 并返回 502。
- Compose 默认使用 `ghcr.io/ifuzyi/icloud-hme:latest`，并保留本地 `build` 段。
