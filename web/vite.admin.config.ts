import react from '@vitejs/plugin-react'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import type { Plugin } from 'vite'

// 管理面板是独立的桌面端前端，与 Mini App 分开构建、分开托管：
//   - base 固定 /admin/：产物引用 /admin/assets/* 与 /admin/api/*，由 Go 的
//     AdminPanelHandler 托管，入口页是 admin.html；
//   - outDir 与 Mini App 共用 webdist（Go 只 go:embed 一个目录），所以
//     emptyOutDir 必须为 false —— 否则会把先构建好的 index.html 清掉。
//     package.json 里保证先跑 Mini App 构建（vite build），再跑本配置。
//   - public/ 不复制：那里只有 Mini App 自托管的 telegram-web-app.js。
function adminDevIndex(): Plugin {
  return {
    name: 'menshen-admin-dev-index',
    apply: 'serve',
    configureServer(server) {
      // 开发时 /admin/ 直接给 admin.html；生产由 Go 做同样的 SPA 回退。
      server.middlewares.use((req, _res, next) => {
        if (req.url === '/admin' || req.url === '/admin/') req.url = '/admin/admin.html'
        next()
      })
    },
  }
}

// 产物目录要始终保留一个占位文件：go:embed all:webdist 要求目录非空，
// 而产物本身不入库；没有它时全新克隆的 go build / go test 会直接编译失败。
function keepWebdistPlaceholder(): Plugin {
  return {
    name: 'menshen-admin-webdist-placeholder',
    apply: 'build',
    closeBundle() {
      const outDir = resolve(import.meta.dirname, '../internal/panel/webdist')
      mkdirSync(outDir, { recursive: true })
      writeFileSync(resolve(outDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  base: '/admin/',
  publicDir: false,
  plugins: [react(), adminDevIndex(), keepWebdistPlaceholder()],
  build: {
    outDir: '../internal/panel/webdist',
    emptyOutDir: false,
    sourcemap: false,
    rollupOptions: {
      // 单入口：管理面板。Mini App 的 index/public 由 vite.config.ts 构建。
      input: { admin: resolve(import.meta.dirname, 'admin.html') },
    },
  },
  server: {
    // 本地开发把面板 API 代理到 Go 服务（默认监听 127.0.0.1:8081）。
    proxy: {
      '/admin/api': 'http://127.0.0.1:8081',
    },
  },
})
