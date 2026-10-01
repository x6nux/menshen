// 开发期 mock 入口：只有 import.meta.env.VITE_MOCK 为真时由 main.tsx 动态加载，
// 生产构建里这段分支是死代码，handlers/fixtures 不会进首包。
export async function startMocks(): Promise<void> {
  const [{ setupWorker }, { handlers }] = await Promise.all([
    import('msw/browser'),
    import('./handlers'),
  ])
  const worker = setupWorker(...handlers)
  await worker.start({
    // publicDir 已关闭（产物根目录不落额外文件）：开发服务器由 vite.config.ts
    // 的 menshen-msw-worker 插件从 msw 依赖里送出这个脚本。
    serviceWorker: { url: `${import.meta.env.BASE_URL}mockServiceWorker.js` },
    onUnhandledRequest: 'bypass',
    quiet: true,
  })
  console.info('[mocks] MSW 已启动，数据来自 web/src/mocks/fixtures.ts')
}
