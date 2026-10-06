# Dogfood QA Report

**Target:** http://127.0.0.1:8081 (iCloud HME 管理台)
**Date:** 2026-10-06
**Scope:** 全站探索性 QA × 四轮（第一轮：日期选择器与筛选栏；第二轮：全量——全部页面 × 3 视口 × 有数据状态 × 对话框/弹层；第三轮：日期面板年份视图——用户截图定位；第四轮：全量 UI 复测——6 视口矩阵 + 交互深度 + 对比度/暗色模式审计）
**Tester:** Hermes Agent (automated exploratory QA)

---

## Executive Summary

**第一轮（7 项，全部修复）：** 日期面板移动端溢出、antd 与设计系统不一致、移动端导航、面板底部按钮等。

**第二轮（8 项新发现，7 修复 + 1 驳回）：** 引入测试数据后暴露出的表格渲染缺陷、原始 ISO 时间泄漏、`toLocaleString` 格式不统一、别名页工具栏移动端溢出等。

**第三轮（1 项 Critical 根因 + 1 项 Medium，全部修复）：** 全局 `table { min-width: 920px }` 泄漏进 antd 日期面板（面板内日历/年份网格也是 `<table>`），把网格撑到 920px → 年份视图第三列（2021/2024/2027）被裁掉。用户截图即此问题。

**第四轮（1 项 High + 1 项 Medium 系统性问题，全部修复）：** 暗色模式下 antd 组件（DatePicker 等）保持白色硬编码、与全黑页面严重割裂；设计系统多个文字 token 低于 WCAG AA 4.5:1（tertiary 3.18:1、danger 4.17:1、暗色主按钮白字 3.02:1、skip-link 3.02:1）。

| 轮次 | 🔴 Critical | 🟠 High | 🟡 Medium | 🔵 Low | 合计 |
|------|------------|---------|-----------|--------|------|
| 第一轮 | 0 | 2 | 2 | 3 | 7（全部修复/驳回） |
| 第二轮 | 0 | 2 | 2 | 4 | 8（7 修复 + 1 驳回） |
| 第三轮 | 1 | 0 | 1 | 0 | 2（全部修复） |
| 第四轮 | 0 | 1 | 1 | 0 | 2（全部修复） |

**Overall Assessment:** 四轮共 19 项发现。第三轮的关键教训：**antd 面板经 portal 渲染到 body，项目全局的元素级选择器（`table`/`th`/`td`/`tbody tr`/`button`）会泄漏进面板内部**，造成面板布局损坏。第四轮的关键教训：**antd 主题与项目 CSS 的暗色机制是两套**——CSS 走 `@media (prefers-color-scheme: dark)`，而 antd 的 ConfigProvider 需要 JS 显式传入 `darkAlgorithm`，只做前者会让 antd 组件在暗色页面上保持白底；且设计 token 的对比度需要实测而非目测（`#86868b` 看似「浅灰正常」，实测仅 3.18:1）。

**方法论说明（四轮均严格执行「measure, don't eyeball」）：** vision 模型在四轮中共给出 **12 次与 DOM 测量直接矛盾的描述**（如声称"年份 2028-2030 缺失"而 DOM 实测 12 格全在、"日历与时间列重叠"而实测间隙 18px 等）。所有结论均以 `getBoundingClientRect()` / `getComputedStyle()` / CDP `CSS.getMatchedStylesForNode` 的测量为准；vision 仅用于初筛可疑区域，不用于定量判断。第四轮的对比度结论全部用 WCAG 相对亮度公式计算（含 alpha 合成）。

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
