import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8081',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    globals: true,
    // 高负载(CI 并行/本机多任务)下 vitest 默认 5s 超时不足, 会导致假失败
    // (Error: Test timed out in 5000ms), 提高到 20s 仍能捕获真正的挂死。
    testTimeout: 20000,
    // beforeEach/afterEach 等 hook 在高负载下同样可能超时, 一并提高。
    hookTimeout: 20000,
  },
})
