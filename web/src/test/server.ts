// 页面测试共用的 MSW server：默认走 src/mocks/handlers（与开发 mock 同一份），
// 用例内用 server.use() 覆盖单个 op 来捕获请求体或注入失败。
/* oxlint-disable react/only-export-components -- 测试辅助文件，导出 server 与启动函数 */
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { handlers } from '../mocks/handlers'

export const server = setupServer(...handlers)

/** startTestServer 在每个测试文件顶层调用一次。 */
export function startTestServer(): void {
  beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
  afterEach(() => server.resetHandlers())
  afterAll(() => server.close())
}
