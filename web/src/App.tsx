// App 外壳：Telegram Mini App（只在 Telegram 内使用）。
//
// 浏览器的桌面端管理面板是独立入口与独立应用（web/src/admin，挂在 /admin），
// 不走这里，也不加载 Telegram SDK。
//
//   1) 初始化中：整页骨架；
//   2) SDK 不可用（不在 Telegram / 拉不到 SDK）：渲染引导页；
//   3) Telegram 里：initData 鉴权，就绪后挂载数据查询。
//
// 契约：鉴权信息就绪前不得发请求（api 鉴权头为空会 401）——桥注入之后才挂载。
import { Box, CssBaseline, Skeleton, Typography } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import type { Theme } from '@mui/material/styles'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { ApiError, setApiBridge } from './api/client'
import { useMiniState } from './api/hooks'
import { meLabel } from './lib/format'
import { NavProvider, PageScope, useNav } from './nav'
import { routeSlug } from './route'
import { detailTitle, renderStackPage, renderTabPage, TAB_NAMES } from './shellPages'
import { buildMiniTheme } from './theme'
import { initTelegram } from './telegram'
import type { TelegramBridge, TelegramTheme } from './telegram'
import { ActionSheetProvider, ErrorBoundary, ErrorState, Skeletons, TabBar, ToastProvider, TopBar } from './ui'

type Boot =
  | { phase: 'booting' }
  | { phase: 'unavailable' }
  | { phase: 'ready'; bridge: TelegramBridge }

export default function App() {
  const [boot, setBoot] = useState<Boot>({ phase: 'booting' })

  useEffect(() => {
    let alive = true
    void initTelegram()
      .then((bridge) => {
        if (!alive) return
        // 查询发出前注入鉴权头；不可用时 initData 为空串，也不会走到查询。
        setApiBridge(bridge)
        setBoot(bridge.available ? { phase: 'ready', bridge } : { phase: 'unavailable' })
      })
      .catch(() => {
        // initTelegram 自身不抛（超时返回不可用桥）；这里兜底注入实现的异常，
        // 让界面落到引导页而不是一直卡在骨架屏。
        if (alive) setBoot({ phase: 'unavailable' })
      })
    return () => {
      alive = false
    }
  }, [])

  if (boot.phase === 'booting') return <BootSkeleton />
  if (boot.phase === 'unavailable') return <UnavailableGuide />
  return <ReadyApp bridge={boot.bridge} />
}

/** useLiveTheme 初读 getTheme，并订阅 onThemeChanged 实时重建主题（不是静态快照）。 */
function useLiveTheme(bridge: TelegramBridge): Theme {
  const [theme, setTheme] = useState<Theme>(() => buildMiniTheme(bridge.getTheme()))
  useEffect(
    () => bridge.onThemeChanged((next: TelegramTheme) => setTheme(buildMiniTheme(next))),
    [bridge],
  )
  return theme
}

function ReadyApp({ bridge }: { bridge: TelegramBridge }) {
  const theme = useLiveTheme(bridge)
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
        <NavProvider backButton={bridge.backButton}>
          <ActionSheetProvider>
            <ToastProvider>
              <ErrorBoundary>
                <Shell />
              </ErrorBoundary>
            </ToastProvider>
          </ActionSheetProvider>
        </NavProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}

function Shell() {
  const nav = useNav()
  // 外壳只在桥就绪后挂载，这里 enabled 恒为 true 是安全的。
  const state = useMiniState(true)
  const top = nav.stack.length > 0 ? nav.stack[nav.stack.length - 1] : null

  if (state.isPending) {
    return (
      <Box sx={{ px: 2, pt: 1 }}>
        <Skeleton variant="text" width={96} height={30} sx={{ mx: 'auto', mb: 1 }} />
        <Skeleton variant="rounded" height={88} sx={{ borderRadius: '12px', mb: 1.5 }} />
        <Skeletons rows={3} />
      </Box>
    )
  }

  if (state.isError) {
    const status = state.error instanceof ApiError ? state.error.status : 0
    return (
      <>
        <TopBar title="门神" />
        <ErrorState status={status} onRetry={() => void state.refetch()} />
      </>
    )
  }

  const me = state.data.me
  const role = me.main ? '主管理员' : '次级管理员'
  const currentName = top ? detailTitle(top, state.data) : TAB_NAMES[nav.tab]

  return (
    <>
      <TopBar
        title={top ? currentName : '门神'}
        subtitle={top ? undefined : `${role} · ${meLabel(me)}`}
      />
      <Box
        component="main"
        sx={{
          px: 2,
          pt: 0.5,
          // 根页给固定 Tab 栏留位；二级页 Tab 栏隐藏，留常规底部间距即可。
          pb: top ? 4 : 'calc(72px + env(safe-area-inset-bottom))',
        }}
      >
        {/* Tab 根页常驻：push 二级页时只隐藏不卸载，返回后搜索词/批量选择等内存状态不丢。
            页面级筛选/搜索由 PageScope 写进 URL；被盖住时 active=false，只留内存不写 URL。 */}
        <Box sx={{ display: top ? 'none' : 'block' }}>
          <PageScope
            key={routeSlug(nav.tab, [])}
            scope={routeSlug(nav.tab, [])}
            active={top === null}
          >
            {renderTabPage(nav.tab)}
          </PageScope>
        </Box>
        {top && (
          <PageScope key={routeSlug(nav.tab, nav.stack)} scope={routeSlug(nav.tab, nav.stack)} active>
            {renderStackPage(top)}
          </PageScope>
        )}
      </Box>
      <TabBar />
    </>
  )
}

/** BootSkeleton 初始化阶段的整页骨架（此时还没有主题与导航）。 */
function BootSkeleton() {
  return (
    <Box data-testid="boot-skeleton" sx={{ minHeight: '100dvh', bgcolor: '#F5F6F7', px: 2, pt: 3 }}>
      <Skeleton variant="text" width={80} height={30} sx={{ mx: 'auto' }} />
      <Skeleton variant="text" width={140} height={20} sx={{ mx: 'auto', mb: 2 }} />
      <Skeleton variant="rounded" height={88} sx={{ borderRadius: '12px', mb: 1.5 }} />
      <Skeletons rows={3} />
    </Box>
  )
}

/** UnavailableGuide 复刻旧页的引导文案：SDK 没加载出来时给出可操作提示。 */
function UnavailableGuide() {
  return (
    <Box data-testid="unavailable-guide" sx={{ minHeight: '100dvh', bgcolor: '#F5F6F7', p: 2, pt: 6 }}>
      <Box sx={{ bgcolor: '#fff', borderRadius: '12px', p: 2 }}>
        <Typography sx={{ fontSize: 15, lineHeight: 1.6 }}>
          请通过 Telegram 里的菜单按钮「配置」打开本页。
        </Typography>
        <Typography sx={{ mt: 1, fontSize: 13, color: '#6B6B70', lineHeight: 1.6 }}>
          如果你已经在 Telegram 里打开，请关闭后重新打开本页；仍不行请更新 Telegram
          客户端或换网络重试。
        </Typography>
      </Box>
    </Box>
  )
}
