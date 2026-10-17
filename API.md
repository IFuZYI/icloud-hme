# iCloud Hide My Email API 文档

## 概述

HTTP JSON API，所有接口返回统一格式：

```json
{
  "success": true,
  "data": {}
}
```

**失败响应：**

```json
{
  "success": false,
  "code": "VALIDATION_ERROR",
  "message": "参数错误"
}
```

**稳定错误码：** `AUTH_REQUIRED`、`INVALID_CREDENTIALS`、`RATE_LIMITED`、`CREATION_COOLDOWN`、`CREATION_LIMIT_REACHED`、`CSRF_INVALID`、`INVALID_API_KEY`、`PERMISSION_DENIED`、`ACCOUNT_NOT_ALLOWED`、`PURGE_IN_PROGRESS`、`PURGE_NOT_FOUND`、`KEY_EXISTS`、`KEY_LIMIT_REACHED`、`KEY_NOT_FOUND`、`VALIDATION_ERROR`、`ACCOUNT_NOT_FOUND`、`OTP_REQUIRED`、`OTP_INVALID`、`UPSTREAM_UNAUTHORIZED`、`UPSTREAM_FAILURE`、`INTERNAL_ERROR`

**安全约定：**

- 除 `POST /api/auth/login` 与 `GET /api/auth/session` 外，所有 `/api` 接口都需要管理员会话 Cookie（`hme_session`）**或** API 秘钥（`X-API-Key` / `Authorization: Bearer`）
- 会话请求：非 GET/HEAD/OPTIONS 必须携带 `X-CSRF-Token` 请求头；秘钥请求豁免 CSRF
- 秘钥按接口粒度独立授权；秘钥管理接口（`/api/keys`、`/api/permissions`）仅限管理员会话
- 会话 Cookie：`hme_session`，`Path=/`、`HttpOnly`、`SameSite=Strict`；TLS 部署时设置 `ICLOUD_HME_SECURE_COOKIE=true` 启用 `Secure`
- 任何账号响应**绝不包含** `cookies`、`app_password`、`proxy` 字段（代理只暴露 `has_proxy` 布尔值）
- 用户可见错误消息不拼接上游响应体或秘密

---

## 认证端点

### 1. 登录

```http
POST /api/auth/login
Content-Type: application/json

{"password": "管理员密码"}
```

**成功响应：** 设置 `hme_session` Cookie

```json
{
  "success": true,
  "data": {
    "csrf_token": "随机值",
    "expires_at": "2026-08-05T22:00:00+08:00"
  }
}
```

**错误：**

- `401 INVALID_CREDENTIALS` — 密码错误（不设置 Cookie）
- `429 RATE_LIMITED` — 同一 IP 15 分钟内失败超过 5 次，响应带 `Retry-After` 头

### 2. 查询会话

```http
GET /api/auth/session
Cookie: hme_session=...
```

**成功响应：** 同上（csrf_token / expires_at）。会话无效返回 `401 AUTH_REQUIRED`。

### 3. 退出

```http
POST /api/auth/logout
Cookie: hme_session=...
X-CSRF-Token: <token>

{"success": true, "data": {"logged_out": true}}
```

---

## 账号端点

### 4. 列出账号

```http
GET /api/accounts
```

**响应：** `Summary[]`，排序为 active → pending → error，同状态按 name、id。

```json
{
  "success": true,
  "data": [
    {
      "id": "acc_12345678",
      "name": "主号",
      "real_email": "owner@example.com",
      "icloud_email": "owner@icloud.com",
      "host": "icloud.com",
      "status": "active",
      "alias_total": 15,
      "alias_active": 12,
      "has_cookies": true,
      "has_app_password": true,
      "has_proxy": false,
      "last_validated": "2026-08-04T09:00:00+08:00",
      "status_message": "",
      "created_at": "2026-08-01T09:00:00+08:00"
    }
  ]
}
```

**禁止出现的字段：** `cookies`、`app_password`、`proxy`。`status_message` 只映射固定文案（pending → "等待配置或验证凭据"，error → "凭据验证失败"），不返回内部错误原文。

### 5. 添加账号

```http
POST /api/accounts
X-CSRF-Token: <token>

{
  "name": "新账号",
  "icloud_email": "owner@icloud.com",
  "host": "icloud.com",
  "proxy": "http://user:pass@host:port",
  "cookies": "X-APPLE-WEBAUTH-TOKEN=abc; X-APPLE-WEBAUTH-USER=def"
}
```

- `name` 必填，去空白后 1–64 字符
- `icloud_email` 必填；服务端做规范化后校验：全角 `＠` 与首尾空白自动清理，显示名格式（`"名称" <a@b.com>`）提取出裸地址，多地址（逗号分隔）拒绝。存储的是规范化后的值
- `host` 只能是 `icloud.com` 或 `icloud.com.cn`（默认 `icloud.com`）
- `proxy` 可选，必须是 `http`/`https`/`socks5` URL
- `cookies` 可选，支持 Cookie Header 字符串或 JSON 文本
- 无 Cookie 时状态为 `pending`，不访问网络

**成功响应：** `201`，返回 `Summary`。

### 6. 编辑账号基本信息

```http
PATCH /api/accounts/:id
X-CSRF-Token: <token>

{"name": "新名称", "host": "icloud.com.cn"}
```

只接受可选的 `name`、`icloud_email`、`host`，至少一个字段存在。响应返回更新后的 `Summary`。账号不存在返回 `404 ACCOUNT_NOT_FOUND`。

### 7. 更新代理

```http
PUT /api/accounts/:id/proxy
X-CSRF-Token: <token>

{"proxy": "http://user:pass@host:port"}
```

空字符串表示清除代理。响应只返回更新后的 `Summary`（代理值从不回显）。

### 8. 更新 Cookie

```http
PUT /api/accounts/:id/cookies
X-CSRF-Token: <token>

{"cookies": "{\"X-APPLE-WEBAUTH-TOKEN\":\"v=1:t=AQAAAAB...\",\"X-APPLE-WEBAUTH-USER\":\"d=...:s=...\",\"X_APPLE_WEB_KB\":\"...\"}"}
```

`cookies` 同时兼容字符串与对象：

```json
{"cookies": {"X-APPLE-WEBAUTH-TOKEN": "v=1:t=AQAAAAB...", "X-APPLE-WEBAUTH-USER": "d=...:s=...", "X_APPLE_WEB_KB": "..."}}
```

两种输入最终都交给 `account.ParseCookieInput`。响应只返回更新后的 `Summary`。

### 9. 设置 App 专用密码

```http
POST /api/accounts/:id/password
X-CSRF-Token: <token>

{"icloud_email": "your_email@icloud.com", "app_password": "xxxx-xxxx-xxxx-xxxx"}
```

服务端会用 IMAP 连接验证凭据。成功返回 `Summary`；IMAP 验证失败返回 `502 UPSTREAM_FAILURE`。

### 9b. 设置外部 IMAP 收件邮箱

```http
PUT /api/accounts/:id/mailbox
X-CSRF-Token: <token>

{
  "provider": "qq",
  "email": "you@example.com",
  "imap_host": "imap.example.com",
  "imap_port": 993,
  "authorization_code": "授权码"
}
```

把一个外部 IMAP 邮箱绑定到该 iCloud 账号：读取邮件时优先走该邮箱（IMAP），未配置时回退 iCloud 自身通道。四个字段（`email`、`imap_host`、`imap_port`、`authorization_code`）必填，`provider` 仅作展示/兼容用途。服务端会立即用 IMAP 连接验证；成功返回脱敏后的 `Summary`（不含授权码），验证失败返回 `502 UPSTREAM_FAILURE`；账号不存在返回 `404 ACCOUNT_NOT_FOUND`；该邮箱已被其他账号绑定时返回 `409 MAILBOX_IN_USE`（一个外部邮箱只能绑定一个账号）。

**权限：** `accounts.write`；**账号范围：** 路径 `:id` 必须在该秘钥的白名单内（否则 `403 ACCOUNT_NOT_ALLOWED`）。

### 10. iCloud 密码登录（获取 Cookie）

```http
POST /api/accounts/:id/login
X-CSRF-Token: <token>

{"password": "用户的常规iCloud密码", "otp_code": "123456"}
```

- `otp_code` 可选，启用 2FA 时使用
- 需要 OTP：`409 OTP_REQUIRED`
- 验证码错误：`401 OTP_INVALID`
- 成功：**只返回 `Summary`，绝不返回 Cookies**（Cookie 自动持久化到账号配置）

> 推荐改用两段式登录（避免在单个请求内阻塞等待验证码）：
>
> ```http
> POST /api/accounts/:id/login/begin     {"password":"..."}
>   → {"status":"done","account":{...}}                   无需 2FA
>   → {"status":"otp_required","session_id":"...","push_sent":true,"delivery":"trusted_devices"}   需要 2FA
> POST /api/accounts/:id/login/otp       {"session_id":"...","code":"123456"}
>   → {"status":"done","account":{...}}                   验证通过
> POST /api/accounts/:id/login/sms       {"session_id":"...","phone_id":2}
> GET  /api/accounts/:id/login/phones?session_id=...
> POST /api/accounts/:id/login/resend    {"session_id":"..."}
> ```
>
> `delivery` 表示服务端实际选择的验证码通道（参考 any-auto-register 的
> prepare_verification，409 之后按账号条件自动决定）：
>
> | delivery | 含义 | 前端提示 |
> |---|---|---|
> | `trusted_devices` | 已推送到受信任设备 | 验证码已推送到受信任设备 |
> | `sms` | 无受信任设备、单手机号 → 已自动发短信 | 验证码已通过短信发送 |
> | `sms_selection_required` | 无受信任设备、多手机号 → 需选择号码 | 请选择手机号接收短信验证码 |
>
> `push_sent` 等价于 `delivery == "trusted_devices"`（保留兼容）。
> 无受信任设备且无手机号时投递失败并返回错误（不会静默等一个收不到的验证码）。
>
> 投递限流：同一会话最多 5 次、每次间隔 ≥30 秒；超限返回 `429 OTP_DELIVERY_LIMITED`。
> 会话有效期 5 分钟；验证码错误时会话保留，可直接重试；成功或过期后会话失效。

### 10b. 检测账号登录态

```http
POST /api/accounts/:id/check
X-CSRF-Token: <token>
```

对账号现有 Cookie 做一次低风险 validate 探活（不创建/修改任何远端资源，不触发创建冷却），并把结果落库后返回脱敏 `Summary`。

- 有效：`200`，`status=active`、`last_validated` 刷新、`last_error` 清空；顺带刷新别名计数
- 失效：`401 UPSTREAM_UNAUTHORIZED`（会话已失效）或 `502 UPSTREAM_FAILURE`（其他上游故障）；`status=error`、`last_error` 记录原因（脱敏，不含 Cookie 值）
- 无 Cookie：`400 VALIDATION_ERROR`（账号未配置 Cookie，无法检测）
- 账号不存在：`404 ACCOUNT_NOT_FOUND`

### 11. 删除账号

```http
DELETE /api/accounts/:id
X-CSRF-Token: <token>
```

**响应：** `{"id": "acc_3"}`。不存在返回 `404 ACCOUNT_NOT_FOUND`。

---

## 业务端点

### 12. 创建 HME 别名

```http
POST /api/create
X-CSRF-Token: <token>

{"account_id": "acc_1", "label": "GitHub"}
```

- `account_id` 必填
- `label` 必填，长度 ≤200 字符；可自由输入（不再限定名称库）
- 同一账号任意连续 60 分钟内创建尝试（成功或失败、人工或自动）最多 5 次；超限返回 `429 CREATION_HOURLY_LIMIT`
- 同一账号两次「人工」创建尝试至少相隔 20 分钟；过早请求返回 `429 CREATION_COOLDOWN`
- 单个账号当天最多创建 50 个（人工创建与自动任务合并统计）；超过上限返回 `429 CREATION_LIMIT_REACHED`
- 若上游创建失败，已预留的人工创建额度会归还

**响应：**

```json
{
  "success": true,
  "data": {
    "email": "xyz123@icloud.com",
    "label": "GitHub",
    "created_at": "2026-01-15T10:30:00+08:00",
    "account_id": "acc_1"
  }
}
```

### 13. 获取别名标签名称库

```http
GET /api/alias-labels
```

返回人工创建和自动任务共用的常用服务名称数组，例如：

```json
{"success": true, "data": ["GitHub", "GitLab", "Google Workspace", "Notion", "Slack"]}
```

### 14. 读取邮件

```http
GET /api/inbox?account_id=acc_1&alias=xyz123@icloud.com&limit=20&start=2026-08-01&end=2026-08-10
```

- `account_id` 必填
- `alias` 可选，只返回发给该别名的邮件
- `limit` 1–100（默认 20）
- `start`/`end` 可选，时间区间；支持 `YYYY-MM-DD`（整天，`end` 含当天到 23:59:59）、`YYYY-MM-DDTHH:mm[:ss]`（精确到分/秒，前端日期时间选择器格式，本地时区）或 RFC3339（带时区）；同时给出时 `end` 不得早于 `start`
- `days` 0–3650（默认 7）仅在未提供 `start`/`end` 时生效；非法值直接 `400 VALIDATION_ERROR`
- `refresh` 可选，`1` 表示**受限刷新**（前端「查询」按钮与「加载更多」携带）：绕过服务端 TTL 缓存回源，但受刷新节流地板约束（默认 5s）——地板内的重复查询复用最近一次回源结果，防连点打爆上游
- `force` 可选，`1` 表示**强制刷新**（前端「刷新」按钮携带）：每次都无条件真实回源——绕过服务端 TTL 缓存**并作废 IMAP 层 30s 列表快照**，不受任何节流约束
- 两者都不携带（首屏/自动加载/翻页）时可命中缓存。防重复访问由「非刷新读取的短 TTL 缓存（收件箱信封 30s、邮件摘要 5m、别名列表 30s）+ 同键并发 singleflight 合并 + 连接复用/保活」承担；另有一道竞态防护：非刷新读取若在显式刷新完成后才返回旧数据，其写入被跳过（刷新结果优先）。删除邮件/别名增删改等写操作会即时失效对应缓存，检测到邮箱 UIDVALIDITY 变化（服务端重建）时自动失效该账号读缓存

**响应（IMAP 优先，Web API 回退）：**

```json
{
  "success": true,
  "data": {
    "account_id": "acc_1",
    "alias": "xyz123@icloud.com",
    "count": 2,
    "method": "imap",
    "messages": [
      {
        "id": "1042",
        "from": "GitHub <noreply@github.com>",
        "to": "xyz123@icloud.com",
        "subject": "[GitHub] Please verify your email address",
        "date": "2026-07-09T14:32:10+08:00",
        "preview": "Almost done! To finish setting up your account...",
        "folder": "INBOX"
      },
      {
        "id": "Junk:88",
        "from": "noreply@example.com",
        "to": "xyz123@icloud.com",
        "subject": "验证码",
        "date": "2026-07-09T15:01:00+08:00",
        "preview": "您的验证码是 123456",
        "folder": "Junk"
      }
    ]
  }
}
```

`method` 为 `imap` 或 `web_api`。IMAP 路径支持服务端按收件人搜索；Web API 路径拉取后本地过滤。

**时间范围参数**：`start`/`end` 由前端两个日期时间选择器（开始时间/结束时间，Ant Design `DatePicker showTime`）产生，格式 `YYYY-MM-DDTHH:mm:ss`，后端原样保留到分钟/秒精度，不做整天对齐。

**文件夹与消息 ID**：IMAP 读取同时扫描收件箱与垃圾邮件箱（iCloud 常把转发邮件判为垃圾；网易 163/126 的垃圾邮件夹名为「垃圾邮件」而非 Junk，一并扫描），`folder` 字段标出来源。IMAP 的 UID 按文件夹生效，因此非 INBOX 邮件的 `id` 带文件夹前缀（`Junk:88`），INBOX 保持纯数字（`1042`，兼容旧格式）。`GET /api/inbox/:message_id`、`DELETE /api/inbox/:message_id` 与 `POST /api/inbox/previews` 的 `ids` 均接受这两种格式，原样回传列表里的 `id` 即可。

IMAP 不可用而回退到 Web API 时，响应会额外带 `warning` 字段说明降级原因（缺省表示走 IMAP 成功）：

```json
{
  "success": true,
  "data": {
    "account_id": "acc_1",
    "count": 1,
    "method": "web_api",
    "warning": "IMAP 不可用，已回退 Web API：IMAP 登录失败 — 请检查邮箱账号、授权码和服务器地址",
    "messages": []
  }
}
```

`preview` 由服务端解析 MIME 正文得到：支持 `multipart/*` 递归、`message/rfc822` 转发邮件、base64 / quoted-printable 解码与非 UTF-8 字符集转换，优先 `text/plain`，仅有 HTML 时转为纯文本。摘要最多保留 2000 个字符（按字符计），完整正文通过 `GET /api/inbox/:message_id` 获取，不受此限制。

附件判定：`Content-Disposition: attachment` 的部分一律跳过（含 filename 未加引号导致解析失败的情况）；多部分邮件中只带 `name=` 参数而无 `Content-Disposition` 的部分同样按附件处理（单部分邮件的 `name=` 仍视为正文）。另：为省去对超大 HTML 部分的解析开销，有 `text/plain` 部分时不再解析 HTML。

`date` 取邮件 `Date` 头（缺失时回退 IMAP `INTERNALDATE`）；`days` 过滤与排序改用服务端收件时间 `INTERNALDATE`，因为 `Date` 由发件人填写，不可靠。

### 14.1 批量清理邮件（按时间范围）

**权限：** `inbox.purge`（与单封删除 `inbox.delete` 分离——批量清空风险量级不同，独立授权）。

**时间粒度：** IMAP SEARCH 按「日」比较（RFC 3501），清理范围的自然粒度是**天**。

**覆盖文件夹：** `INBOX`、`Junk`、`垃圾邮件`（与读信口径一致）。

**风控设计（分块节流）：**
- 每块 = 1×`UID STORE +FLAGS \Deleted`（整块 seqset，连续 UID 自动压缩为区间）+ 1×`EXPUNGE`，而非逐封删除——1 万封从 ~2 万次往返压到 ~40 次；
- 块大小 500 封；块间间隔 3s + 0~2s 随机抖动；
- 滚动 1 小时最多 20 块（1 万封），超出转 `throttled` 状态等待窗口滚动；
- 失败单块重试 1 次，仍失败 → `paused`（记录错误，可从断点继续）；
- 按账号并发：同账号同时最多 1 个任务，不同账号可并行。

**预览（仅统计，不删除）：**

```http
POST /api/inbox/purge/preview
X-CSRF-Token: <token>

{"account_id":"acc_1","range_start":"2026-01-01T00:00:00Z","range_end":"2026-10-16T00:00:00Z"}
```

响应：

```json
{
  "success": true,
  "data": {
    "total": 1234,
    "folders": [{"folder":"INBOX","count":1200},{"folder":"Junk","count":34}],
    "estimated_seconds": 8,
    "range_start": "2026-01-01T00:00:00Z",
    "range_end": "2026-10-16T00:00:00Z"
  }
}
```

**创建清理任务（异步执行）：**

```http
POST /api/inbox/purge
X-CSRF-Token: <token>

{"account_id":"acc_1","range_start":"...","range_end":"..."}
```

同账号已有活跃任务时返回 `409 PURGE_IN_PROGRESS`。

**任务列表（秘钥按账号范围过滤）：**

```http
GET /api/purge-tasks
```

```json
{
  "success": true,
  "data": [
    {
      "id": "purge_a1b2c3d4e5f60718",
      "account_id": "acc_1",
      "account_name": "主账号",
      "range_start": "2026-01-01T00:00:00Z",
      "range_end": "2026-10-16T00:00:00Z",
      "status": "running",
      "total": 1234, "processed": 500, "deleted": 500, "failed": 0,
      "current_message": "INBOX: 删除 500 封",
      "started_at": "2026-10-16T09:00:00Z"
    }
  ]
}
```

`status` 取值：`running`｜`throttled`（风控节流中）｜`paused`（失败/手动暂停）｜`done`｜`failed`｜`cancelled`｜`interrupted`（服务重启中断，可 resume）。

**任务控制：**

```http
POST /api/purge-tasks/:id/pause
POST /api/purge-tasks/:id/resume
POST /api/purge-tasks/:id/cancel
```

任务不存在或不在秘钥账号范围内 → `404 PURGE_NOT_FOUND`（防存在性探测）。

**持久化：** 任务存 `<数据目录>/purge_tasks.json`，保留最近 50 条；服务重启后运行中的任务标记 `interrupted`，可 resume 从断点继续。

**注意：** `EXPUNGE` 是文件夹级操作（go-imap v1 无 UID EXPUNGE/UIDPLUS）——若其它客户端有「延迟 expunge」的待删邮件，会被一并清掉。这是标准 IMAP 行为，与单封删除路径一致。

### 15. 列出别名

```http
GET /api/aliases?account_id=acc_1
```

**响应：** alias 对象字段风格为 camelCase（兼容 iCloud 原始格式）：

```json
{
  "success": true,
  "data": {
    "account_id": "acc_1",
    "count": 15,
    "aliases": [
      {
        "email": "xyz123@icloud.com",
        "anonymousId": "abc123",
        "label": "注册某网站",
        "active": true,
        "createdAt": "2026-01-15T10:30:00Z"
      }
    ]
  }
}
```

### 16. 停用/激活/删除别名

```http
POST /api/aliases/:id/deactivate
POST /api/aliases/:id/reactivate
DELETE /api/aliases/:id
X-CSRF-Token: <token>

{"account_id": "acc_1"}
```

- `:id` 为别名的 `anonymousId`，非空且 URL 解码后不超过 256 字符
- `account_id` 必填
- 删除不可恢复；直接删除失败时会先停用再删

### 17. 批量停用/激活/删除别名

```http
POST /api/aliases/batch
X-CSRF-Token: <token>

{
  "account_id": "acc_1",
  "action": "delete",
  "aliases": [
    {"anonymous_id": "abc123", "email": "alias1@icloud.com"},
    {"anonymous_id": "def456", "email": "alias2@icloud.com"}
  ]
}
```

- `action` 只能是 `deactivate`、`reactivate` 或 `delete`
- `aliases` 必须包含 1–200 项；每项都必须提供 `anonymous_id` 和 `email`
- 删除操作从第二项开始每项间隔 3 秒
- 每项操作结果与失败原因写入任务日志；初始日志无法持久化时请求会在执行任何别名操作前返回 `500 INTERNAL_ERROR`

```json
{
  "success": true,
  "data": {
    "action": "delete",
    "succeeded": 1,
    "failed": 1,
    "results": [
      {"anonymous_id": "abc123", "email": "alias1@icloud.com", "success": true},
      {"anonymous_id": "def456", "email": "alias2@icloud.com", "success": false, "error": "删除失败"}
    ],
    "logging_error": "可选；副作用开始后的日志持久化错误"
  }
}
```

### 18. 自动创建任务

自动任务为账号低频创建 HME 别名，达到 `target_count` 后自动停止。分两类：

- **自主任务（`mode: auto`）**：`daily_limit` 设定每天创建数量（1–50 任意整数，缺省 20），系统按当天剩余时间分摊执行时刻，每次 1 个。`interval_minutes` 由系统推算（24×60 ÷ 每日数量，不低于 20），无需填写。创建当天按剩余时间折算今日配额 `today_quota`（如每日 20 个、中午 12 点创建 → 今日 10 个），次日跨天恢复满额。`today_quota=0` 是合法值，表示当天剩余时间不足 1 个（如深夜创建），当日不排、次日恢复；该字段始终随响应下发，前端直接读取即可。
- **定时任务（`mode: scheduled`）**：每隔 `interval_minutes`（20–1440）创建 `batch_count`（1–20）个；`daily_limit` 固定为账号每日安全上限 50。

标签由 `label_mode` 决定：`library`（名称库轮换）/ `sequential`（`label_prefix` + 补零序号）/ `hash`（`label_prefix` + `hash_length` 位随机哈希，4–8）。

```http
GET    /api/alias-tasks              # 列出任务（含进度与 next_run）
POST   /api/alias-tasks              # 新建（默认启用，10 秒后首轮）
PATCH  /api/alias-tasks/:id          # 编辑（保存后自动重新启用）
POST   /api/alias-tasks/:id/toggle   # 启用 / 暂停
DELETE /api/alias-tasks/:id          # 删除
GET    /api/alias-task-logs          # 运行日志（逐邮箱成功/失败）
POST   /api/alias-task-logs/cleanup  # 按时间清理日志（写）
X-CSRF-Token: <token>                # 非 GET 请求需要
```

**清理日志请求体：** `{"older_than_days": 7}` —— 删除严格早于「now - N 天」的条目（N 为 1–3650 的整数；前端提供 1 天前 / 1 周前 / 1 个月前 三档）。恰好等于 cutoff 时刻的条目保留（「1 天前」= 保留最近 24 小时）；时间无法解析的条目保守保留。响应 `{"deleted": N, "remaining": M}`。

**新建请求体（字段校验）：**

| 字段 | 必填 | 说明 |
|---|---|---|
| `account_id` | ✅ | 目标账号 ID |
| `target_count` | ✅ | 累计创建目标（1–999） |
| `mode` | ⭕ | `auto`（默认）/ `scheduled` |
| `daily_limit` | auto 可选 | 每天创建数量，1–50 的整数（缺省 20） |
| `interval_minutes` | scheduled 必填 | 间隔分钟（20–1440） |
| `batch_count` | scheduled 必填 | 每周期数量（1–20） |
| `label_mode` | ⭕ | `library`（默认）/ `sequential` / `hash` |
| `label_prefix` | sequential/hash 必填 | 标签前缀（最长 32 字符） |
| `hash_length` | ⭕ | hash 后缀长度（4–8，默认 4） |

参数不合法返回 `400 VALIDATION_ERROR`，任务不存在返回 `404 TASK_NOT_FOUND`。旧版任务向后兼容：缺 `mode` 视为 `scheduled`，缺 `label_mode` 视为 `library`；缺 `created_at`（本次新增字段）在加载时按文件数组位置补写合成时间戳并惰性落盘。

**上游创建失败容错：** 单次上游创建失败不再立即暂停任务，而是保持启用并按既有间隔自动重试；仅当「连续 2 次失败」或「最近 5 次结果中失败 ≥3 次」时才暂停并清空 `next_run`。失败历史由任务字段 `recent_outcomes` 承载（最近 5 次 `ok`/`fail`，随任务持久化，重启不重置；用户手动启用或编辑保存后清空）。本地状态/日志持久化失败不属于上游误报，仍立即暂停。

**任务展示顺序（禁止闪现换位）：** `GET /api/alias-tasks` 返回的数组按任务 `created_at` 升序（新任务在末尾），同一时刻以 `id` 兜底，**跨请求稳定**。后端 `taskListLocked` 不再直接返回 Go map 迭代顺序（该顺序随机，会让前端每 2 秒轮询看到卡片换位）；`created_at` 为固定 9 位小数秒的 UTC 时间串（`2006-01-02T15:04:05.000000000Z07:00`），保证字符串序 = 时间序。`PATCH` 编辑保留原 `created_at`，任务位置与创建时刻均不变。

### 19. 重新加载配置

```http
POST /api/reload
X-CSRF-Token: <token>
```

重新读取 `accounts.json`。

---

## API 秘钥（脚本调用）

秘钥用于脚本代替会话 Cookie 调用接口，**仅限管理员会话管理**（秘钥不能管理秘钥或给自己提权）。

**认证方式：** 请求头 `X-API-Key: sk-…` 或标准形式 `Authorization: Bearer sk-…`；两者同时出现时以 `X-API-Key` 为准。秘钥请求**豁免 CSRF**（不依赖 Cookie，无浏览器自动携带凭证的 CSRF 面）。

**权限模型：** 每个秘钥独立授权，按接口粒度勾选（权限 ID 见 `GET /api/permissions`）。权限不足返回 `403 PERMISSION_DENIED`；秘钥无效/已停用返回 `401 INVALID_API_KEY`（与未知秘钥同文案，不泄露秘钥存在性）。

**账号使用范围：** 每个秘钥可额外限定可访问的账号列表（`account_ids`）。**留空 = 全部账号**（默认，向后兼容）；**非空 = 白名单**，仅列出的账号可用。请求的目标账号不在范围内时返回 `403 ACCOUNT_NOT_ALLOWED`，且校验早于业务逻辑（不会到达 handler）。目标账号从 query `account_id`、JSON body `account_id`、或账号路由路径 `:id` 中提取；**不依赖 `Content-Type`**（只要请求体能解析出 `account_id` 即校验，防止用 `text/plain` 携带 JSON body 绕过）。

范围限制同时作用于**读**与**写**：
- 列表接口只返回范围内数据：`/api/accounts`、`/api/aliases`、`/api/alias-tasks`、`/api/alias-task-logs`；
- 单项操作按资源**真实归属账号**判定：`/api/alias-tasks/:id`（经任务 ID 解析归属）；
- 日志清理 `/api/alias-task-logs/cleanup` 仅清理范围内账号的日志；
- 为防枚举探测，任务路由上「范围外」与「不存在」统一返回 `404 TASK_NOT_FOUND`。

### 20. 列出秘钥

```http
GET /api/keys
```

响应（含明文秘钥，面板可查看/复制）：

```json
{
  "success": true,
  "data": {
    "count": 1,
    "keys": [
      {
        "id": "key_ba6fcc58b77b586a",
        "name": "签到脚本",
        "key": "sk-eAWxxxxxxxxxxxxxxxxxxxxHBwr",
        "enabled": true,
        "permissions": ["inbox.list"],
        "account_ids": ["acc_ba6fcc58b77b586a"],
        "created_at": "2026-10-15T19:56:29+08:00",
        "last_used_at": "2026-10-15T20:01:00+08:00",
        "last_used_ip": "192.168.1.5"
      }
    ]
  }
}
```

### 21. 创建秘钥

```http
POST /api/keys
X-CSRF-Token: <token>

{"name":"签到脚本","key":"","permissions":["inbox.list","inbox.get"],"account_ids":["acc_ba6fcc58b77b586a"]}
```

- `name` 必填；`key` 留空自动生成 `sk-` + 24 位随机字符（大小写字母+数字），手动指定时要求 8–128 位可打印 ASCII（不含空格）且全局唯一
- `permissions` 为权限 ID 列表（可空 = 无任何权限）；未知权限 ID 直接 `400 VALIDATION_ERROR`
- `account_ids` 为账号使用范围（**空/缺省 = 全部账号**）；引用不存在的账号返回 `400 VALIDATION_ERROR`（不做幽灵绑定）
- 重复秘钥 `409 KEY_EXISTS`；数量达上限（100）`409 KEY_LIMIT_REACHED`

### 22. 更新秘钥

```http
PATCH /api/keys/:id
X-CSRF-Token: <token>

{"name":"新名字","permissions":["inbox.list"],"account_ids":["acc_1","acc_2"],"enabled":false}
```

字段均可选（缺省不变）；**秘钥值不可修改**（需要换值请删除重建）。`account_ids` 传空数组 `[]` 表示清空为「全部账号」；`account_ids` 为 null/缺省则不变。不存在 `404 KEY_NOT_FOUND`。

### 23. 删除秘钥

```http
DELETE /api/keys/:id
X-CSRF-Token: <token>
```

删除后使用该秘钥的脚本立即失效。不存在 `404 KEY_NOT_FOUND`。

### 24. 权限目录

```http
GET /api/permissions
```

```json
{
  "success": true,
  "data": {
    "groups": [{"id":"inbox","label":"收件箱"}],
    "permissions": [{"id":"inbox.list","label":"读取收件箱列表","group":"inbox"}]
  }
}
```

**权限 ID 一览：** `accounts.list`、`accounts.write`、`aliases.list`、`aliases.create`、`aliases.write`、`tasks.list`、`tasks.write`、`inbox.list`、`inbox.get`、`inbox.previews`、`inbox.delete`、`inbox.purge`、`system.reload`。

---

## curl 使用示例（Cookie Jar + CSRF）

```bash
BASE="http://localhost:8081"

# 1. 登录,保存 Cookie 到 jar
curl -c cookies.txt -X POST "$BASE/api/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"password":"你的管理员密码"}'

# 2. 从响应中提取 csrf_token(可用 jq)
CSRF=$(curl -b cookies.txt "$BASE/api/auth/session" | jq -r '.data.csrf_token')

# 3. 读取账号列表(GET 无需 CSRF)
curl -b cookies.txt "$BASE/api/accounts"

# 4. 添加账号(mutation 需要 CSRF 头)
curl -b cookies.txt -X POST "$BASE/api/accounts" \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $CSRF" \
  -d '{"name":"新账号","icloud_email":"owner@icloud.com"}'

# 5. 创建别名
curl -b cookies.txt -X POST "$BASE/api/create" \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $CSRF" \
  -d '{"account_id":"acc_1","label":"GitHub"}'

# 6. 读取邮件
curl -b cookies.txt "$BASE/api/inbox?account_id=acc_1&limit=10"
```

## curl 使用示例（API 秘钥, 推荐给脚本）

```bash
BASE="http://localhost:8081"

# 1. 在管理台「秘钥」页创建, 或经会话 API 创建:
#    curl -b cookies.txt -X POST "$BASE/api/keys" -H "Content-Type: application/json" \
#      -H "X-CSRF-Token: $CSRF" \
#      -d '{"name":"签到脚本","permissions":["inbox.list","inbox.get"]}'
#    从响应 data.key 取秘钥, 保存到脚本环境变量:
KEY="sk-..."

# 2. 直接用秘钥调用(无需 Cookie、无需 CSRF; 只受权限表约束)
curl -H "X-API-Key: $KEY" "$BASE/api/inbox?account_id=acc_1&limit=10"
curl -H "Authorization: Bearer $KEY" "$BASE/api/accounts"

# 3. 权限不足/秘钥无效的区分:
#    403 PERMISSION_DENIED  = 秘钥有效但未勾选该接口
#    403 ACCOUNT_NOT_ALLOWED = 秘钥有效但该账号不在其账号使用范围内
#    401 INVALID_API_KEY    = 秘钥错误或已停用(删除/停用后立即失效)
```

---

## 认证方式（iCloud 账号侧）

### Cookie 认证（功能最完整）

用于创建/停用/激活/删除别名、读取邮件（Web API 回退）。

**获取方式：**
1. 浏览器登录 [icloud.com](https://www.icloud.com) 或 [icloud.com.cn](https://www.icloud.com.cn) (国区)
2. F12 → Application → Cookies
3. 导出 Cookie 为 `{"key":"value"}` JSON，粘贴到管理界面「更新 Cookie」

**关键 Cookie：** `X-APPLE-WEBAUTH-TOKEN`（认证 token）、`X-APPLE-WEBAUTH-USER`（含 dsid）、`X-APPLE-WEBAUTH-HSA-TRUST`（设备信任）、`X-APPLE-DS-WEB-SESSION-TOKEN`（会话）

**有效期：** 约 24 小时

### App Password 认证（IMAP 优先读邮件）

用于 IMAP 读取邮件（优先路径，支持服务端按收件人搜索）。在 [appleid.apple.com](https://appleid.apple.com) → 登录和安全 → App 专用密码 生成。

---

## 技术说明

**Web API 路径** (`internal/mail/web_client.go`)：
1. 调用 `setup.icloud.com.cn/setup/ws/1/validate` 获取 `mccgateway` URL
2. 调用 `mccgateway/mailws2/v1/thread/search` 读取邮件

**⚠️ 已知坑：**
- `validate` 返回的 mccgateway URL 可能带 `:443` 端口，tls-client 的 cookie jar 按不带端口的 host 存储 cookie，带端口请求时 cookie 无法附加导致 403；**解决：** 解析 URL 后剥离端口号

**IMAP 路径** (`internal/mail/client.go`)：标准 IMAP 协议，连接 `imap.mail.me.com:993`，需要 App Password。

**升级差异（相对旧版）：**
- 全部 API 需要管理员登录（`401 AUTH_REQUIRED`）
- 账号响应不再返回 Cookie/密码/代理原文，改用 `has_cookies`/`has_app_password`/`has_proxy`
- `POST /api/accounts/:id/login` 成功响应不再返回 `cookies` 字段
- `accounts.json` 使用 `{"accounts": {id: {...}}}` map wrapper 格式（参考 `accounts.json.template`）

## 限制

- **创建频率**：同账号任意连续 60 分钟内创建尝试（成功或失败、人工或自动）最多 5 次；两次「人工」创建尝试至少相隔 20 分钟。自动任务分自主（每天 1–50 个，按拟人模型分摊：用户画像 + 日/周周期 + 会话自激 + 对数正态间隔，作息时区跟随账号代理出口 IP）与定时（每 20–1440 分钟创建 1–20 个，同样受滚动小时上限约束）两类，并对人工与自动创建合计执行每账号每天 50 个上限；上游失败不自动重试，自动任务会暂停等待人工确认。
- **Cookie 有效期**：约 24 小时，需定期更新
- **邮件读取**：依赖 IMAP 连接，超时默认 30 秒
- **请求体上限**：1 MiB
