import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.tsx'

// VITE_MOCK=1 时先启动 MSW（开发用）；生产构建里条件为假，
// mocks 目录会作为独立 chunk 被摇掉，不进首屏。
async function bootstrap() {
  if (import.meta.env.VITE_MOCK) {
    const { startMocks } = await import('./mocks')
    await startMocks()
  }
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
}

void bootstrap()
