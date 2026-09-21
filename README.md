# iCloud Hide My Email 本地管理工具

[English](#english) | 中文

通过逆向 iCloud Web 接口和 IMAP 邮件协议，实现 Apple iCloud 隐藏邮箱别名的创建、列出和邮件收取功能。内置中文管理界面（React 单页应用，随二进制内嵌分发）。

## 功能特性

- ✅ **中文管理界面** — 浏览器访问 `http://localhost:8081` 即开即用
- ✅ **创建 HME 别名** — 自动生成 iCloud 隐藏邮箱地址
- ✅ **列出所有别名** — 查看账号下的所有 HME 别名
- ✅ **批量管理别名** — 多选停用、启用或删除；长时间删除在后台执行并记录逐项结果
- ✅ **两类自动创建任务** — 自主任务（设定每天 5–50 个，系统自动分摊全天时刻）与定时任务（每隔固定分钟创建固定数量）；均到达目标总数后停止，失败立即暂停
- ✅ **三种标签生成方式** — 名称库自动轮换、前缀+顺序序号（`主邮箱001`）、前缀+随机哈希（`主邮箱k7m9`）
- ✅ **收取邮件** — 通过 IMAP 或 Web API 读取发到 HME 别名的邮件
- ✅ **双路径读信** — 邮件读取优先走 IMAP (App Password),无 App Password 时回退 Web API (Cookie)
- ✅ **多账号管理** — 支持多个 iCloud 账号并行管理
- ✅ **双认证模式** — Cookie (创建别名 + 读邮件回退) 和 App Password (IMAP 优先)
- ✅ **安全模型** — 单管理员会话、CSRF 校验、登录限流、响应脱敏
- ✅ **Apple 风格响应式 UI** — 深色模式、减少动态效果、移动端布局与键盘无障碍操作

## 快速开始

### 1. 安装

#### 方式一：下载二进制发布版（推荐）

从 [GitHub Releases](https://github.com/IFuZYI/icloud-hme/releases) 下载对应平台的二进制文件：

| 平台 | 文件 |
|---|---|
| Linux x86_64 | `icloud-hme_linux_amd64` |
| Linux ARM64 | `icloud-hme_linux_arm64` |
| macOS Intel | `icloud-hme_darwin_amd64` |
| macOS Apple Silicon | `icloud-hme_darwin_arm64` |
| Windows x86_64 | `icloud-hme_windows_amd64.exe` |

```bash
# 示例：Linux 下直接运行（必须先设置管理员密码）
export ICLOUD_HME_ADMIN_PASSWORD='change-this-before-running-2026'
chmod +x icloud-hme_linux_amd64
./icloud-hme_linux_amd64
```

#### 方式二：Docker

```bash
# 拉取镜像
docker pull ghcr.io/ifuzyi/icloud-hme:latest

# 运行（将本机 data 目录挂载进去）
docker run -d \
  --name icloud-hme \
  --restart unless-stopped \
  --read-only \
  --tmpfs /tmp:size=16m,mode=1777 \
  --security-opt no-new-privileges:true \
  -p 8081:8081 \
  -v "$(pwd)/data:/app/data" \
  -e ICLOUD_HME_ADMIN_PASSWORD='change-this-before-running-2026' \
  ghcr.io/ifuzyi/icloud-hme:latest
```

> ⚠️ 上面的密码仅为示例，**不可照抄**，请务必更换为至少 8 字符的强密码。

镜像支持 `linux/amd64` 和 `linux/arm64` 双架构，自动适配。

#### 方式三：源码编译（需要 Go 1.26+ 与 Node.js 22.12+ 双工具链）

```bash
# 前置要求: Go 1.26+、Node.js 22.12+
git clone https://github.com/IFuZYI/icloud-hme.git
cd icloud-hme

# 一键构建（安装前端依赖 → 前端测试 → 前端构建 → Go 测试 → 编译）
./build.sh

# 或者手动分步构建
npm --prefix web ci
npm --prefix web run build
go build -o icloud-hme .
```

#### 方式四：Docker Compose（推荐，长期运行 / 自建部署）

仓库内置 `docker-compose.yml`，一条命令完成构建与启动（含端口映射、数据卷、时区、日志轮转与健康检查）。

```bash
# 1. 进入项目目录并创建 .env（管理员密码必填）
cd icloud-hme
cat > .env <<'EOF'
ICLOUD_HME_ADMIN_PASSWORD='your-strong-password'
ICLOUD_HME_SESSION_TTL=12h
ICLOUD_HME_SECURE_COOKIE=false
ICLOUD_HME_PORT=8081
TZ=Asia/Shanghai
EOF

# 2. 构建镜像并后台启动
docker compose up -d --build

# 3. 查看运行状态（等待 healthy）
docker compose ps

# 4. 打开管理界面
#    浏览器访问 http://localhost:8081
```

常用命令：

```bash
# 查看实时日志（日志已限制为 10MB × 3 个文件）
docker compose logs -f icloud-hme

# 停止 / 启动 / 重启
docker compose stop
docker compose start
docker compose restart

# 升级：重新构建并滚动重启
docker compose up -d --build

# 完全清理（保留 data 卷，只删容器与镜像）
docker compose down
```

说明：

- **配置持久化**：`./data` 目录挂载到容器 `/app/data`，`accounts.json`、自动任务配置 `alias_task.json` 与运行日志 `alias_task_logs.json` 都保存在宿主机的 `./data` 下，容器重建不丢失。
- **时区**：默认 `Asia/Shanghai`，可在 `.env` 中通过 `TZ` 修改。
- **端口**：默认映射 `8081:8081`；在 `.env` 设置 `ICLOUD_HME_PORT=9090` 可改为从宿主机 `9090` 访问。
- **HTTPS 反代部署**：置于 TLS 反向代理后时，将 `ICLOUD_HME_SECURE_COOKIE` 设为 `true`。
- **构建门禁**：Docker 镜像构建只产出前端资源并编译二进制；lint、单元测试与 `go vet` 属于 CI 与本地 `./build.sh` 环节，不在镜像构建内执行，以缩短构建耗时。
- **运行时加固**：Compose 默认启用只读根文件系统、`no-new-privileges` 和独立临时目录；只有 `/app/data` 持久化目录可写。
- **镜像大小**：构建采用多阶段（前端 Node 构建 → Go 编译 → 精简 Alpine 运行时），最终镜像仅含二进制与 `ca-certificates`、`tzdata`。
- **健康检查**：容器内置 `wget` 探活 `/`，约 30 秒检测一次，失败 3 次标记 unhealthy（`docker compose ps` 可见）。

### 2. 安全配置（必读）

管理界面与 API 均需要管理员登录，升级后所有 API 都必须先通过 `POST /api/auth/login` 获取会话：

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `ICLOUD_HME_ADMIN_PASSWORD` | 管理员密码，**必填**，至少 8 字符 | 无（缺失时拒绝启动） |
| `ICLOUD_HME_SESSION_TTL` | 会话有效期 | `12h`（范围 `15m`–`168h`） |
| `ICLOUD_HME_SECURE_COOKIE` | 通过 TLS 反向代理部署时设为 `true` | `false` |

> **Breaking Change（v0.3+）**：升级后未设置 `ICLOUD_HME_ADMIN_PASSWORD` 将拒绝启动；
> 原有匿名 API 调用将收到 `401 AUTH_REQUIRED`。管理员会话只存内存，进程重启即失效。

### 3. 配置账号

在程序 `data/` 目录下创建 `accounts.json`（参考仓库内 `accounts.json.template`）：

```json
{
  "accounts": {
    "acc_1": {
      "id": "acc_1",
      "name": "主号",
      "real_email": "owner@example.com",
      "icloud_email": "owner@icloud.com",
      "cookies": {
        "X-APPLE-WEBAUTH-TOKEN": "v=1:t=AQAAAAB...",
        "X-APPLE-WEBAUTH-USER": "d=...:s=...",
        "X_APPLE_WEB_KB": "..."
      },
      "host": "icloud.com",
      "proxy": "http://user:pass@host:port",
      "app_password": "xxxx-xxxx-xxxx-xxxx",
      "status": "active"
    }
  }
}
```

> **提示:** 也可以通过管理界面的「账号」页面动态添加账号，无需手动编辑 JSON 文件。`cookies`、`app_password`、`proxy` 都是可选的。

### 4. 启动服务

```bash
# 二进制方式（默认 data 目录）
export ICLOUD_HME_ADMIN_PASSWORD='your-strong-password'
./icloud-hme_linux_amd64

# 指定端口和数据目录
./icloud-hme_linux_amd64 -addr :9090 -data ./my_data

# 调试模式（启用请求日志）
./icloud-hme_linux_amd64 -debug

# 查看完整参数
./icloud-hme_linux_amd64 -h
```

服务默认监听 `:8081`。浏览器打开 `http://localhost:8081` 进入管理界面（账号 / 别名 / 收件箱）。完整 API 契约见 [API.md](API.md)。

### 5. 使用管理界面

- **账号**：添加账号时可直接粘贴 Cookie JSON；区域下拉框支持全球区和中国区。
- **别名**：支持搜索、状态筛选、创建时间排序和多选批量操作。确认批量删除后弹窗立即关闭，结果通过通知和任务日志反馈。
- **自动任务**：两种任务类型可选——自主任务设定每天创建数量（5/10/…/50），系统自动把创建时刻分摊到全天；定时任务设定间隔分钟与每批数量。标签可选名称库自动轮换、前缀顺序序号或前缀随机哈希。展示进度、下次执行时间和失败原因，达到目标总数后自动停止。
- **日志**：查看自动任务及批量操作的逐项成功/失败记录和详细原因。
- **收件箱**：按账号、别名、数量和时间范围筛选邮件。

## API 接口

> **认证**：除 `POST /api/auth/login` 与 `GET /api/auth/session` 外，所有 `/api` 接口都需要管理员会话 Cookie（`hme_session`）；非 GET/HEAD/OPTIONS 请求还需携带 `X-CSRF-Token` 请求头。完整契约与 curl 示例见 [API.md](API.md)。

### 核心接口

#### 创建 HME 别名

```bash
POST /api/create

# 请求体
{
  "account_id": "acc_1",      # 必填: 账号 ID
  "label": "GitHub"           # 必填: 从名称库选择的标签
}

# 响应
{
  "success": true,
  "data": {
    "email": "xyz123@icloud.com",
    "label": "GitHub",
    "created_at": "2024-01-15T10:30:00Z",
    "account_id": "acc_1"
  }
}
```

#### 读取邮件

```bash
GET /api/inbox?account_id=acc_1&alias=xyz123@icloud.com&limit=20&days=7

# 参数说明:
#   account_id - 必填: 账号 ID
#   alias      - 可选: 只读取发到该别名的邮件
#   limit      - 可选: 返回邮件数量 (默认 20)
#   days       - 可选: 查找最近几天的邮件 (默认 7,仅 IMAP 模式)

# 响应
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
        "from": "noreply@example.com",
        "to": "xyz123@icloud.com",
        "subject": "欢迎注册",
        "preview": "感谢您的注册...",
        "date": "2026-07-09T14:32:10+08:00"
      }
    ]
  }
}

# 读取方式 (自动选择):
#   method: "imap"    — 通过 App Password 认证 (优先)
#   method: "web_api" — 通过 Cookie 认证,无需 App Password (回退)
```

### 账号管理接口

#### 列出所有账号

```bash
GET /api/accounts

# 响应
{
  "success": true,
  "data": [
    {"id": "acc_1", "name": "主号"},
    {"id": "acc_2", "name": "副号"}
  ]
}
```

#### 添加账号

**简化版（cookies 可选）:**

```bash
POST /api/accounts

# 请求体
{
  "name": "新账号",
  "host": "icloud.com",           # 可选
  "proxy": "http://..."           # 可选
}

# 响应 - 状态为 pending,需登录
{
  "success": true,
  "data": {
    "id": "acc_xxx",
    "name": "新账号",
    "status": "pending"
  }
}
```

**完整版（带 Cookie）:**

```bash
POST /api/accounts

# 请求体
{
  "name": "新账号",
  "cookies": "{\"x-apple-session-token\":\"token_value\"}",  # JSON 或 Header 格式
  "host": "icloud.com",           # 可选
  "proxy": "http://..."           # 可选
}

# 响应
{
  "success": true,
  "data": {
    "id": "acc_3",
    "name": "新账号",
    "status": "active"
  }
}
```

#### 账号登录（获取 Cookie）

```bash
POST /api/accounts/:id/login

# 请求体
{
  "password": "用户的常规iCloud密码",  # 不是 App Password
  "otp_code": "123456"                  # 可选,2FA 验证码
}

# 响应
{
  "success": true,
  "data": {
    "id": "acc_1",
    "cookies": {
      "x-apple-session-token": "...",
      "X-APPLE-WEBAUTH-TOKEN": "..."
    }
  }
}
```

#### 删除账号

```bash
DELETE /api/accounts/:id

# 响应
{
  "success": true,
  "data": {"id": "acc_3"}
}
```

#### 设置 App Password

```bash
POST /api/accounts/:id/password

# 请求体
{
  "icloud_email": "your_email@icloud.com",
  "app_password": "xxxx-xxxx-xxxx-xxxx"
}

# 响应
{
  "success": true,
  "data": {
    "id": "acc_1",
    "icloud_email": "your_email@icloud.com"
  }
}
```

### 别名管理接口

#### 列出所有别名

```bash
GET /api/aliases?account_id=acc_1

# 响应
{
  "success": true,
  "data": {
    "account_id": "acc_1",
    "count": 15,
    "aliases": [
      {
        "email": "xyz123@icloud.com",
        "label": "注册某网站",
        "created_at": "2024-01-15T10:30:00Z"
      }
    ]
  }
}
```

#### 停用别名

```bash
POST /api/aliases/:id/deactivate

# 请求体
{
  "account_id": "acc_1"
}

# 响应
{
  "success": true,
  "data": {
    "anonymous_id": "abc123",
    "success": true
  }
}
```

#### 激活别名

```bash
POST /api/aliases/:id/reactivate

# 请求体
{
  "account_id": "acc_1"
}

# 响应
{
  "success": true,
  "data": {
    "anonymous_id": "abc123",
    "success": true
  }
}
```

#### 删除别名

```bash
DELETE /api/aliases/:id

# 请求体
{
  "account_id": "acc_1"
}

# 响应
{
  "success": true,
  "data": {
    "anonymous_id": "abc123"
  }
}
```

#### 批量停用、启用或删除别名

```bash
POST /api/aliases/batch

{
  "account_id": "acc_1",
  "action": "delete",
  "aliases": [
    {"anonymous_id": "abc123", "email": "alias1@icloud.com"},
    {"anonymous_id": "def456", "email": "alias2@icloud.com"}
  ]
}
```

单次最多 200 个别名；`action` 可为 `deactivate`、`reactivate` 或 `delete`。删除操作之间间隔 3 秒。响应包含 `succeeded`、`failed` 和逐项 `results`；若操作已完成但审计日志写入失败，还会返回 `logging_error`。

### 自动创建任务接口

自动创建任务分为两种类型，均以低频方式为指定账号创建 HME 别名，达到目标总数（`target_count`，1–999）后自动停止：

- **自主任务（`mode: auto`）**：设定每天创建数量 `daily_limit`（5/10/15/…/50），系统按 24 小时自动分摊执行时刻，每次创建 1 个。
- **定时任务（`mode: scheduled`）**：每隔 `interval_minutes`（20–1440 分钟）创建 `batch_count` 个（1–20）。

标签可用三种方式生成（`label_mode`）：

- `library`：内置常用服务名称库自动轮换（如 GitHub、Google Workspace、Notion、Slack、淘宝）。
- `sequential`：`label_prefix` + 补零序号（如 `主邮箱001`）。
- `hash`：`label_prefix` + 长度 `hash_length`（4–8）的随机哈希（如 `主邮箱k7m9`）。

| 接口 | 说明 |
|---|---|
| `GET /api/alias-tasks` | 列出所有任务（含进度与下次执行时间） |
| `POST /api/alias-tasks` | 新建任务（默认启用，10 秒后执行首轮） |
| `PATCH /api/alias-tasks/:id` | 编辑任务配置（保存后自动重新启用） |
| `POST /api/alias-tasks/:id/toggle` | 启用 / 暂停任务 |
| `DELETE /api/alias-tasks/:id` | 删除任务 |
| `GET /api/alias-task-logs` | 查看任务运行日志（每个邮箱的成功/失败记录） |

#### 新建任务

```bash
POST /api/alias-tasks

# 自主任务：每天 10 个，名称库标签
{
  "account_id": "acc_1",
  "mode": "auto",
  "target_count": 100,
  "daily_limit": 10,
  "label_mode": "library"
}

# 定时任务：每 60 分钟 2 个，前缀+顺序序号标签
{
  "account_id": "acc_1",
  "mode": "scheduled",
  "target_count": 50,
  "interval_minutes": 60,
  "batch_count": 2,
  "label_mode": "sequential",
  "label_prefix": "主邮箱"
}

# 响应（自主任务示例）
{
  "success": true,
  "data": {
    "id": "task_ab12cd34",
    "enabled": true,
    "account_id": "acc_1",
    "mode": "auto",
    "interval_minutes": 144,
    "batch_count": 1,
    "daily_limit": 10,
    "label_mode": "library",
    "max_total": 100,
    "created_count": 0,
    "next_number": 1,
    "daily_count": 0,
    "next_run": "2026-09-10T12:00:00+08:00"
  }
}
```

字段说明：

| 字段 | 必填 | 说明 |
|---|---|---|
| `account_id` | ✅ | 目标账号 ID |
| `target_count` | ✅ | 累计创建目标（1–999），达到后任务自动停止 |
| `mode` | ⭕ | `auto`（默认）或 `scheduled` |
| `daily_limit` | auto 必填 | 自主任务每天创建数量，取值 5/10/…/50 |
| `interval_minutes` | scheduled 必填 | 定时任务执行间隔（20–1440 分钟） |
| `batch_count` | scheduled 必填 | 定时任务每个周期创建数量（1–20） |
| `label_mode` | ⭕ | `library`（默认）/ `sequential` / `hash` |
| `label_prefix` | sequential/hash 必填 | 手动标签前缀（最长 32 字符） |
| `hash_length` | ⭕ | hash 模式后缀长度（4–8，默认 4） |

> 自主任务的 `interval_minutes` 由系统按每日数量自动分摊（24×60 ÷ 每日数量，且不低于 20 分钟），无需手动填写。定时任务的 `daily_limit` 固定为账号每日安全上限 50。

#### 启用 / 暂停

```bash
POST /api/alias-tasks/:id/toggle
```

暂停后 `next_run` 清空，界面显示“已暂停”；重新启用后按 `现在 + 间隔` 重新推算下次执行，不会错过触发。

#### 任务运行规则

- 新建任务默认启用，保存后 **10 秒缓冲** 执行首轮，之后按 `interval_minutes` 间隔执行
- 每个周期创建的数量：自主任务固定 1 个；定时任务为 `batch_count` 个（受剩余目标与当日额度约束，批内有短暂间隔）
- 标签按 `label_mode` 生成；名称仅是本地标识，不代表已在对应服务注册
- 任意创建失败或 iCloud 提示都会**立即暂停**任务并清空 `next_run`，须由用户检查后手动恢复
- 达到 `daily_limit` 或账号每日总上限 50 时，推迟到次日 00:05 后继续
- 同一账号任意两次创建尝试至少间隔 20 分钟
- 达到 `target_count` 后任务自动停止并清空 `next_run`
- 任务配置持久化在 `data/alias_task.json`，运行日志持久化在 `data/alias_task_logs.json`（重启后自动恢复）
- 旧版任务向后兼容：缺 `mode` 视为 `scheduled`，缺 `label_mode` 视为 `library`

## 认证方式

### 方式一: Cookie 认证 (推荐,功能最完整)

Cookie 认证可实现所有功能:创建别名、读取邮件、管理别名。

**适用范围:**
- 创建/停用/激活/删除 HME 别名 ✅
- 读取邮件 (通过 iCloud Web API,无需 App Password) ✅

**获取 Cookie:**

1. 使用浏览器登录 [icloud.com](https://www.icloud.com) 或 [icloud.com.cn](https://www.icloud.com.cn) (国区)
2. 打开浏览器开发者工具 (F12)
3. 进入 Application → Cookies
4. 导出全部 Cookie 为 `{"key":"value"}` 格式的 JSON

```json
{
  "X-APPLE-WEBAUTH-TOKEN": "v=1:t=AQAAAAB...",
  "X-APPLE-WEBAUTH-USER": "d=...:s=...",
  "X_APPLE_WEB_KB": "..."
}
```

**关键 Cookie (必需):**
- `X-APPLE-WEBAUTH-TOKEN` — 认证 token
- `X-APPLE-WEBAUTH-USER` — 含 dsid (`v=1:s=1:d=22789132008`)
- `X-APPLE-WEBAUTH-HSA-TRUST` — 设备信任 token
- `X-APPLE-DS-WEB-SESSION-TOKEN` — 会话 token

**注意:** 导出的 Cookie 值不要包含多余的引号或转义字符。

### 方式二: App Password 认证 (IMAP,优先读邮件)

App Password 用于 IMAP 读取邮件,是邮件读取的优先路径 (支持服务端按收件人搜索)。

**生成 App Password:**

1. 登录 [appleid.apple.com](https://appleid.apple.com)
2. 进入 "登录和安全" → "App 专用密码"
3. 生成新密码,用于此工具

### 邮件读取双路径

`GET /api/inbox` 自动选择读取方式:

1. **优先: IMAP (App Password)** — 设置了 App Password 时使用,支持服务端按收件人 (`TO`) 搜索
2. **回退: Web API (Cookie)** — 无 App Password 或 IMAP 失败时,通过 `mccgateway` 端点读取,本地按别名过滤

响应中包含 `"method": "web_api"` 或 `"method": "imap"` 字段,标识实际使用的读取方式。

## 项目架构

```
icloud-hme/
├── main.go                 # 入口: 读取安全配置、加载账号、启动服务
├── web/                    # 前端工程 (React + TypeScript + Vite)
│   └── src/                #   管理界面源码
├── Dockerfile              # 多阶段生产镜像
├── docker-compose.yml      # 持久化、健康检查与运行时加固
├── data/                   # 运行数据目录 (自动生成，Git 忽略)
├── go.mod
└── internal/
    ├── account/
    │   ├── manager.go      # 多账号管理器 (持久化、客户端工厂)
    │   └── public.go       # 公开 DTO (Summary) 与输入校验
    ├── auth/
    │   ├── manager.go      # 管理员会话 + CSRF
    │   └── limiter.go      # 登录失败限流
    ├── hme/
    │   ├── client.go       # iCloud HME Web 客户端 (Cookie 认证)
    │   └── auth.go         # SRP 登录 (账号密码 + 2FA 获取 Cookie)
    ├── mail/
    │   ├── client.go       # IMAP 邮件客户端 (App Password 认证)
    │   └── web_client.go   # Web 邮件客户端 (Cookie 认证,无需 App Password)
    ├── server/
    │   ├── server.go       # 路由分组 (认证 + CSRF)
    │   ├── backend.go      # 业务接口与 Manager 适配器
    │   ├── auth.go         # 登录/会话/退出 handler 与中间件
    │   ├── account_handlers.go  # 账号管理 handler
    │   ├── auto_task.go    # 自动创建任务调度器 (间隔执行/总数上限/自动暂停)
    │   ├── auto_task_logs.go    # 任务运行日志持久化
    │   ├── alias_task_handlers.go # 自动任务 API handler
    │   └── middleware.go   # 安全响应头、请求上限
    └── webui/
        └── embed.go        # 内嵌前端资源 + SPA fallback
```

### 核心模块

- **account.Manager**: 管理多个 iCloud 账号,负责配置持久化和客户端创建
- **server.autoTaskManager**: 自动创建任务调度器,负责任务持久化、间隔触发、总数上限与账号异常自动暂停
- **hme.Client**: 封装 iCloud HME Web API,支持 Cookie 认证
- **hme.auth**: SRP 协议登录,支持账号密码 + 可选 2FA
- **mail.Client**: IMAP 邮件客户端 (App Password,优先读邮件)
- **mail.WebClient**: 通过 iCloud Web API (mccgateway) 读取邮件,无需 App Password
- **server.Server**: HTTP API 服务 + 管理界面静态资源

## 技术栈

- **Go 1.26+** / **Gin** — HTTP 框架
- **React 19 + TypeScript + Vite 8** — 管理界面
- **go-imap** — IMAP 协议实现
- **tls-client** — TLS 指纹模拟 (绕过 iCloud 反爬)

## 常见问题

### Q: 创建别名返回 401/403 错误?

**A:** Cookie 已过期，需要重新获取。iCloud Cookie 有效期通常为 24 小时。

### Q: 读取邮件返回超时?

**A:** 检查网络连接，确保可以访问 `imap.mail.me.com:993`。

### Q: 如何查看某个别名收到了哪些邮件?

**A:** 调用 `GET /api/inbox?account_id=acc_1&alias=your_alias@icloud.com`

### Q: 支持同时管理多个 iCloud 账号吗?

**A:** 支持，在 `accounts.json` 中配置多个账号即可，每个账号有独立的 `id`。

## 开发指南

### 本地开发

```bash
# 前端开发模式 (vite dev server, /api 代理到 :8081)
npm --prefix web ci
npm --prefix web run dev

# 后端开发模式
export ICLOUD_HME_ADMIN_PASSWORD='your-strong-password'
go run main.go -debug

# 前端检查 (lint + test + build)
npm --prefix web run check

# 完整构建 (含前端)
./build.sh

# 交叉编译
GOOS=linux GOARCH=amd64 go build -o icloud-hme .
GOOS=windows GOARCH=amd64 go build -o icloud-hme.exe .
```

### 发布

推送 `v*` tag 到 GitHub 自动触发 CI：

```bash
git tag v0.2.0 && git push origin --tags
```

Actions 会自动构建多平台二进制、Docker 镜像（`ghcr.io/ifuzyi/icloud-hme`）并创建 Release。

### 代码规范

- 代码注释使用中文
- 错误信息返回给用户时使用中文
- API 响应格式统一: `{success: bool, data: any, message: string}`

## 许可证

MIT License

---
## 社区

友情链接：[LINUX DO](https://linux.do)

## English

A local management tool for Apple iCloud Hide My Email (HME) aliases, supporting creation, listing, and email reading through reverse-engineered iCloud Web API and IMAP protocol. Ships with a built-in Chinese management UI (React SPA embedded in the single binary).

### Features

- Built-in management UI at `http://localhost:8081`
- Create HME aliases automatically
- Low-frequency scheduled creation: choose a target count, a 20–60 minute interval and a daily cap; one alias per run, labels rotate from a common-service library, failures pause the task
- Batch deactivate, reactivate, and delete with per-item audit logs; long deletions do not block the confirmation dialog
- List all aliases for an account
- Read emails sent to HME aliases via IMAP or Web API
- Manage multiple iCloud accounts
- Dual authentication: Cookie and App Password
- Security: single-admin session, CSRF checks, login rate limiting, redacted API responses
- Responsive Apple-inspired UI with dark mode, reduced-motion support, and keyboard accessibility

### Quick Start

#### Option 1: Binary (GitHub Releases)

Download the latest binary from [GitHub Releases](https://github.com/IFuZYI/icloud-hme/releases):

| Platform | File |
|---|---|
| Linux x86_64 | `icloud-hme_linux_amd64` |
| Linux ARM64 | `icloud-hme_linux_arm64` |
| macOS Intel | `icloud-hme_darwin_amd64` |
| macOS Apple Silicon | `icloud-hme_darwin_arm64` |
| Windows x86_64 | `icloud-hme_windows_amd64.exe` |

```bash
# Linux example (admin password is REQUIRED, min 8 chars)
export ICLOUD_HME_ADMIN_PASSWORD='change-this-before-running-2026'
chmod +x icloud-hme_linux_amd64
./icloud-hme_linux_amd64
```

#### Option 2: Docker

```bash
docker pull ghcr.io/ifuzyi/icloud-hme:latest

docker run -d \
  --name icloud-hme \
  --restart unless-stopped \
  --read-only \
  --tmpfs /tmp:size=16m,mode=1777 \
  --security-opt no-new-privileges:true \
  -p 8081:8081 \
  -v "$(pwd)/data:/app/data" \
  -e ICLOUD_HME_ADMIN_PASSWORD='change-this-before-running-2026' \
  ghcr.io/ifuzyi/icloud-hme:latest
```

> The password above is only an example — do NOT copy it. Use a strong password with at least 8 characters.

#### Option 3: Docker Compose

```bash
cat > .env <<'EOF'
ICLOUD_HME_ADMIN_PASSWORD=your-strong-password
ICLOUD_HME_SESSION_TTL=12h
ICLOUD_HME_SECURE_COOKIE=false
ICLOUD_HME_PORT=8081
TZ=Asia/Shanghai
EOF

docker compose up -d --build
docker compose ps
```

#### Option 4: Build from source (Go 1.26+ and Node.js 22.12+)

```bash
git clone https://github.com/IFuZYI/icloud-hme.git
cd icloud-hme

# One-shot build (frontend deps → frontend test → frontend build → Go test → binary)
./build.sh

# Or step by step
npm --prefix web ci
npm --prefix web run build
go build -o icloud-hme .
```

### Configuration

| Env var | Description | Default |
|---|---|---|
| `ICLOUD_HME_ADMIN_PASSWORD` | Admin password, **required**, min 8 chars | none (refuses to start) |
| `ICLOUD_HME_SESSION_TTL` | Session TTL | `12h` (range `15m`–`168h`) |
| `ICLOUD_HME_SECURE_COOKIE` | Set `true` when deployed behind TLS | `false` |

> **Breaking change (v0.3+)**: without `ICLOUD_HME_ADMIN_PASSWORD` the server refuses to start; all API endpoints now require login (`401 AUTH_REQUIRED`). Admin sessions are in-memory only and are lost on restart.

Create `data/accounts.json` (see `accounts.json.template`) and start the server (default port `:8081`). Open `http://localhost:8081` to use the management UI. Full API contract: [API.md](API.md).
