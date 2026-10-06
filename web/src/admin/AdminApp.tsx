// 桌面端管理面板的应用外壳：主题、数据查询、全局提示与错误边界。
//
// 与 Mini App 的 App.tsx 是两套独立应用：这里不加载 Telegram SDK，鉴权走
// 会话 cookie（main.tsx 在挂载前注入 X-Web 与目标 bot）。
import { CssBaseline } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { ApiError } from '../api/client'
import { ErrorBoundary, ToastProvider } from '../ui'
import { AdminNavProvider } from './nav'
import { AdminShell } from './shell'
import { buildAdminTheme, prefersDark, subscribeSystemTheme } from './theme'

export function AdminApp() {
  const [dark, setDark] = useState(prefersDark)
  useEffect(() => subscribeSystemTheme(setDark), [])
  const theme = useMemo(() => buildAdminTheme(dark), [dark])

  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // 4xx（身份/权限/参数）重试无意义；网络失败与 5xx 重试一次后交给错误态。
            retry: (failureCount, error) => {
              if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
                return false
              }
              return failureCount < 1
            },
            refetchOnWindowFocus: false,
          },
        },
      }),
  )

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <QueryClientProvider client={queryClient}>
        <ToastProvider>
          <ErrorBoundary>
            <AdminNavProvider>
              <AdminShell />
            </AdminNavProvider>
          </ErrorBoundary>
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
