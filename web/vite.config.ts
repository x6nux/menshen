import react from '@vitejs/plugin-react'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { defineConfig } from 'vitest/config'
import type { Plugin } from 'vite'

// msw 浏览器 mock（VITE_MOCK=1）需要 /miniapp/mockServiceWorker.js，而工程
// 关掉了 publicDir（产物根目录不留额外文件）。开发时由这个插件从 msw 依赖
// 里直接送出，仓库因此不需要提交一份生成文件。
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

// base 固定为 /miniapp/：产物由 Go 在 /miniapp 下托管（internal/panel/miniapp.go），
// 开发服务器也走同一路径，保证 dev 与线上引用资源的方式一致。
export default defineConfig({
  base: '/miniapp/',
  // 约定静态资源只走 src 的 import（进 assets/），不设 public 目录：
  // 产物根下多出文件的话，会被 /miniapp/<其他> 的 SPA 回退遮蔽。
  publicDir: false,
  plugins: [react(), mswWorkerPlugin()],
  build: {
    // 产物直接落到 Go 侧 go:embed 的目录；该目录不入库（见 .gitignore），
    // 由 Docker/CI 或本地 npm run build 生成。
    outDir: '../internal/panel/webdist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        // React 与 MUI 体积大且很少变动，单独分包便于缓存。
        // Vite 8（Rolldown）只接受函数形式，这里按包路径归入 vendor。
        manualChunks: (id) => {
          if (/node_modules\/(react|react-dom|@mui|@emotion)\//.test(id)) {
            return 'vendor'
          }
        },
      },
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
