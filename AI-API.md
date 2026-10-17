# AI 应用接入指南（API 秘钥鉴权）

面向**其他 AI 应用 / 自动化脚本**的只读接入文档。用一个 API 秘钥即可完成：

- 查看账号列表
- 查看某个账号的别名（Hide My Email 地址）
- 读取收件箱列表
- 读取邮件详情（完整正文）
- 批量读取邮件摘要（列表页的渐进式补全）

> 只需要**只读**能力时，不要申请 `accounts.write` / `aliases.write` / `inbox.delete` / `inbox.purge`。
> 本文所有接口都只读，按本文的权限组合即可。

---

## 1. 架构与调用方式

```
AI 应用 / Agent  ──HTTP(S)──▶  iCloud HME 管理服务  ──IMAP/Web──▶  iCloud
        │                              ▲
        └── X-API-Key: sk-… ───────────┘
```

- 传输：HTTP JSON，统一响应包 `{"success": true, "data": …}`
- 鉴权：请求头 `X-API-Key: <秘钥>`（也支持 `Authorization: Bearer <秘钥>`，两者同时存在时以 `X-API-Key` 为准）
- **秘钥请求豁免 CSRF**：不需要 Cookie，也不需要 `X-CSRF-Token`
- 建议把服务放在内网或 HTTPS 反向代理之后，秘钥只出现在服务端环境变量里

---

## 2. 创建秘钥（管理员操作，一次性）

1. 浏览器登录管理台 → 左侧「秘钥」→「创建秘钥」
2. 名称：例如 `ai-agent-readonly`
3. **账号使用范围**：勾选该 AI 应用允许访问的账号；不勾 = 全部账号（不推荐给外部应用）
4. **权限**：只勾选下表 5 项
5. 创建后**立即复制秘钥**（`sk-` 开头），之后也可在列表里「显示 / 复制」

### 最小权限组合

| 权限 ID | 标签 | 对应接口 |
|---|---|---|
| `accounts.list` | 查看账号列表 | `GET /api/accounts` |
| `aliases.list` | 查看别名 | `GET /api/aliases` |
| `inbox.list` | 读取收件箱列表 | `GET /api/inbox` |
| `inbox.get` | 读取邮件详情 | `GET /api/inbox/{id}` |
| `inbox.previews` | 读取邮件摘要 | `POST /api/inbox/previews` |

> 这 5 项都是只读权限，无法创建/删除别名、无法删除或清理邮件、无法修改账号。

---

## 3. 认证与错误约定

```http
GET /api/accounts HTTP/1.1
Host: hme.internal:8081
X-API-Key: sk-xxxxxxxxxxxxxxxxxxxxxxxx
```

| 状态码 | code | 含义 | AI 应用应如何处理 |
|---|---|---|---|
| 401 | `INVALID_API_KEY` | 秘钥不存在 / 已停用 / 格式错误 | 停止重试，提示运维检查秘钥 |
| 403 | `PERMISSION_DENIED` | 秘钥缺少该接口权限 | 停止重试，补权限 |
| 403 | `ACCOUNT_NOT_ALLOWED` | 目标账号不在秘钥的账号范围内 | 换账号或调整范围，**不要重试** |
| 400 | `VALIDATION_ERROR` | 参数缺失/越界（如 `limit` 超 50） | 修正参数后重试 |
| 404 | `ACCOUNT_NOT_FOUND` / `ALIAS_NOT_FOUND` / `TASK_NOT_FOUND` | 资源不存在（或不在秘钥范围内） | 视为不存在，不要重试 |
| 502 | `UPSTREAM_FAILURE` | iCloud/IMAP 上游失败（网络、风控、Cookie 失效） | 指数退避重试 1–3 次 |
| 429 | `RATE_LIMITED` | 登录限流（仅登录接口） | — |

**范围受限秘钥**：`account_ids` 非空时，所有接口只允许操作白名单内的账号；范围外账号一律 `403 ACCOUNT_NOT_ALLOWED`（任务类资源伪装成 404，避免探测存在性）。列表接口会自动过滤，只返回范围内的数据。

---

## 4. 接口详解

### 4.1 查看账号列表

```http
GET /api/accounts
X-API-Key: sk-…
```

```bash
curl -s http://hme.internal:8081/api/accounts -H "X-API-Key: $HME_KEY"
```

```json
{
  "success": true,
  "data": [
    {
      "id": "acc_d13aad7b",
      "name": "主账号",
      "real_email": "me@example.com",
      "icloud_email": "me@icloud.com",
      "host": "icloud.com",
      "status": "active",
      "alias_total": 480,
      "alias_active": 478,
      "has_cookies": true,
      "has_app_password": true,
      "has_proxy": false,
      "last_validated": "2026-10-15T15:56:00+08:00",
      "created_at": "2026-09-01T10:00:00+08:00"
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `id` | 账号 ID，后续所有接口的 `account_id` 都用它 |
| `status` | `active` / `pending` / `error`（`pending` 通常表示还没配置 Cookie，读信会失败） |
| `alias_total` / `alias_active` | 别名总数 / 已启用数 |
| `has_cookies` / `has_app_password` | 是否具备两条读信通道（IMAP 优先，Web API 回退） |
| `mailbox` | 绑定了外部收件邮箱时出现（`{provider,email,imap_host,imap_port}`） |

> 响应里**永远不含** Cookie、App Password、代理凭据。

---

### 4.2 查看别名

```http
GET /api/aliases?account_id=acc_d13aad7b
X-API-Key: sk-…
```

```bash
curl -s "http://hme.internal:8081/api/aliases?account_id=$ACC" -H "X-API-Key: $HME_KEY"
```

```json
{
  "success": true,
  "data": {
    "account_id": "acc_d13aad7b",
    "count": 2,
    "aliases": [
      {
        "email": "quiet-otter-42@icloud.com",
        "anonymousId": "anon_9f3c…",
        "label": "购物站",
        "active": true,
        "createdAt": "2026-09-12T08:31:00Z"
      }
    ]
  }
}
```

| 字段 | 说明 |
|---|---|
| `email` | HME 别名地址（可直接用于匹配收件人） |
| `anonymousId` | 内部别名 ID（删除/停用接口用；只读场景一般不需要） |
| `label` | 创建时写的标签，可能为空 |
| `active` | 是否启用 |

> 该接口一次性返回该账号**全部**别名（无分页）。别名很多时请缓存，不要每次对话都拉。

---

### 4.3 读取收件箱列表

```http
GET /api/inbox?account_id=acc_d13aad7b&limit=20&offset=0&days=7
X-API-Key: sk-…
```

| 参数 | 必填 | 说明 |
|---|---|---|
| `account_id` | ✅ | 账号 ID |
| `alias` | — | 只看发往某个别名的邮件（传 4.2 返回的 `email`） |
| `limit` | — | 每页条数 `1–50`，默认 `20` |
| `offset` | — | 偏移 `0–10000`，默认 `0` |
| `start` / `end` | — | 日期区间，`YYYY-MM-DD` 或 RFC3339；`end` 含当天；**优先于 `days`** |
| `days` | — | 最近 N 天，`0–3650`，默认 `7`；`0` = 不限 |
| `refresh` | — | `1` = 绕过服务端 TTL 缓存强制回源（默认走缓存） |

```bash
curl -s "http://hme.internal:8081/api/inbox?account_id=$ACC&alias=quiet-otter-42@icloud.com&limit=10&days=3" \
  -H "X-API-Key: $HME_KEY"
```

```json
{
  "success": true,
  "data": {
    "account_id": "acc_d13aad7b",
    "alias": "quiet-otter-42@icloud.com",
    "count": 1,
    "total": 37,
    "offset": 0,
    "method": "imap",
    "messages": [
      {
        "id": "1042",
        "from": "noreply@shop.example",
        "to": "quiet-otter-42@icloud.com",
        "subject": "Your verification code",
        "date": "2026-10-16T09:12:00Z",
        "preview": "",
        "folder": "INBOX"
      }
    ]
  }
}
```

| 字段 | 说明 |
|---|---|
| `id` | 邮件 ID，**读详情时原样回传** |
| `preview` | 列表阶段可能为空字符串；用 4.5 批量补摘要 |
| `folder` | `INBOX` / `Junk` / `垃圾邮件`；非 INBOX 的 `id` 形如 `Junk:88` |
| `method` | `imap` 或 `web_api`（读信通道，便于排障） |
| `total` | 符合日期过滤的总数（判断「还有下一页」） |
| `warning` | 降级原因（如 IMAP 不可用转 Web API），为空表示正常 |

**分页**：`offset += limit` 直到 `offset >= total`。`offset` 上限 10000。

> 邮件可能被 iCloud 判为垃圾，服务端已同时扫描 `INBOX`、`Junk`、`垃圾邮件` 三个文件夹，无需自己再查。

---

### 4.4 读取邮件详情（完整正文）

```http
GET /api/inbox/1042?account_id=acc_d13aad7b
X-API-Key: sk-…
```

```bash
curl -s "http://hme.internal:8081/api/inbox/$MSG_ID?account_id=$ACC" -H "X-API-Key: $HME_KEY"
```

```json
{
  "success": true,
  "data": {
    "id": "1042",
    "from": "noreply@shop.example",
    "to": "quiet-otter-42@icloud.com",
    "subject": "Your verification code",
    "date": "2026-10-16T09:12:00Z",
    "preview": "Your code is 482913 …",
    "folder": "INBOX",
    "body": "Your verification code is 482913.\n\n…",
    "content_type": "text/plain; charset=utf-8"
  }
}
```

要点：

- `body` 是**纯文本**：HTML 邮件已在服务端转成文本，可直接喂给模型
- 邮件 ID 必须与列表返回的 `id` 完全一致（`Junk:88` 这种带文件夹前缀的不要自己拼）
- 该接口**只走 IMAP**：账号没有 App Password 时会返回 `502 UPSTREAM_FAILURE`
- 正文可能很长，建议按需截断（如保留前 8000 字符）再送入上下文

---

### 4.5 批量读取邮件摘要

列表返回的 `preview` 可能为空；用这个接口按 ID 批量补摘要（**最多 20 个 ID**）。

```http
POST /api/inbox/previews?account_id=acc_d13aad7b
X-API-Key: sk-…
Content-Type: application/json

{"ids": ["1042", "Junk:88"]}
```

```bash
curl -s -X POST "http://hme.internal:8081/api/inbox/previews?account_id=$ACC" \
  -H "X-API-Key: $HME_KEY" -H "Content-Type: application/json" \
  -d '{"ids":["1042","Junk:88"]}'
```

```json
{
  "success": true,
  "data": {
    "1042": "Your code is 482913 …",
    "Junk:88": "Invoice #2026-10 is ready …"
  }
}
```

- 返回是 **`id → 摘要文本`** 的映射，未取到的 ID 不会出现在结果里
- 摘要上限约 2000 字符；超过 20 个 ID 会被截断为前 20 个
- 单次调用失败（`502`）时退避重试即可，不要并发轰炸同一账号

---

## 5. 典型调用流程（AI 取验证码）

```python
import os, time, requests

BASE = os.environ["HME_BASE_URL"]          # 例如 http://hme.internal:8081
KEY  = os.environ["HME_API_KEY"]           # sk-…（只放服务端环境变量）
S = requests.Session()
S.headers.update({"X-API-Key": KEY})

def get(path, **params):
    r = S.get(f"{BASE}{path}", params=params, timeout=15)
    r.raise_for_status()
    return r.json()["data"]

def post(path, payload, **params):
    r = S.post(f"{BASE}{path}", params=params, json=payload, timeout=20)
    r.raise_for_status()
    return r.json()["data"]

# 1) 选账号（范围受限秘钥只会看到自己范围内的账号）
accounts = get("/api/accounts")
acc = accounts[0]["id"]

# 2) 找目标别名的最近邮件
aliases = get("/api/aliases", account_id=acc)["aliases"]
target = next(a["email"] for a in aliases if a["label"] == "购物站")

inbox = get("/api/inbox", account_id=acc, alias=target, limit=10, days=1)

# 3) 补摘要（列表里 preview 为空时）
ids = [m["id"] for m in inbox["messages"] if not m["preview"]]
if ids:
    previews = post("/api/inbox/previews", {"ids": ids[:20]}, account_id=acc)
    for m in inbox["messages"]:
        m["preview"] = previews.get(m["id"], m["preview"])

# 4) 命中关键词后读完整正文
hit = next(m for m in inbox["messages"] if "code" in (m["subject"] + m["preview"]).lower())
full = get(f"/api/inbox/{hit['id']}", account_id=acc)
print(full["body"][:2000])
```

**轮询建议**：验证码类场景用 `days=1&limit=10`，间隔 3–5 秒、最多 20 次；不要带 `refresh=1`（会绕过缓存、增加上游压力与被风控概率）。列表默认走缓存，适合高频轮询。

---

## 6. 只读场景的行为约定

| 主题 | 约定 |
|---|---|
| 账号范围 | 秘钥白名单外的账号：读接口 `403 ACCOUNT_NOT_ALLOWED`；列表接口直接过滤掉 |
| 列表缓存 | `/api/inbox` 默认命中服务端 TTL 缓存；`refresh=1` 强制回源（受限频） |
| 空结果 | `messages: []` + `count: 0`，不是错误 |
| 时间格式 | 响应为 RFC3339（UTC 或带偏移）；请求日期用 `YYYY-MM-DD` 或 RFC3339 |
| 上限 | `limit ≤ 50`、`offset ≤ 10000`、`previews ≤ 20`、`days ≤ 3650` |
| 并发 | 同一账号串行调用；多账号可并行。IMAP 连接池按账号复用，过高并发会触发上游风控 |
| 幂等 | 本文 5 个接口全部只读，可安全重试（`502` 退避后重试） |

---

## 7. 安全清单（给接入方）

- 秘钥只放**服务端**环境变量或密钥管理服务；不要写进前端、日志、提示词或对话上下文
- 一个应用一把秘钥，命名可追溯（如 `ai-agent-prod`），并设置**账号范围**
- 定期轮换：管理台「秘钥」页可停用/删除，删除后立即失效（`401`）
- 管理台「最近使用 / 来源 IP」可用于审计异常调用
- 不要把 `body` 原文直接回显给终端用户前先做敏感信息过滤（验证码、链接、地址）

---

## 8. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `401 INVALID_API_KEY` | 秘钥被停用/删除，或复制时带了空格 | 重新复制；确认只放 `X-API-Key` 头 |
| `403 PERMISSION_DENIED` | 秘钥没勾对应权限 | 管理台补权限（需重建秘钥或编辑权限） |
| `403 ACCOUNT_NOT_ALLOWED` | 账号不在秘钥范围 | 用范围内的账号，或调整秘钥范围 |
| `400 VALIDATION_ERROR: account_id` | 缺少 `account_id` | 先调 `/api/accounts` 取 `id` |
| `502 UPSTREAM_FAILURE` | Cookie/App Password 失效、IMAP 被限流 | 退避重试；持续失败让运维在「账号」页重新验证 |
| 列表有邮件但 `preview` 为空 | 渐进式加载设计 | 调 `/api/inbox/previews` 补摘要 |
| 邮件 ID 带 `Junk:` 前缀 | 邮件在垃圾箱，UID 按文件夹生效 | 原样回传 ID，不要拆掉前缀 |

---

## 9. 相关文档

- 完整 API 契约（含写操作、清理任务、秘钥管理）：[API.md](API.md)
- 项目结构与安全边界：[ARCHITECTURE.md](ARCHITECTURE.md)
- 部署与配置：[README.md](README.md)
