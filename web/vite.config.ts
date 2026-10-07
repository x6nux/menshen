import react from '@vitejs/plugin-react'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { defineConfig } from 'vitest/config'
import type { Plugin } from 'vite'

// msw 浏览器 mock（VITE_MOCK=1）需要 /miniapp/mockServiceWorker.js。该文件不在
// public/ 下，不会被复制进产物；开发时由本插件从 msw 依赖里直接送出，
// 仓库因此不需要提交一份生成文件。
function mswWorkerPlugin(): Plugin {
  return {
    name: 'menshen-msw-worker',
    apply: 'serve',
    configureServer(server) {
      const require = createRequire(import.meta.url)
      const workerFile = require.resolve('msw/mockServiceWorker.js')
      server.middlewares.use('/miniapp/mockServiceWorker.js', (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
        res.setHeader('Cache-Control', 'no-store')
        res.end(readFileSync(workerFile))
      })
    },
  }
}

// 产物目录要始终保留一个占位文件：go:embed all:webdist 要求目录非空，而
// 产物本身不入库；没有它时全新克隆的 go build / go test 会直接编译失败。
// vite 构建会清空 outDir，所以在构建结束时把 .gitkeep 写回来（该文件入库）。
function keepWebdistPlaceholder(): Plugin {
  return {
    name: 'menshen-webdist-placeholder',
    apply: 'build',
    closeBundle() {
      const outDir = resolve(import.meta.dirname, '../internal/panel/webdist')
      mkdirSync(outDir, { recursive: true })
      writeFileSync(resolve(outDir, '.gitkeep'), '')
    },
  }
}

// base 固定为 /miniapp/：产物由 Go 在 /miniapp 下托管（internal/panel/miniapp.go），
// 开发服务器也走同一路径，保证 dev 与线上引用资源的方式一致。
export default defineConfig({
  base: '/miniapp/',
  // public/ 随产物复制：目前只有自托管的 telegram-web-app.js（见 index.html）。
  // 产物根下的文件由 Go 在 SPA 回退之前直接服务（internal/panel/miniapp.go）。
  publicDir: 'public',
  plugins: [react(), mswWorkerPlugin(), keepWebdistPlaceholder()],
  build: {
    // 产物直接落到 Go 侧 go:embed 的目录；该目录不入库（见 .gitignore），
    // 由 Docker/CI 或本地 npm run build 生成。
    outDir: '../internal/panel/webdist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      // 两个入口：index 是 Telegram Mini App；public 是申诉验证、原文查看、
      // 申诉详情等公开网页（_w 路由），单独分包避免公开页加载整个管理端。
      // 桌面端管理面板是独立构建（vite.admin.config.ts，base /admin/），不在这里。
      input: {
        main: resolve(import.meta.dirname, 'index.html'),
        public: resolve(import.meta.dirname, 'public.html'),
      },
      // vendor 分包交给 Rolldown 自动分组：多入口下手工分包会把 CJS 包装函数
      // 与 MUI 拆进互相 import 的两个 chunk，模块初始化顺序反转时触发 TypeError。
    },
  },
  server: {
    // 本地开发把 API 代理到 Go 服务（默认监听 127.0.0.1:8081）。
    proxy: {
      '/miniapp/api': 'http://127.0.0.1:8081',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
})
