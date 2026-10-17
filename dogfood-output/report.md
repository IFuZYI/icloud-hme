# Dogfood QA Report

**Target:** http://127.0.0.1:8081 (iCloud HME 管理台)
**Date:** 2026-10-09
**Scope:** 全站探索性 QA × 六轮（第一轮：日期选择器与筛选栏；第二轮：全量——全部页面 × 3 视口 × 有数据状态 × 对话框/弹层；第三轮：日期面板年份视图——用户截图定位；第四轮：全量 UI 复测——6 视口矩阵 + 交互深度 + 对比度/暗色模式审计；第五轮：2FA 登录对话框恢复路径；第六轮：新增功能验证（多账号默认不选/凭据 chips/读缓存 refresh/连接复用/日志清理）+ 全站回归）
**Tester:** Hermes Agent (automated exploratory QA)

---

## Executive Summary

**第一轮（7 项，全部修复）：** 日期面板移动端溢出、antd 与设计系统不一致、移动端导航、面板底部按钮等。

**第二轮（8 项新发现，7 修复 + 1 驳回）：** 引入测试数据后暴露出的表格渲染缺陷、原始 ISO 时间泄漏、`toLocaleString` 格式不统一、别名页工具栏移动端溢出等。

**第三轮（1 项 Critical 根因 + 1 项 Medium，全部修复）：** 全局 `table { min-width: 920px }` 泄漏进 antd 日期面板（面板内日历/年份网格也是 `<table>`），把网格撑到 920px → 年份视图第三列（2021/2024/2027）被裁掉。用户截图即此问题。

**第四轮（1 项 High + 1 项 Medium 系统性问题，全部修复）：** 暗色模式下 antd 组件（DatePicker 等）保持白色硬编码、与全黑页面严重割裂；设计系统多个文字 token 低于 WCAG AA 4.5:1（tertiary 3.18:1、danger 4.17:1、暗色主按钮白字 3.02:1、skip-link 3.02:1）。

**第五轮（0 项新缺陷；1 项 Low 观察项，验证性 QA）：** 验证「2FA 验证码推送失败」恢复路径（本轮新增功能）。三态全覆盖：`push_sent=false`（警示条 + 两个恢复按钮）、`push_sent=true`（正常提示）、旧服务端缺字段（视为已推送不误报）。恢复链路实测：重发成功警示消失、短信通道 `method=sms`+`phone_id` 正确随提交带上。键盘可达性：5 个可聚焦元素、Tab 循环无遗漏、Escape 关闭。对比度：暗色警示条 6.19:1、暗色恢复按钮 5.7:1、浅色 4.7:1/4.51:1，全部过 WCAG AA。观察项：恢复按钮为链接式（高 25px），触控目标小于 Apple HIG 44px 建议——与项目既有 `.text-button` 约定一致，记录不修。

| 轮次 | 🔴 Critical | 🟠 High | 🟡 Medium | 🔵 Low | 合计 |
|------|------------|---------|-----------|--------|------|
| 第一轮 | 0 | 2 | 2 | 3 | 7（全部修复/驳回） |
| 第二轮 | 0 | 2 | 2 | 4 | 8（7 修复 + 1 驳回） |
| 第三轮 | 1 | 0 | 1 | 0 | 2（全部修复） |
| 第四轮 | 0 | 1 | 1 | 0 | 2（全部修复） |
| 第五轮 | 0 | 0 | 0 | 1 | 1（观察项，不修） |
| 第六轮 | 2 | 0 | 1 | 2 | 5（全部修复） |

**第六轮摘要（新增功能验证 + 全站回归，5 项全部修复）：** 两个 Critical 均与本轮新功能/真实使用场景直接相关——①「按别名筛选无命中 → `messages:null` → 前端 `.filter()` TypeError → 整页白屏」（React 无错误边界，白屏无任何提示）；②「163/126 邮箱按别名筛选永远 0 结果」——网易 Coremail 对 `SEARCH TO/FROM/SUBJECT/HEADER/TEXT` 静默返回空（返回 OK 但不报错），原始协议双探针（imaplib + go-imap）实测确认，`ALL`/`SINCE`/`UID` 正常。②正是用户核心场景「按别名查验证码」在 163 上的完全失效。修复后真实 163 实测：白屏 URL 正常渲染、按别名搜索 0 → 1 封（命中 SpaceXAI 验证码邮件）。其余 3 项为 Medium/Low 体验项（未选账号摘要误导、chips 对比度差 0.05 不到 AA、0 条清理提示不友好）。新增功能本身（多账号默认不选、凭据 chips、refresh 缓存、日志清理对话框）经三处验证（单元测试 + API 实测 + 浏览器实测）均无缺陷。

**Overall Assessment:** 六轮共 25 项发现（24 修复/驳回 + 1 观察项）。第三轮的关键教训：**antd 面板经 portal 渲染到 body，项目全局的元素级选择器（`table`/`th`/`td`/`tbody tr`/`button`）会泄漏进面板内部**，造成面板布局损坏。第四轮的关键教训：**antd 主题与项目 CSS 的暗色机制是两套**——CSS 走 `@media (prefers-color-scheme: dark)`，而 antd 的 ConfigProvider 需要 JS 显式传入 `darkAlgorithm`，只做前者会让 antd 组件在暗色页面上保持白底；且设计 token 的对比度需要实测而非目测（`#86868b` 看似「浅灰正常」，实测仅 3.18:1）。第五轮验证了「推送失败 → 用户可见 → 可恢复」的完整链路：**此前的缺陷模式是「失败只写服务端日志」**——用户界面与真实状态脱节（显示「请输入验证码」而验证码永远不会到），恢复手段（重发/短信端点）虽已实现但 UI 从未接上。第六轮的关键教训：**上游「静默失效」是最隐蔽的缺陷类**——163 的 SEARCH 返回 OK 但零结果、后端 nil 切片序列化成 null、前端对契约过于信任，三者叠加造成「白屏 + 功能静默失效」；修复必须同时钉住契约（`messages` 恒为数组）、加防御层（前端 null 守卫）与行为兜底（SEARCH 零命中且箱非空时本地复核）。

**方法论说明（五轮均严格执行「measure, don't eyeball」）：** vision 模型在四轮中共给出 **12 次与 DOM 测量直接矛盾的描述**（如声称"年份 2028-2030 缺失"而 DOM 实测 12 格全在、"日历与时间列重叠"而实测间隙 18px 等）。所有结论均以 `getBoundingClientRect()` / `getComputedStyle()` / CDP `CSS.getMatchedStylesForNode` 的测量为准；vision 仅用于初筛可疑区域，不用于定量判断。第四轮的对比度结论全部用 WCAG 相对亮度公式计算（含 alpha 合成）。第五轮对登录恢复路径采用**页面内 fetch 拦截**（真实浏览器 + 真实 React 代码 + 受控响应），逐态驱动 UI 并测量渲染与请求体——不依赖真实 2FA 账号即可覆盖三态。

---

## 第六轮 Issues（本轮新增，5 项全部修复）

### Issue #21: 按别名筛选无命中时整页白屏（Critical）

| Field | Value |
|-------|-------|
| **Severity** | 🔴 Critical |
| **Category** | Functional |
| **URL** | `/inbox?account_id=acc_335ee74c&alias=temper.clap.2e%40icloud.com` |

**Description:**
当按别名筛选且该别名当前无命中邮件时，后端 `FindByRecipientRange` 返回 nil 切片，JSON 序列化为 `"messages":null`；前端 `data.messages.filter(...)` 抛 `TypeError: Cannot read properties of null (reading 'filter')`，React 无错误边界 → 整个应用白屏（无任何错误提示、无导航、无恢复手段，只能手动改 URL）。

**Steps to Reproduce:**
1. 打开 `/inbox?account_id=acc_335ee74c&alias=temper.clap.2e%40icloud.com`（一个在日期范围内无邮件的别名）
2. 观察页面白屏

**Expected Behavior:** 显示「暂无邮件」空态，页面可用。

**Actual Behavior:** 整页白屏；控制台 `Uncaught TypeError: Cannot read properties of null (reading 'filter')`。

**证据:**
- 服务端响应字节级确认：`{"success":true,"data":{...,"messages":null,...}}`
- 浏览器控制台：`ERROR: Uncaught TypeError ... @ index-DG8QNQa6.js:177` + `REJECTION: TypeError`
- MEDIA:/home/icloud-hme/dogfood-output/screenshots/r6-06-inbox-alias-white-screen.png

**修复:** 后端契约钉死 `messages` 恒为 `[]`（`managerBackend.ListInbox` 统一归一化）+ 前端防御性 null 守卫（`data.messages ?? []`）。修复后同一 URL 正常渲染，`messages:[]`。

---

### Issue #22: 163/126 邮箱「按别名筛选」永远 0 结果（Critical）

| Field | Value |
|-------|-------|
| **Severity** | 🔴 Critical |
| **Category** | Functional |
| **URL** | `/inbox?account_id=acc_335ee74c&alias=temper.clap.2e%40icloud.com` |

**Description:**
真实 163 收件邮箱下，列表中明明有 `to=temper.clap.2e@icloud.com` 的邮件，但按该别名筛选永远返回 0 条。根因：网易 Coremail 对 `SEARCH TO/FROM/SUBJECT/HEADER/TEXT` 条件**静默返回空结果**（返回 `OK` 但不报错，也不给任何错误提示），客户端只依赖服务端 SEARCH，于是「按别名查验证码」这一核心场景在 163 上完全失效。原始协议双探针（Python imaplib + go-imap SetDebug）实测确认：`TO`/`FROM`/`SUBJECT`/`TEXT` 全部返回 `* SEARCH`（空），而 `ALL`/`SINCE`/`UID SEARCH` 正常返回全部 UID。

**Steps to Reproduce:**
1. 账号接入 163 收件邮箱（imap.163.com:993）
2. 收件箱确认存在发给某别名的邮件（列表可见）
3. 按该别名筛选 → 结果 0 条

**Expected Behavior:** 返回该别名的邮件。

**Actual Behavior:** 永远 0 条（且无错误提示）。

**证据:**
- 原始协议（imaplib）：`UID SEARCH TO "temper"` → `OK [b'']`（空）而 `UID SEARCH ALL` → 全部 5 个 UID
- go-imap 探针（SetDebug 原始协议帧）：`UID SEARCH CHARSET UTF-8 TO "..."` → `* SEARCH`（空）+ `OK SEARCH completed`
- API 实测：无 alias → count=5（含 to=temper.clap.2e）；alias=temper.clap.2e → count=0

**修复:** `forEachByRecipientRange` 增加行为兜底：SEARCH 零命中且邮箱非空时，退化为本地信封过滤复核（`forEachRecentMatchingRange`）——命中用本地结果，仍无命中才判定真为空。修复后真实 163 实测：0 → 1 封（命中 SpaceXAI 验证码邮件）。

---

### Issue #23: 未选账号时收件箱摘要与按钮误导（Medium）

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | UX |
| **URL** | `/inbox`（多账号初始态） |

**Description:**
本轮「多账号默认不自动选择」新功能下，未选账号时仍显示「共 0 封 / 读取方式：IMAP」统计摘要——用户会误读为「已查询过且没有邮件」（实际是「还没查」）；且「刷新」「查询」按钮可点击但静默无响应（无请求、无提示）。

**Steps to Reproduce:**
1. 打开 `/inbox`（不选账号）
2. 观察摘要行显示「共 0 封」
3. 点击「刷新」或「查询」→ 无任何反应

**Expected Behavior:** 未选账号时不显示统计摘要；按钮禁用或给出提示。

**Actual Behavior:** 显示误导性「共 0 封」；按钮可点击但静默无响应。

**修复:** 未选账号时隐藏摘要行（`!loading && !error && accountId`）+ 刷新/查询按钮 `disabled={!accountId}`。

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r6-03-inbox-no-account-summary.png

---

### Issue #24: 浅色模式凭据 chips 对比度 4.456:1（Low）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Accessibility |
| **URL** | `/accounts` |

**Description:**
本轮新增的凭据 chips 使用 `--color-text-secondary`(#6e6e73)，在 `--color-bg-subtle`(#f0f0f2) 上实测 4.456:1——差 0.044 不到 WCAG AA 4.5:1 门槛（12px 常规文字）。合成背景后精确计算确认（非目测）。

**修复:** 改用 `--color-text-tertiary`(#6a6a6f)，实测 4.726:1 过 AA。

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r6-02-accounts-chips-light.png

---

### Issue #25: 「已清理 0 条日志」提示不友好（Low）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | UX |
| **URL** | `/logs` |

**Description:**
日志清理对话框在无可清理条目时提示「已清理 0 条日志」——读起来像操作失败或数据异常，实际是正常结果。

**修复:** 0 条时提示「没有符合条件的日志，未删除任何条目」。

---

## 第六轮验证矩阵（新功能，全部通过）

| 功能 | 检查项 | 结果 | 证据 |
|------|--------|------|------|
| 多账号默认不选 | 别名页初始不选账号、显示引导 | ✅ | 「请选择账号」+「请先选择账号查看别名」；无账号请求发出 |
| 多账号默认不选 | 收件箱页初始不选账号 | ✅ | 「请先选择账号查看邮件」；inbox 请求数 0 |
| 多账号默认不选 | 任务表单多账号默认不选、保存禁用 | ✅ | 保存按钮 DISABLED；选账号后 ENABLED |
| 多账号默认不选 | 单账号仍自动选中 | ✅ | 单账号场景直接加载 |
| 凭据 chips | 渲染紧凑标签（桌面/移动/暗色） | ✅ | chips 无溢出、无截断；暗色 6.61:1、浅色 4.726:1 |
| 凭据 chips | 长邮箱只进 title | ✅ | `title="fuzy_public@163.com"`，可见文本「收件箱」 |
| 读缓存 | 首屏不带 refresh、命中缓存 | ✅ | 回源 2.9s → 二次 0.001s |
| 读缓存 | 查询/刷新/加载更多带 refresh=1 | ✅ | performance entries 确认 |
| 读缓存 | 删除邮件后缓存失效 | ✅ | 删除后列表回源更新 |
| 连接复用 | 外部邮箱长连接复用 | ✅ | mock 连接计数：2 次读信仅 1 连接 |
| 日志清理 | 三档选择（1天/1周/1月） | ✅ | 对话框三 radio；请求体 older_than_days=7/30 正确 |
| 日志清理 | 成功/失败/取消三态 | ✅ | 成功关闭+toast；失败保留+错误；取消零请求 |
| 日志清理 | 键盘可达性 | ✅ | Tab 循环无遗漏、Escape 关闭、初始焦点正确 |
| 日志清理 | 移动端布局 | ✅ | 390px 对话框三档并排 111px、按钮 40px 高 |
| 全站回归 | 控制台零错误（5 页面） | ✅ | accounts/aliases/alias-tasks/logs/inbox 全空 |
| 全站回归 | 移动端 390px 无溢出 | ✅ | 表格横向滚动为既有设计；按钮 ≥40px |

---

## 第五轮验证记录（2FA 恢复路径，0 新缺陷）

| 检查项 | 结果 | 证据 |
|--------|------|------|
| `push_sent=false` → 警示条 + 2 个恢复按钮 | ✅ | `.alert-warning` 文案「验证码推送失败。可点击『重发验证码』，或改用短信验证。」；`.login-recovery-actions` 含「重发验证码」「改用短信验证」 |
| `push_sent=true` → 正常提示 | ✅ | `.alert-info`「验证码已推送到受信任设备。」；恢复按钮仍常显（未收到时可随时重发） |
| 旧服务端缺 `push_sent` → 不误报 | ✅ | 缺省视为已推送，显示正常提示（兼容旧服务端） |
| 「重发验证码」链路 | ✅ | POST `/login/resend` 携带 `session_id`；成功后警示消失、提示「已重新请求推送验证码到受信任设备。」 |
| 「改用短信验证」链路 | ✅ | GET `/login/phones` → POST `/login/sms`（`phone_id:2`）；提示「已向 +86 138****1234 发送短信验证码。」 |
| 短信提交带通道参数 | ✅ | 提交体 `{session_id, code, method:"sms", phone_id:2}` — 实测拦截确认 |
| 390px 移动端几何 | ✅ | 对话框 x=0 w=390 无溢出；恢复行 scrollWidth=clientWidth=348 无横向滚动 |
| 键盘可达性 | ✅ | 5 个可聚焦元素；Tab 循环 `重发→短信→取消→验证并登录→验证码→回重发` 无遗漏；Escape 关闭 |
| 暗色对比度 | ✅ | 警示条 6.19:1、恢复按钮 5.7:1（合成背景后计算） |
| 浅色对比度 | ✅ | 恢复按钮 4.7:1、警示条 4.51:1 — 全部 ≥4.5:1 |
| 🔵 观察项 | 记录 | 恢复按钮高 25px（链接式 `.text-button`），低于 Apple HIG 44px 触控建议；与项目既有约定一致，不修 |

---

## 第四轮 Issues（本轮新增）

### Issue #18: 暗色模式下 antd 组件保持白色硬编码，与全黑页面严重割裂

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual |
| **URL** | `/inbox`（所有含 antd 组件的页面） |

**Description:**
项目的暗色模式由 CSS `@media (prefers-color-scheme: dark)` 驱动（`styles.css` 中完整的暗色 token 块），但 `App.tsx` 的 antd `ConfigProvider` 主题是**硬编码的浅色值**（`colorBorder: '#c7c7cc'`、`colorText: '#1d1d1f'`，无 `algorithm`）。结果：暗色系统下页面背景是纯黑，而 DatePicker 输入框是纯白（实测 `rgb(255,255,255)`），日期面板也是白底——vision 复核描述为「就像直接贴上去的补丁」。

**Steps to Reproduce:**
1. 系统切换到暗色模式（或 DevTools 模拟 `prefers-color-scheme: dark`）
2. 打开 `/inbox`
3. 观察：页面全黑，但「开始时间/结束时间」输入框和日期面板是白底

**Expected:** antd 组件跟随系统暗色（深色容器、浅色文字）。

**Actual:** antd 组件保持白底黑字，与页面割裂。

**修复：**
1. 新增 `web/src/antdTheme.ts` — `buildAntdTheme(dark)` 在暗色时启用 `antdTheme.darkAlgorithm` 并切换暗色 token（`colorBgContainer: #1d1d1f`、`colorText: #f5f5f7`、`colorBorder: #48484a`，与 `styles.css` 暗色块一致）。
2. 新增 `web/src/usePrefersDark.ts` — 用 `useSyncExternalStore` 订阅 `matchMedia('(prefers-color-scheme: dark)')`（React 官方推荐的外部数据源订阅方式；初版 `useState+useEffect` 被 lint 判定 `setState synchronously within an effect`，已重写）。
3. `App.tsx` 接入：`const dark = usePrefersDark()` → `theme={buildAntdTheme(dark)}`。

**验证：** 暗色下 DatePicker 背景 `rgb(29,29,31)`、文字 `rgb(245,245,247)`、面板背景 `rgb(31,31,31)`；浅色下保持白色不变。测试 `web/src/antdTheme.test.ts`（3 用例）。

---

### Issue #19: 设计系统多处文字对比度低于 WCAG AA 4.5:1

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Accessibility |
| **URL** | 全站（`/accounts`、`/logs`、`/inbox`、`/aliases` 均有命中） |

**Description:**
用 WCAG 相对亮度公式（含 alpha 合成）全站扫描后，发现 5 处系统性对比度不足：

| 元素 | 原值 | 实测对比度 | 修复后 | 修复值对比度 |
|------|------|-----------|--------|-------------|
| `--color-text-tertiary` 表格表头/次要文字 | `#86868b` | 3.18:1 (bg-subtle) | `#6a6a6f` | 4.73:1 |
| `--color-danger` 错误文字 | `#d93026` | 4.17:1 (danger-soft) | `#c22b21` | 5.02:1 |
| 暗色主按钮（白字） | `#2997ff` | 3.02:1 | `#1d6fd6` | 4.89:1 |
| 暗色主按钮 hover（白字） | `#64b5ff` | 2.19:1 | `#1e74dc` | 4.58:1 |
| skip-link（白字） | `#0071e3`/`#2997ff` | 4.70:1 / 3.02:1 | `#1668c4` | 5.51:1 |

**修复：**
- `styles.css`：浅色 token 调整（tertiary、danger）；暗色块新增按钮专用 token `--color-primary-button` / `--color-primary-button-hover` / `--color-danger-button` / `--color-danger-button-hover`（**只影响按钮**，`--color-primary`/`--color-danger` 保持亮色供焦点环/错误文字/图标等前景场景使用）；skip-link 固定深蓝 `--color-skip-link`。
- `antdTheme.ts`：`colorLink` 暗色用 `#64b5ff`（antd 暗色算法默认 link 色在深底上仅 3.18:1）。
- `aliases-ui.css`：`.aliases-primary-btn` 迁移到按钮 token（审查发现的同类遗漏——首轮修复只改了 `button.primary`，别名页自定义主按钮未覆盖，暗色下白字仍 3.02:1）。

**验证：** 浅色 `/inbox` 全页扫描 0 命中；暗色下 `button.primary`/`button.danger`/`.aliases-primary-btn` 实测按钮底色分别为 `rgb(29,111,214)`（4.89:1）、`rgb(194,43,33)`（5.74:1）、`rgb(29,111,214)`，浅色对应 `rgb(0,113,227)`/`rgb(194,43,33)` 不回归。日历相邻月份的灰日期（antd 刻意弱化样式）保留，非缺陷。

**范围说明（审查后修正）：** 首轮扫描仅在 `/inbox` 页面执行，未覆盖 `/aliases` 的自定义主按钮与 `button.danger`——独立审查员发现此遗漏后已一并修复（同类问题应在全仓 grep 后一次修完，见「方法论」）。

---

## 第三轮 Issues（保留）

### Issue #16: 全局 `table { min-width: 920px }` 泄漏进 antd 日期面板，年份/日期网格被撑爆裁切

| Field | Value |
|-------|-------|
| **Severity** | 🔴 Critical（数据选择功能受损） |
| **Category** | Functional / Visual |
| **URL** | `/inbox`（所有含日期选择器的页面） |

**Description:**
antd 的日期面板经 portal 渲染到 `body`，其内部的日历网格与年份/月份网格都用 `<table>` 实现。项目全局的 `table { min-width: 920px }`（本意是给页面数据表格用的）一并命中面板内的 `<table>`：

- **年份视图**：网格被撑到 920px、每列 307px，而面板只有 351px → **第三列（2021/2024/2027/2030）完全不可见**，第二列也被部分裁切。用户截图里"年份缺失"即此问题。
- **日期视图**：日历网格被撑到 920px、每列 131px，与右侧时间列（x=1022）重叠。
- 同类泄漏还有：全局 `th/td` 的 padding/border 给日历格子加了框线、表头加了灰底；全局 `tbody tr` 的入场动画作用到日历行；全局 `button` 的 `min-height: 40px` 把面板头部箭头按钮撑到 50px 高。

**Steps to Reproduce:**
1. 打开 `/inbox`，点击「开始时间」
2. 点击面板头部的年份（如「2026年」）进入年份视图
3. 观察：年份网格 3 列只显示约 2 列，右侧列被裁

**Expected:** 年份/日期网格完整显示在面板内。

**Actual:** 年份网格宽 920px（面板 351px），第三列在屏幕外；日期网格宽 920px（面板 492px），与时间列重叠。

**根因定位（CDP `CSS.getMatchedStylesForNode`）：**
```
.ant-picker-date-panel .ant-picker-content
  [user-agent] table -> {'border-collapse': 'separate'}
  [regular]    table -> {'border-collapse': 'collapse', 'width': '100%', 'min-width': '920px'}   ← 泄漏源
  [regular]    :where(.css-oc1rc0)... -> {'width': '100%', 'border-collapse': 'collapse'}
```
`[regular]` 来源即 `web/src/styles.css:595` 的全局 `table` 规则。

**修复：** 在 `styles.css` 新增 **antd 面板隔离层**（`.ant-picker-dropdown` 作用域内复位泄漏属性）：

```css
.ant-picker-dropdown table { min-width: 0; }
.ant-picker-dropdown th, .ant-picker-dropdown td { padding: 0; border-bottom: 0; background: transparent; ... }
.ant-picker-dropdown tbody tr { animation: none; transition: none; will-change: auto; }
.ant-picker-dropdown button { min-height: auto; }
```

**验证（4 视口 × 3 视图矩阵）：**

| 视口 | 日期视图 | 年份视图 | 月份视图 |
|------|---------|---------|---------|
| 1440×900 | ✅ 0 溢出 | ✅ 12 格全在 | ✅ 12 格全在 |
| 768×1024 | ✅ 0 溢出 | ✅ | ✅ |
| 513×702 | ✅ | ✅ | ✅ |
| 390×844 | ✅ | ✅ | ✅ |

**回归验证（隔离层不得影响页面表格）：** `/logs`、`/accounts` 表格的 `min-width: 920px`、`th` padding/bg、`row-enter` 动画全部保留。

**测试：** `web/src/test/antd-isolation.test.ts`（5 个守卫测试，断言隔离规则存在 + 全局规则保留）。**变异验证**：删除 `min-width: 0` → 测试失败；恢复 → 通过。

---

### Issue #17: 窄屏 bottom-sheet 下年份/月份面板左对齐，右侧空 154px

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Visual |
| **URL** | `/inbox` |

**Description:**
≤560px 时面板改为 bottom-sheet（撑满视口宽 497px），但年份/月份面板保持 antd 固定宽 351px 并左对齐，右侧空出 154px；与下方日期视图（撑满）不一致。

**修复：** 窄屏媒体查询内将 `.ant-picker-year-panel` / `.ant-picker-month-panel` 及其 body/content 撑满整宽（与日期视图一致，格子触控区域也更大）。

**验证：** 513px 下左/右 gap 从 8/154 变为 8/8；390px 从 8/31 变为 8/8。

---

## 第三轮方法论记录

**用户截图时间戳分析：** 用户截图 `upload_20261006_064708_3.png`（06:47）早于修复构建（07:11），截图中的年份裁切正是 Issue #16。修复后按用户截图同尺寸（513×702）复现验证：2021/2024/2027 全部回归、网格左右对称。

**驳回的 vision 误报（本轮 3 条）：**
1. 声称"日历与时间列重叠" → DOM 实测日期表右缘 1004 < 时间列左缘 1022，间隙 18px；放大图复核无重叠
2. 声称"年份 2028-2030 缺失" → DOM 实测 12 格全在（当时是 577px 低视口，格子在视口外但面板内）
3. 声称"确定按钮灰色禁用" → DOM 实测 `disabled: false, bg: rgb(22,119,255)`（蓝色可用）；用户截图中确为禁用态，因为**输入框被清空**（无值可确认）——antd 标准行为

**验证的标准交互行为（非缺陷）：**
- 输入框灰色文字 = antd hover 预览（`rgba(0,0,0,0.25)`）：鼠标悬停年份格子时输入框显示预览值。实测 hover 2022 → 输入框显示 "2022-09-29 00:00"（灰）；hover 2025 → 跟随变化。

---

## 第二轮 Issues（保留）

### Issue #8: 凭据列窄至 60px 时「未配置」被逐字拆成 3 行，整行撑到 92px

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual |
| **URL** | `/accounts` |

**Description:**
注入长名称/长邮箱的测试账号后，表格自动布局把「凭据」列压缩到 60px（可用内容宽仅 28px），「未配置」三个字被逐字折成 3 行（每行 1 个字，y=255/277/300），把整行高度从 66px 撑到 92px。同一行的其他单元格内容最高仅 33px，产生大量垂直空白。

**Steps to Reproduce:**
1. 账号页存在长名称/长邮箱的账号（真实场景常见）
2. 观察「凭据」列与整行高度

**Expected:** 短标签单行显示，行高由内容自然决定。

**Actual:** 凭据列逐字折行 3 行，行高 92px。

**根因定位过程（逐单元格消隐法）：** 隐藏单个单元格的子元素后测行高，发现凭据列贡献 0px 但其他列贡献 23-35px——矛盾。最终通过 vision 报告「未配置被拆成三行」+ DOM `Range.getClientRects()` 确认 3 个文字矩形（y=255/277/300）。

**修复：** 凭据列与最近验证列加 `cell-nowrap`（`white-space: nowrap`），列宽由表格布局重新分配（60→74px）。实测行高 92px → 66px（内容自然高度）。

**测试：** `AccountsPage.table.test.tsx`「短标签列(凭据/最近验证)禁止逐字换行」。

---

### Issue #9: 长名称/邮箱折成 3-4 行撑高行（88-92px）

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual |
| **URL** | `/accounts` |

**Description:**
长账号名（266px 自然宽，列宽 207px）折 2 行、长邮箱（298px 自然宽，列宽 263px）折 3 行，行高被撑到 88px，破坏表格可扫读性。

**修复：** 名称/邮箱单元格加 `cell-truncate`（单行截断 + 省略号 + `title` 完整值提示），对齐参考项目 Register 的 antd `ellipsis={{tooltip}}` 做法。上限经实测选定 **200px**：220px+ 会把两列最小宽度推到超出容器，在 1440px 桌面引入新的横向滚动条（实测 200px 时表格 1094px = 容器宽，无滚动）。

**测试：** `AccountsPage.table.test.tsx`「名称与邮箱单行截断并带 title 提示」。

---

### Issue #10: 「最近验证」显示原始 ISO 串 `2026-10-05T09:00:00+08:00`

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | UX / Consistency |
| **URL** | `/accounts` |

**Description:**
其他页面（别名/收件箱）均用 `formatDateTime` 输出「2026/10/05 01:00」，账号页直接渲染服务端原始 ISO 串，既冗长又与其他页面不一致。

**修复：** 新增 `formatValidated()`（复用 `utils/datetime` 的 `formatDateTime`，空值显示「—」，解析失败回退原文）。

**测试：** `AccountsPage.table.test.tsx`「最近验证时间格式化为可读形式, 不显示原始 ISO 串」。

---

### Issue #11: 日志页时间用浏览器默认 `toLocaleString`，输出美式格式

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | UX / Consistency |
| **URL** | `/logs` |

**Description:**
`new Date(log.time).toLocaleString()` 在无 locale 参数时输出 **`9/12/2026, 6:00:00 AM`**（美式 12 小时制），与项目统一的中文 24 小时制「2026/09/12 06:00」不一致。日志详情对话框同样问题。

**修复：** 新增 `formatLogTime()` 复用 `formatDateTime`（列表 + 详情两处）。

**测试：** `LogsPage.test.tsx`「日志时间用项目统一的中文格式」——断言 `/2026\/09\/12 06:00/` 存在且无 `AM|PM`。

---

### Issue #12: 别名页工具栏在 390px 溢出屏幕 243px

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Visual |
| **URL** | `/aliases` |

**Description:**
`.aliases-toolbar` 是 `flex-wrap: nowrap`，4 个控件（账号选择/自动创建任务/创建别名/状态筛选）在 390px 总宽 **633px**：状态筛选右缘 633、创建别名按钮右缘 515，均远超 390px 视口，页面出现横向滚动（`documentElement.scrollWidth` 633 > 390）。

**修复：** ≤640px 断点改为 `flex-wrap: wrap` + 每行两个等宽控件（`flex: 1 1 calc(50% - var(--space-2))`），并为 ghost 按钮补齐样式。

**验证：** 390px 下 `scrollWidth 390 = clientWidth 390`，无溢出元素。

---

### Issue #13: 任务页「下次执行」同样用 `toLocaleString`

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Consistency |
| **URL** | `/alias-tasks` |

**Description:** `AliasTasksPage` 的「下次执行：」时间同为美式格式。

**修复：** 新增 `formatNextRun()` 复用 `formatDateTime`。

---

### Issue #14: 别名页错误态「账号未配置 Cookie」横幅与重试按钮

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Functional |
| **URL** | `/aliases` |

**Description:** 注入的测试账号无 Cookie，页面正确显示错误态 + 重试按钮——**验证了错误处理路径正常**，非缺陷。记录以说明该状态已被覆盖。

---

### Issue #15: 移动端筛选栏「账号」下拉文本截断

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Visual |
| **URL** | `/inbox` |

**Description:** 390px 下账号下拉显示「测试主号（很...」。**判定：非缺陷**——移动端宽度约束下的正常截断行为，控件有 `title` 提示完整值，与设计系统其他 select 行为一致。

---

## 第二轮验证矩阵（全部通过）

| 视口 | /accounts | /aliases | /alias-tasks | /logs | /inbox |
|------|-----------|----------|--------------|-------|--------|
| 1440×900 | ✅ | ✅ | ✅ | ✅ | ✅ |
| 768×1024 | ✅ | ✅ | ✅ | ✅ | ✅ |
| 390×844 | ✅ | ✅ | ✅ | ✅ | ✅ |

（断言：`documentElement.scrollWidth === clientWidth`、无 fixed 定位以外的元素右缘超出视口；表格容器内部横向滚动属预期设计。）

**其他验证：** 添加账号对话框（桌面 480×446 居中 / 移动 390 宽全适配、均完整在视口内）、收件箱错误横幅（390px 下 x=16 宽 358 完整可见）、移动端 5 项导航全可见。

---

## 第一轮 Issues（保留）

### Issue #1: 窄屏（390px）日期时间面板整体溢出屏幕，日期格子与时间列被裁

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual / Functional |
| **URL** | `/inbox` |

**Description:**
antd 日期时间面板固定宽 492px（日历 280px + 时间列），且 `.ant-picker-content` 带 `min-width: 920px` 硬约束。在 390px 视口下面板左侧溢出 135px，右侧日期格子（x=277、x=409）完全在屏幕外；时间列被顶到 x=965，用户无法选择日期与时间。面板高度 695px 也超出视口，底部「确定」按钮被裁掉。

**Steps to Reproduce:**
1. 视口设为 390×844（iPhone 12/13/14 尺寸）
2. 打开 `/inbox`，点击「开始时间」输入框
3. 面板弹出

**Expected Behavior:**
面板完整显示在视口内，日历与时间列均可用。

**Actual Behavior:**
面板 x=-135、宽 492px；42 个日期格子中 30 个在屏幕外；时间列 x=965（远超 390px 视口）。

**Screenshot:**
MEDIA:/home/icloud-hme/dogfood-output/screenshots/04-mobile-panel-overflow.png

**修复：** 窄屏改为底部固定定位（bottom-sheet 模式）：`position: fixed; inset: auto 8px 8px 8px`，纵向堆叠（`.ant-picker-datetime-panel { flex-direction: column }`），并解除 `min-width` 约束。修复后：面板 x=8、右缘 382、底部 836（全部在 390×844 内），时间列完整可见，「此刻/确定」均可见。

**修复验证：**
MEDIA:/home/icloud-hme/dogfood-output/screenshots/08-mobile-bottom-sheet.png

---

### Issue #2: antd 组件与设计系统全面不一致（高度/圆角/边框色/文字色）

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual |
| **URL** | `/inbox` |

**Description:**
日期时间选择器带着 antd 默认样式与相邻的 SelectMenu 并排，形成明显错位：

| 属性 | DatePicker（修复前） | 设计系统（SelectMenu/按钮） |
|---|---|---|
| 高度 | 50px | 40px |
| 圆角 | 6px | 11px |
| 边框色 | `rgb(217,217,217)` | `rgb(199,199,204)` |
| 文字色 | `rgba(0,0,0,0.88)` | `rgb(29,29,31)` |

**根因（两层）：**
1. antd 未接入项目设计 token（`ConfigProvider` 无 theme）
2. 项目的全局 `input { min-height: 40px; padding; border }` 规则把 antd 面板内部的裸 `<input>` 撑到 40px + 8px×2 padding = 58px，反而比 antd 默认更高

**Steps to Reproduce:**
1. 打开 `/inbox`
2. 对比「账号」下拉框与「开始时间」选择器的高度与圆角

**Expected Behavior:**
同一筛选栏内所有控件视觉一致（40px 高、11px 圆角、同色边框文字）。

**Actual Behavior:**
两个日期选择器比其他控件高 10px、圆角小 5px，垂直中心偏移 5px，标签错位 10px。

**Screenshot:**
MEDIA:/home/icloud-hme/dogfood-output/screenshots/01-inbox-filter-misalign.png

**修复：**
1. `App.tsx` 的 `ConfigProvider` 挂载 `theme`，把设计 token 映射为 antd token（`controlHeight: 40`、`borderRadius: 11`、`colorBorder: #c7c7cc`、`colorText: #1d1d1f`、`fontFamily`）
2. `styles.css` 为 `.ant-picker input` 增加豁免规则，还原为无样式裸元素（宽度/内边距/边框/背景全部复位）

**修复验证（DOM 测量）：** 高度 40/40、圆角 11/11、边框色一致、文字色一致、控件垂直中心 spread=0、标签 top spread=0。

MEDIA:/home/icloud-hme/dogfood-output/screenshots/02-inbox-filter-aligned.png

---

### Issue #3: 移动端导航溢出——390px 下当前页「收件箱」在屏幕外

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Visual / UX |
| **URL** | 全站 header |

**Description:**
`nav` 在 ≤760px 是横向滚动容器（`overflow-x: auto`），但 390px 下 5 个菜单项总宽 427px，超出 39px。第 5 项「收件箱」右缘 415px > 390px，被挤出视野；而滚动条被 `scrollbar-width: none` 隐藏，用户没有任何提示可以横向滚动——恰好当前页（收件箱）就是不可见的那一项。

**Steps to Reproduce:**
1. 视口 390px，访问 `/inbox`
2. 观察顶部导航

**Expected Behavior:**
当前页菜单项应可见，或至少提供可见的滚动提示。

**Actual Behavior:**
「收件箱」位于 x=326~415，超出视口右缘 25px，完全不可见。

**Screenshot:**
MEDIA:/home/icloud-hme/dogfood-output/screenshots/03-mobile-nav-overflow.png

**修复：**
1. `AppShell.tsx` 增加 `useEffect`：路由切换后把 `a[aria-current="page"]` 滚入视野（`scrollIntoView({ block: 'nearest', inline: 'center' })`）
2. `styles.css` 增加 ≤420px 断点：收紧导航项内边距与字号，5 项完整放下（实测 navW 390 = scrollW 390，无需滚动）

---

### Issue #4: 移动端日期面板打开后底部「确定」按钮在视口外

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Functional / UX |
| **URL** | `/inbox` |

**Description:**
纵向堆叠后（Issue #1 修复过程）面板总高 695px，从输入框下方展开（y=427），底部 1122px 远超 844px 视口。`max-height` 已生效但 `overflow-y: auto` 的滚动容器让用户需要手动滚动才能触达「确定」——不是可接受的默认状态。

**Steps to Reproduce:**
1. 390px 视口打开日期面板
2. 观察底部按钮

**Expected Behavior:**
「此刻/确定」无需滚动即可见。

**Actual Behavior:**
面板 bottom=1122，按钮在视口外。

**修复：**
随 Issue #1 一并解决——bottom-sheet 定位让面板底边固定在 `视口底部 - 8px`，实测 bottom=836 ≤ 844，按钮可见。

---

### Issue #5: 「查询」按钮底部与输入框基线错位 1-2px（vision 报告，测量驳回）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Visual |
| **URL** | `/inbox` |

**Description:**
vision 模型报告「查询按钮高度略低于输入框，底部没有和输入框基线对齐」。DOM 实测：按钮与全部控件同为 40px、top/bottom 完全一致（233/273）、垂直中心 spread=0。**判定为 vision 幻觉，非缺陷。**

**处理：** 无需修复。记录以说明本次 QA 的证据标准。

---

### Issue #6: 登录页/各页「退出登录」按钮高度 37px 与设计系统 40px 不一致

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Visual |
| **URL** | 全站 header |

**Description:**
`shell-logout` 实测高 37px，与设计系统的 40px 控件标准不一致（其余按钮均 40px）。

**Steps to Reproduce:**
1. 任意页面测量 `.shell-logout` 高度

**Expected Behavior:**
40px（与其他控件统一）。

**Actual Behavior:**
37px。

**判定（复核后）：** 非缺陷。设计系统的 40px 标准适用于**表单控件**；`shell-logout` 是 header 中的文字按钮，与相邻的 nav 链接（min-height 36px、无边框）同属导航区控件。37px 与 nav 项视觉一致（1px 差异不可见），刻意对齐到 40px 反而会与同区域的 nav 项产生新的不一致。**不改。**

---

### Issue #7: 日期面板在 577px 高视窗下溢出（测试环境视窗过小）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low |
| **Category** | Visual |
| **URL** | `/inbox` |

**Description:**
默认浏览器测试视窗为 1280×577 时，面板（420px 高）从 y=276 展开到 y=686，超出 577px 视口 109px。在常规 1440×900 视窗下无此问题（bottom=686 ≤ 900）。

**判定：** 视窗高度 <700px 时 antd 会自动向上翻转面板（`placement` 属性），本次未复现翻转是因为测试视窗设置；属于边界环境而非缺陷。**修复：** 随 Issue #1 的 bottom-sheet 模式，窄屏（≤560px）不再受此影响。

---

## Issues Summary Table

| # | Title | Severity | Category | URL | 状态 |
|---|-------|----------|----------|-----|------|
| 1 | 窄屏日期面板整体溢出屏幕 | 🟠 High | Visual/Functional | /inbox | ✅ 已修复 |
| 2 | antd 与设计系统全面不一致 | 🟠 High | Visual | /inbox | ✅ 已修复 |
| 3 | 移动端导航当前页不可见 | 🟡 Medium | Visual/UX | 全站 | ✅ 已修复 |
| 4 | 移动端面板底部按钮视口外 | 🟡 Medium | Functional | /inbox | ✅ 已修复 |
| 5 | 查询按钮基线错位（vision 幻觉） | 🔵 Low | — | /inbox | ⛔ 非缺陷 |
| 6 | 退出登录按钮 37px | 🔵 Low | Visual | 全站 | ⛔ 复核后非缺陷 |
| 7 | 低视窗下日期面板溢出 | 🔵 Low | Visual | /inbox | ✅ 随 #1 解决 |
| 8–15 | 第二轮：表格渲染/ISO 时间/`toLocaleString`/工具栏溢出等 | 🟠🟡🔵 | 多类 | 多页 | ✅ 7 修复 + 1 驳回 |
| 16–17 | 第三轮：`table min-width` 泄漏进 antd 面板 / bottom-sheet 对齐 | 🔴🟡 | Visual | /inbox | ✅ 已修复 |
| 18–19 | 第四轮：暗色 antd 硬编码白底 / 对比度 token 低于 AA | 🟠🟡 | Visual/A11y | 全站 | ✅ 已修复 |
| 20 | 第五轮：恢复按钮触控目标 25px（观察项） | 🔵 | — | /login | ⛔ 记录不修 |
| 21 | 按别名筛选无命中 → `messages:null` → 整页白屏 | 🔴 Critical | Functional | /inbox | ✅ 已修复 |
| 22 | 163/126 邮箱「按别名筛选」永远 0 结果（SEARCH 静默失效） | 🔴 Critical | Functional | /inbox | ✅ 已修复 |
| 23 | 未选账号时收件箱摘要与按钮误导 | 🟡 Medium | UX | /inbox | ✅ 已修复 |
| 24 | 浅色凭据 chips 对比度 4.456:1（差 0.05 不到 AA） | 🔵 Low | Accessibility | /accounts | ✅ 已修复 |
| 25 | 「已清理 0 条日志」提示不友好 | 🔵 Low | UX | /logs | ✅ 已修复 |

## Testing Coverage

### Pages Tested
- `/login`（登录表单、错误密码、成功跳转）
- `/accounts`（账号管理）
- `/aliases`（别名管理）
- `/alias-tasks`（自动创建任务）
- `/logs`（任务日志）
- `/inbox`（收件箱：筛选栏、日期面板、空状态、刷新）

### Features Tested
- 登录/登出流程（含整页导航后会话保持）
- 收件箱筛选栏：账号/别名/每页下拉、开始/结束时间选择器、查询按钮
- 日期时间面板：打开、选日期、点确定、值回填、清空
- URL 参数水合（`?start=...&end=...` 刷新后复现筛选）
- 响应式：1440px / 768px / 390px 三档
- 面板交互：日期格子点击、时间列、此刻/确定按钮

### Not Tested / Out of Scope
- 真实 iCloud 账号操作（无账号数据，仅空状态）
- 自动任务实际创建流程（需真实账号与 20 分钟冷却）
- 邮件详情/删除（需真实邮件）
- 跨浏览器（仅 Chromium）
- 键盘可访问性完整走查（Tab 顺序、焦点陷阱）

### Blockers
- 无账号数据：所有列表页为空状态，无法验证有数据时的表格布局/分页/长文本截断
- 无真实 iCloud 凭据：无法测试登录对话框、邮件读取、别名创建

---

## Notes

### 方法论：measure, don't eyeball

本次 QA 中 vision 模型给出了 **4 次与 DOM 测量直接矛盾** 的报告：
1. 声称「账号/别名下拉框比日期选择器矮 1-2px」→ 实测全部 40px、中心 spread=0
2. 声称「标签没有统一对齐基准」→ 实测 5 个标签 top 完全一致（spread=0）
3. 声称「面板底部没有此刻/确定按钮」→ DOM 中按钮存在且坐标有效
4. 声称「日期面板不是标准 Ant Design 布局」→ 实际就是 antd v6 的标准结构

**结论：** 所有 UI 缺陷结论必须以 `getBoundingClientRect()` / `getComputedStyle()` / CDP `CSS.getMatchedStylesForNode` 为准。vision 仅用于「有无明显异常」的初筛，不用于定量判断。

### 修复涉及的根因（供后续参考）

1. **CSP `style-src 'self'`** 曾拦截 antd v6 的 CSS-in-JS 运行时注入（14 个 `<style>` 标签全部未解析）——已在前一轮修复，本次 QA 确认生效（14/14 已解析）
2. **全局 `input` 规则与 antd 冲突**：项目的 `input { min-height: 40px; padding; border }` 会污染 antd 组件内部的裸 input
3. **antd 面板固定宽度**：`.ant-picker-content { min-width: 920px }` 是硬约束，窄屏必须显式解除
4. **antd 分栏容器类名**：`.ant-picker-datetime-panel`（不是 `.ant-picker-panel-layout`）——选错选择器会静默失效

### 建议的后续项
- 有真实账号数据后，建议复测表格页在有数据时的横向溢出（dogfood 技能 checklist #14：宽表 sticky 操作列）
- 建议补一个移动端 UI 探针脚本进 CI（390px 断言：nav 无溢出、面板完整在视口内）

---

## 第七轮（API 秘钥功能 + 全站菜单/风格/响应式，1 项修复）

**范围：** API 秘钥页（新功能）+ 全站 6 页面 × 6 视口响应式矩阵 + UI 风格一致性。
**目标实例：** 本地一次性环境 `http://127.0.0.1:18200`（QA 数据目录已清理）。
**前置功能：** 秘钥功能相关 25 项历史问题不受影响；本轮为秘钥功能上线前的验收 QA。

### 交付项验收

| 用户要求 | 实测结果 | 证据 |
|---|---|---|
| 菜单顺序 账号→别名→收件箱→自动任务→秘钥→日志 | ✅ DOM `nav a` href 序列完全一致 | 单元测试 + 浏览器实测 |
| UI 风格一致化 | ✅ 5 处不一致已消除（见下） | 与 AccountsPage/LogsPage 逐项对齐 |
| 响应式（手机端） | ✅ 36 组合零横向溢出；触控目标修复 | 6 页面 × 6 视口矩阵 |

### Issue #26: 秘钥行内操作按钮移动端触控目标仅 28×40px（Medium，已修复）

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Accessibility / Responsive |
| **URL** | `/keys` |

**Description:**
秘钥行内操作按钮（显示/复制/编辑/停用/删除）在 390px 视口下实测 28×40px，低于 iOS HIG 44×44、Android 48×48 触控规范；项目既有移动端约定为 `min-height: 44px`（`aliases-ui.css:123,125`），本页未遵循。

**Steps to Reproduce:**
1. 视口设 390×844，打开 `/keys`（存在 ≥1 条秘钥）
2. 测量 `tbody .key-row-actions button` 尺寸 → 28×40

**Expected Behavior:** 移动端触控目标 ≥44px（对齐项目约定）。

**Actual Behavior:** 宽 28px 高 40px，手指易误触。

**修复:**
- `web/src/keys-ui.css`：新增 `.key-row-actions .link-button` / `.key-cell .link-button` 桌面 `min-height: 40px`；`@media (max-width: 640px)` 提升至 `min-height: 44px` + `padding-inline: 8px`。
- 同步新增窄屏 `.key-cell` 纵向堆叠，避免脱敏值 + 按钮挤成一行。

**验证:**
- 修复后 390px 实测按钮 44×44px；桌面端 @1280 保持 40px（未回归）。
- 服务端交付字节确认：`index-CyVSMlWy.css` 同时含 `min-height:40px` 与 `min-height:44px` 规则。

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r7-keys-mobile-touch.png

---

### UI 风格一致化（本轮修正清单）

| 项 | 修正前 | 修正后 | 依据 |
|---|---|---|---|
| 页面主操作按钮 | 自定义 `.keys-create-btn`（自绘背景/hover） | `className="primary"` | 与 AccountsPage 一致 |
| 危险操作色 | `--danger`（未定义变量 → 回退） | `--color-danger`（设计令牌） | `styles.css` token |
| 复选框 | 已用 `accent-color` | `accent-color: var(--color-primary)` | 与 aliases-ui 一致 |
| 行内操作按钮组 | `.row-actions`（与全局胶囊样式同名语义冲突） | `.key-row-actions`（独立语义） | 消除类名语义冲突 |
| 页面结构 | `page-header`/`page-title`/`h2`/副标题 | 与 LogsPage/AccountsPage 完全一致 | 逐行对齐 |

### 响应式矩阵（36 组合全部通过）

| 视口 | 账号 | 别名 | 收件箱 | 自动任务 | 秘钥 | 日志 |
|---|---|---|---|---|---|---|
| 320px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 390px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 414px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 768px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 1280px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| 1920px | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

（断言：`documentElement.scrollWidth <= innerWidth + 1`；表格容器内部横向滚动属既有设计。）

**控制台错误：** 全页面 × 全断点 **0 条**。

### 本轮观察项（未修复，均预存，非本功能引入）

| # | 标题 | 严重度 | 说明 |
|---|------|--------|------|
| O1 | `/favicon.ico` 返回 404 | 🔵 Low → ✅ **已修复** | `web/public/` 无 favicon、`index.html` 无 `<link rel="icon">`；`git log -S favicon -- web/` 无记录（从未存在）。**后续提交已补齐**：新增 `favicon.svg` + `favicon.ico`（32×32 PNG-in-ICO），`index.html` 加 `<link rel="icon">`，新增 `TestFaviconServed` 防回归（webui 层 200 + Content-Type 断言）；实测 `/favicon.ico` → 200 `image/x-icon`。 |
| O2 | 移动端导航横向滚动（6 项 423px > 390px） | 🔵 Low | 既有设计（`AppShell` 注释明确），可滚动且激活项自动居中；仅溢出 33px。 |
| O3 | 窗口尺寸证明：秘钥表格 @390px 宽 920px | ✅ 一致 | 与 `/logs` 实测同宽（920px），属既有表格模式，非回归。 |

### 第七轮测试说明与边界

**已覆盖：** 6 页面 × 6 视口布局/溢出/触控目标/控制台错误；秘钥页全交互（创建/列表/脱敏/显示/复制/编辑权限/删除/移动端表格滚动/移动端对话框内部滚动到提交按钮）。

**未覆盖（明确说明）：**
- **登录键盘路径：** 到达已登录页面通过 QA 实例自身会话 Cookie 注入（该实例与数据均为本次启动的一次性环境）；用户拒绝了 vault 保存登录提示，未以真实键盘输入路径验证登录表单提交。
- **物理设备：** 仅 CDP 视口模拟，未在真机验证触摸手势/滚动惯性。
- **数据密集态：** 账号/别名等页面在空数据集下检测；大行数渲染性能未测。

### 第七轮结论

- 三项用户要求（菜单顺序、风格一致化、响应式）**全部达成**。
- 新发现 1 项移动端触控问题（#26），**已修复并验证**；桌面端无回归。
- 3 项预存观察项已记录，均非本次引入，不阻塞。
- 门禁：`gofmt` 空 / `vet` OK / `go test ./...` 全 ok / `-race`（apikey 1.61s / server 57.5s）/ 前端 **30 文件 170 测试全过**。

---

## 第八轮（UI 优化检测 + 风格一致性 + UI 性能优化，5 项全部修复）

**范围：** 用户要求「完成 UI 优化检测，保证 UI 风格一致性。优化 UI 性能。」——6 页面 × 桌面/移动/深色矩阵全量检测 + 资源管线审计 + 性能优化落地。
**目标实例：** 本地环境 `http://127.0.0.1:8081`（用户提供的 QA 凭据 `local-test-2026`）。

### 性能优化（用户要求「优化 UI 性能」）——三项落地

| 优化项 | 优化前 | 优化后 | 收益 |
|---|---|---|---|
| **静态资源 gzip**（新增） | JS 674,040 / CSS 60,859 字节（原样传输） | JS 218,722 / CSS 11,081 字节 | **-68% / -82%** |
| **路由级代码分割**（新增） | 单 bundle 674KB 全量加载 | 入口 chunk 15KB；`/inbox` 重依赖按需加载 | 入口 **-98%**；首屏 gzip 229.8KB → **147KB（-36%）** |
| **未定义 token 修复**（见 Issue #27） | 4 个 `var()` 引用被静默丢弃 | 全部换用既有 token | 消除无效规则 + 渲染缺陷 |

**实测数据（服务端字节，curl -H 'Accept-Encoding: gzip'）：**
```
JS:  674,040 → 218,722 字节（-68%）  解压 MD5 与源文件一致
CSS:  60,859 →  11,081 字节（-82%）
入口 chunk: 674,040 → 15,109 字节（-98%）
首屏关键路径(gzip): 229,803 → 147,042 字节（-36%）
非收件箱页面另省 InboxPage chunk 76KB(gzip)
```

### Issue #27: 未定义 CSS token 导致清理任务卡背景与圆角静默丢失（High，已修复）

| Field | Value |
|-------|-------|
| **Severity** | 🟠 High |
| **Category** | Visual / Functional |
| **URL** | `/logs`（`.purge-task-card`、`.logs-filter select`、`.purge-progress`） |

**Description:**
`styles.css` 引用了 4 个从未定义的 CSS 变量（`--color-surface`、`--radius-md`、`--color-surface-secondary`、`--dur-med`）。浏览器对无回退值的未定义 `var()` 会**丢弃整条声明**——`.purge-task-card` 实测计算样式为 `background: rgba(0,0,0,0)`（透明）、`border-radius: 0px`。布局探针与截图都发现不了（布局正常、文字可读）。skill 探针 #32 的教科书案例。

**Steps to Reproduce:**
1. 打开 `/logs`
2. `getComputedStyle(document.querySelector('.purge-task-card')).backgroundColor` → `rgba(0, 0, 0, 0)`
3. `borderRadius` → `0px`；对照 `styles.css` 全文件无 `--color-surface:` / `--radius-md:` 定义

**Expected:** 卡片白底（`--color-bg-raised`）+ 14px 圆角（`--radius-xl`），与 `.table-wrap`/`.task-card` 卡片体系一致。

**Actual:** 透明背景、零圆角；进度条轨道同样退化（`--color-surface-secondary`）、过渡时长退化（`--dur-med`）。

**修复：**
- `--color-surface` → `--color-bg-raised`（2 处）；`--radius-md` → `--radius-xl`；`--color-surface-secondary` → `--color-border`（与 `.task-progress` 轨道一致）；`--dur-med` → `0.7s var(--ease-apple)`（与 `.task-progress-fill` 一致）
- **防回归测试** `web/src/styles.tokens.test.ts`：扫描全部样式表，任何 `var(--token)` 引用必须有定义（RED 列出 4 个孤立 token → GREEN）
- **变异反证：** 重新引入未定义 token → 测试变红 ✅

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r8-02-undefined-token-card.png

### Issue #28: 移动端导航「日志」被裁切——431–505px 边界带无规则覆盖（Medium，已修复）

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Visual / UX |
| **URL** | 全站 header（≤505px 视口） |

**Description:**
导航 6 项总宽 423px（图标+文字）在 390px 视口溢出 33px：「日志」右缘 411px 完全不可见。旧 `≤420px` 规则按 5 项校准、6 项下失效；**431–505px 区间无任何规则覆盖**——480px 视口实测 `nav.scrollWidth=505 > clientWidth=480`，同样溢出。滚动条被隐藏，用户无任何提示。

**Steps to Reproduce:**
1. 视口 390px（或 431–505px 任一宽度），打开任意页面
2. 实测 `nav.scrollWidth=423`（480px 下 505）、`clientWidth=390`、「日志」右缘 411px

**修复：**
- `≤520px`：隐藏导航图标（沿用 ≤940px 既有处理）+ 内边距 10px/字号 13px → 6 项总宽 337px
- `≤340px`：进一步收紧至 7px/12px
- **13 宽度矩阵实测（320→1024px）：全部 `scrollWidth == clientWidth` 且最后一项可见**

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r8-06-mobile-nav-bug-state.png

### Issue #29: 主按钮字重不统一（400 vs 500）（Medium，已修复）

| Field | Value |
|-------|-------|
| **Severity** | 🟡 Medium |
| **Category** | Visual（设计系统一致性） |
| **URL** | 全站主按钮 |

**Description:**
`button.primary` 字重 400，而 `.aliases-primary-btn` / `.inbox-action.primary` 为 500——同级别主操作按钮（同底色、同尺寸）字重不一致。逐页实测：`/accounts`「添加账号」、`/alias-tasks`「新建任务」、`/keys`「创建秘钥」= 400 vs `/aliases`「创建别名」、`/inbox`「查询」= 500。

**修复：** `button.primary` 增加 `font-weight: 500`；修复后 5 页实测全部 500 ✅

**证据:** MEDIA:/home/icloud-hme/dogfood-output/screenshots/r8-04-desktop-final.png

### Issue #30: 静态资源完全未压缩（Low→已优化）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low（性能优化项） |
| **Category** | Performance |
| **URL** | `/assets/*.js`、`/assets/*.css` |

**Description:** `webui.Handler` 用 `io.Copy` 直出，无压缩协商；`curl -H 'Accept-Encoding: gzip'` 返回 674,040 字节（与未压缩一致）。

**修复：**
- `webui.Handler` 增加 gzip 协商：`compressibleType`（JS/CSS/HTML/SVG/JSON）+ `acceptsGzip`（含 `q=0` 拒绝语义）+ 1KB 阈值
- 压缩结果按文件名缓存（嵌入资源不可变 → 一次压缩多次复用）
- 新增 3 测试：gzip 协商 / 未压缩回退 / HEAD 与 GET 头部一致；**变异反证：关闭 gzip 分支 → 测试变红 ✅**
- 实测：解压 MD5 与源文件一致；`Vary: Accept-Encoding` 正确

### Issue #31: 单 bundle 674KB 全量加载（Low→已优化）

| Field | Value |
|-------|-------|
| **Severity** | 🔵 Low（性能优化项） |
| **Category** | Performance |
| **URL** | 全站首屏 |

**Description:** `App.tsx` 静态导入全部 6 页 → 单一 674KB bundle；访问任何页都要下载全部代码（含 `/inbox` 独占的 antd DatePicker + dayjs）。

**修复：** 路由级 `React.lazy` + `Suspense`（fallback 复用既有 `empty-state` 加载态）。
**实测：** 入口 674KB → 15KB；`/accounts` 首屏 gzip 229.8KB → 147KB；导航到 `/inbox` 时才拉取 `InboxPage` chunk（76KB gzip）；6 页面渲染正常、零控制台错误、188 测试全过。

### 第八轮验证矩阵

| 项目 | 结果 |
|---|---|
| 桌面 1280 全 6 页 | ✅ 零溢出、零控制台错误 |
| 移动 390 全 6 页 | ✅ 零溢出（`scrollWidth ≤ innerWidth`） |
| 深色模式（accounts/logs） | ✅ 黑底白字、卡片 `#1d1d1f`、表头对比度 4.70:1（WCAG AA） |
| 导航宽度矩阵（13 档 320→1024px） | ✅ 全部无溢出、最后一项可见 |
| gzip 协商（curl） | ✅ -68%/-82%、MD5 校验一致、Vary 正确 |
| 代码分割（浏览器 Network） | ✅ 按页 chunk 按需加载、入口 15KB |
| 门禁 | Go 全量 + `-race` + `vet` + `gofmt` 全过；前端 **33 文件 188 测试** |

### 第八轮测试说明与边界

**已覆盖：** 6 页面 × 桌面/移动/深色三矩阵布局与控制台错误；导航 13 宽度矩阵；设计系统一致性（按钮字重/表头/卡片/token 完整性）；资源管线（压缩/缓存头/HEAD/SPA fallback）；代码分割按需加载。

**未覆盖（明确说明）：**
- 写操作（创建/删除账号、别名、秘钥、任务）——本轮为 UI 检测，写路径由既有 E2E 覆盖。
- 真实 IMAP 取信——依赖外部邮箱可用性；`/inbox` 渲染已覆盖。
- 跨浏览器（仅 Chromium；`color-mix`/`backdrop-filter` 未在 Firefox/Safari 验证）。
- **登录路径说明：** 凭证库未保存本机登录项（用户拒绝保存）→ 管理页会话使用用户在本会话中提供的本地测试密码（该实例的 QA 凭据，仅回环地址测试环境）。

### 第八轮结论

- 用户三项要求（UI 优化检测、风格一致性、UI 性能优化）**全部达成**。
- 5 项发现全部修复；新增 2 类防回归测试（token 完整性 + gzip 行为），均通过变异反证。
- 性能：首屏传输 **-36%**（非收件箱页面 **-68%**）；入口 chunk **-98%**。
- 门禁全绿；桌面/移动/深色三矩阵零回归。


---

## 第九轮 Issues（本轮新增，2 项全部修复）

本轮针对用户点名的两处界面问题做定位与修复，并对其余 6 个页面做回归巡检。

### Issue #26: 日志页「日志类别」筛选器被拉成整行、无下拉箭头（High）

**现象**：筛选区只剩「日志类别」四个字与一条 1039px 宽的空白条，看不出是下拉框。

**根因**：该处用原生 `<select>`，命中两条全局规则：
1. `input, select, textarea { width: 100% }` —— `.logs-filter` 是 `display: flex`，100% 宽度在 flex 行里被拉伸到整行（实测 1039 / 1096px）；
2. `select { appearance: none }` —— 去掉浏览器默认箭头，而自定义箭头依赖 `background-image` 渐变，在 36px 高度 + 12px 内边距下几乎不可见。

**修复**：改用全站统一的 `components/SelectMenu`（胶囊触发按钮 131px + chevron + 毛玻璃弹层），`aria-label` 带当前值（「日志类别：邮件清理」）。

**验证**：桌面 1280 / 移动 375 均无溢出；点开弹层列出 4 个选项；选「邮件清理」后表格 12 行全部为该类别；原生 `<select>` 数量归零。

### Issue #27: 创建秘钥弹层「账号使用范围」无分组边框（Medium）

**现象**：同一弹层内，下方 5 个权限分组都是带边框的 `fieldset`，上方「账号使用范围」却是裸标签 + 复选框，视觉层级断裂。

**根因**：该区块用 `<div class="form-field">` + `<span class="field-label">`，没有任何容器样式；权限分组用 `fieldset.permission-group` 描边。

**修复**：改为 `<fieldset class="permission-group account-scope-group">` + `<legend>账号使用范围</legend>`，直接复用权限分组的边框/圆角/内边距与 legend 排版；仅补框内提示与列表的垂直间距。

**验证**：浏览器实测 `border 1px solid` / `border-radius 10px` / `padding 10px 12px 12px` / legend `13px 600`，与权限分组逐项相同；弹层其余排版未变（451px 宽、权限项单列、按钮静态）。

### 第九轮验证矩阵

| 项目 | 结果 |
|---|---|
| 桌面 1280 六页巡检 | ✅ 零控制台错误、零文档溢出 |
| 移动 390 六页巡检 | ✅ 零文档溢出（表格在 `.table-wrap` 内横向滚动，既有设计） |
| 日志类别筛选（桌面/移动） | ✅ 131px 胶囊、4 选项、过滤正确、回显正确 |
| 创建秘钥弹层（桌面 1280 / 移动 375） | ✅ 账号范围已套框、无溢出、按钮可达 |
| 秘钥写路径（UI 创建 → 列表 → 删除） | ✅ 创建成功、列表显示、删除后回到 2 条 |
| 门禁 | ✅ 前端 33 文件 191 测试 + lint 0 errors；Go 全量 + `vet` + `gofmt` 全绿 |

### 第九轮说明与边界

- 本轮**按用户要求保留创建秘钥页原有排版**：曾试做弹层加宽 / 权限两列网格 / 整组全选 / 底部吸附按钮，经确认后全部回退（`git checkout` 还原 3 个文件），仅保留「账号使用范围」补框这一项。
- 未覆盖：真实 IMAP 取信、跨浏览器（仅 Chromium）、写路径的并发/失败分支（由 Go 侧测试覆盖）。
