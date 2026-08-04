# 计划自审结果

## 结论

计划可以交给 DeepSeek 按任务顺序执行。范围被拆成 10 个可独立测试和提交的任务，业务代码尚未修改。

## Spec coverage

- 管理员登录、会话恢复与退出：Task 2、3、6。
- 账号、Cookie、iCloud 登录、OTP、App Password 与代理：Task 1、3、7。
- 别名创建/列表/停用/激活/删除：Task 3、8。
- 收件箱筛选与邮件摘要：Task 3、9。
- 单二进制内嵌、SPA fallback：Task 4、5、10。
- 安全认证、CSRF、限流、脱敏、安全头、无 HTML 注入：Task 1–3、6–9。
- Go/前端测试、竞态检测、CI、Docker、Release 与文档：Task 1–10。
- 明确排除多用户、数据库、邮件正文和 iCloud 协议重写：计划第 1.2 节与 DeepSeek 执行约束。

未发现需求缺口。

## Placeholder scan

- 未发现 `TBD`、`TODO`、`implement later`、`fill in details`、“类似 Task N”或“适当处理”等占位表达。
- 出现的 `./...` 均为 Go 包通配命令，不是内容占位。
- Docker 密码示例使用明确的不可照抄示例值，并要求文档标注更换。

## Type and contract consistency

- `account.Summary` 是唯一 HTTP 账号 DTO；`managerBackend` 把 Manager 方法适配为 handler 使用的 `Backend`。
- `AddAccountWithInput` → `Backend.AddAccount`、`UpdateMetadata` → `Backend.UpdateAccount` 的适配关系已明确。
- alias 保留 `anonymousId/createdAt`，create/inbox/account 保留现有 snake_case；前端类型按真实字段建模。
- CSRF Header、Cookie 名称、会话响应、错误码和环境变量在后端、前端、测试、文档任务中一致。
- Vite public 占位文件会在清空 dist 后复制回去，不会导致 tracked `placeholder.txt` 被删除。

## 风险审查

- Critical：当前匿名 API、登录响应 Cookie 泄露、ListAccounts 的 AppPassword/Proxy 泄露均被列为上线前阻断项。
- Warning：Manager 解锁后继续访问共享指针/map；Task 1 要求深拷贝并用 race detector 验证。
- Warning：当前无测试缝；Task 3 定义高层 Backend，所有 handler 测试离线。
- Info：引入 Node 构建会增加源码构建步骤；发布二进制运行时仍不需要 Node。

## 审查限制

按仓库规范尝试并行调用 Gemini 与 Claude，但外部模型读取仓库的授权被安全策略拒绝，未绕过。自审基于本地源码、官方 React/Vite/Vitest 文档以及计划内一致性检查完成。
