// oxlint-disable react/only-export-components -- Provider、hook 与工具 hook 共享同一模块。
// 管理面板的导航上下文：当前路由 + 跳转。侧栏、返回按钮与各视图都从这里取。
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { adminPath, parseAdminRoute } from './route'
import type { AdminRoute } from './route'

export interface AdminNav {
  route: AdminRoute
  /** go 跳转到某个路由；replace=true 时不新增历史（返回按钮用）。 */
  go(route: AdminRoute, replace?: boolean): void
}

const AdminNavContext = createContext<AdminNav | null>(null)

export function AdminNavProvider({ children }: { children: ReactNode }) {
  const [route, setRoute] = useState<AdminRoute>(() =>
    parseAdminRoute(typeof location !== 'undefined' ? location.pathname : '/admin'),
  )

  // 浏览器前进/后退：从路径反解路由。
  useEffect(() => {
    const onPop = () => setRoute(parseAdminRoute(location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const go = useCallback((next: AdminRoute, replace = false) => {
    setRoute(next)
    const url = adminPath(next)
    const current = location.pathname + location.search
    if (url === current) return
    if (replace) history.replaceState(null, '', url)
    else history.pushState(null, '', url)
  }, [])

  const value = useMemo<AdminNav>(() => ({ route, go }), [route, go])
  return <AdminNavContext.Provider value={value}>{children}</AdminNavContext.Provider>
}

/** useAdminNav 必须在 AdminNavProvider 内使用。 */
// oxlint-disable-next-line react/only-export-components -- Provider 与 hook 共享同一个 context。
export function useAdminNav(): AdminNav {
  const ctx = useContext(AdminNavContext)
  if (!ctx) throw new Error('useAdminNav 必须在 AdminNavProvider 内使用')
  return ctx
}

/** useDebouncedValue 延迟返回最新值：服务端搜索用（输入 300ms 内不重复发请求）。 */
export function useDebouncedValue<T>(value: T, ms = 300): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), ms)
    return () => window.clearTimeout(timer)
  }, [value, ms])
  return debounced
}
