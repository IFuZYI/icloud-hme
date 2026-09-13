# 前端管理界面

React 19 + TypeScript + Vite 的单页应用，构建产物内嵌到 Go 二进制（`internal/webui/dist`）。

## 开发

```bash
npm ci          # 安装依赖（锁定版本）
npm run dev     # 启动 Vite 开发服务器，/api 代理到 http://127.0.0.1:8081
```

## 检查

```bash
npm run lint       # ESLint
npm run test:run   # Vitest 单元/组件测试
npm run build      # TypeScript 类型检查 + Vite 构建（输出到 ../internal/webui/dist）
npm run check      # lint + test + build 一键执行
```

## 结构

- `src/pages/` — 各页面（账号 / 别名 / 收件箱 / 自动任务 / 日志 / 登录）
- `src/components/` — 可复用组件与对话框
- `src/api/` — API 客户端与类型定义
- `src/auth/` — 管理员会话状态管理
- `src/test/` — 测试环境（MSW mock 服务端）
