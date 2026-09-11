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

**稳定错误码：** `AUTH_REQUIRED`、`INVALID_CREDENTIALS`、`RATE_LIMITED`、`CSRF_INVALID`、`VALIDATION_ERROR`、`ACCOUNT_NOT_FOUND`、`OTP_REQUIRED`、`OTP_INVALID`、`UPSTREAM_UNAUTHORIZED`、`UPSTREAM_FAILURE`、`INTERNAL_ERROR`

**安全约定：**

- 除 `POST /api/auth/login` 与 `GET /api/auth/session` 外，所有 `/api` 接口都需要管理员会话
- 非 GET/HEAD/OPTIONS 请求必须携带 `X-CSRF-Token` 请求头
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
- `icloud_email` 必填，`net/mail` 校验且地址值必须等于输入
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

{"cookies": "a=1; b=2"}
```

`cookies` 同时兼容字符串与对象：

```json
{"cookies": {"a": "1", "b": "2"}}
```

两种输入最终都交给 `account.ParseCookieInput`。响应只返回更新后的 `Summary`。

### 9. 设置 App 专用密码

```http
POST /api/accounts/:id/password
X-CSRF-Token: <token>

{"icloud_email": "your_email@icloud.com", "app_password": "xxxx-xxxx-xxxx-xxxx"}
```

服务端会用 IMAP 连接验证凭据。成功返回 `Summary`；IMAP 验证失败返回 `502 UPSTREAM_FAILURE`。

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

{"account_id": "acc_1", "label": "注册某网站"}
```

- `account_id` 必填
- `label` 可选，最长 200 字符

**响应：**

```json
{
  "success": true,
  "data": {
    "email": "xyz123@icloud.com",
    "label": "注册某网站",
    "created_at": "2026-01-15T10:30:00+08:00",
    "account_id": "acc_1"
  }
}
```

### 13. 读取邮件

```http
GET /api/inbox?account_id=acc_1&alias=xyz123@icloud.com&limit=20&days=7
```

- `account_id` 必填
- `alias` 可选，只返回发给该别名的邮件
- `limit` 1–100（默认 20）
- `days` 1–90（默认 7）；非法整数直接 `400 VALIDATION_ERROR`

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
        "preview": "Almost done! To finish setting up your account..."
      }
    ]
  }
}
```

`method` 为 `imap` 或 `web_api`。IMAP 路径支持服务端按收件人搜索；Web API 路径拉取后本地过滤。

### 14. 列出别名

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

### 15. 停用/激活/删除别名

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

### 16. 重新加载配置

```http
POST /api/reload
X-CSRF-Token: <token>
```

重新读取 `accounts.json`。

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

- **创建频率**：iCloud 限制别名创建频率，过快会返回 429（服务端自动重试最多 5 次）
- **Cookie 有效期**：约 24 小时，需定期更新
- **邮件读取**：依赖 IMAP 连接，超时默认 30 秒
- **请求体上限**：1 MiB
