// 页面测试渲染器：一次性装上 QueryClient / Nav / ActionSheet / Toast 四层 Provider，
// 并提供 NavProbe / IntentSetter 观察与预置导航状态。
/* oxlint-disable react/only-export-components -- 测试辅助文件，导出渲染器与探针组件 */
/* oxlint-disable react-hooks/exhaustive-deps -- IntentSetter 只在挂载时预置一次导航状态 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { useEffect } from 'react'
import type { ReactNode } from 'react'
import { NavProvider, useNav } from '../nav'
import type { TabKey } from '../nav'
import { ActionSheetProvider, ToastProvider } from '../ui'

export function makeTestClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
}

export function renderPage(ui: ReactNode, options: { client?: QueryClient } = {}) {
  const client = options.client ?? makeTestClient()
  const view = render(
    <QueryClientProvider client={client}>
      <NavProvider>
        <ActionSheetProvider>
          <ToastProvider>{ui}</ToastProvider>
        </ActionSheetProvider>
      </NavProvider>
    </QueryClientProvider>,
  )
  return { ...view, client }
}

/** NavProbe 把当前导航状态暴露成可断言的 testid（tab/intent/栈顶页）。 */
export function NavProbe() {
  const nav = useNav()
  const top = nav.stack.length > 0 ? nav.stack[nav.stack.length - 1] : null
  return (
    <div>
      <div data-testid="nav-tab">{nav.tab}</div>
      <div data-testid="nav-intent">{nav.intent ?? ''}</div>
      <div data-testid="nav-top">{top ? top.k : ''}</div>
    </div>
  )
}

/** IntentSetter 挂载后模拟一次「带意图切 tab」，用于页面预置过滤的测试。 */
export function IntentSetter({ tab, intent }: { tab: TabKey; intent: string }) {
  const nav = useNav()
  useEffect(() => {
    nav.switchTab(tab, intent)
  }, [])
  return null
}
