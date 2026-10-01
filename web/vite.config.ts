import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// base 固定为 /miniapp/：产物由 Go 在 /miniapp 下托管（internal/panel/miniapp.go），
// 开发服务器也走同一路径，保证 dev 与线上引用资源的方式一致。
export default defineConfig({
  base: '/miniapp/',
  plugins: [react()],
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
  },
})
