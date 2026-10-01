// App 外壳：initTelegram() 的可用性驱动三种界面——
//   1) 初始化中：整页骨架；
//   2) SDK 不可用（被墙/没在 Telegram 里打开）：引导页（沿用旧页文案）；
//   3) 就绪：装配主题（getTheme 初读 + onThemeChanged 实时重建）、数据查询、
//      导航与全局 Provider，渲染 TopBar + 当前页 + TabBar。
//
// 契约：initData 就绪前不得发请求（api 鉴权头为空会 401）——本组件只在
// bridge.available 之后才挂载 Shell，Shell 内 useMiniState(true) 必已带上 initData。
// 页面映射集中在 Shell 一处，Task 2-4 会用真实页面替换 Placeholder。
import { Box, CssBaseline, Skeleton, Typography } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import type { Theme } from '@mui/material/styles'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { ApiError, setApiBridge } from './api/client'
import { useMiniState } from './api/hooks'
import { NavProvider, useNav } from './nav'
import type { Page, TabKey } from './nav'
import { Placeholder } from './pages/Placeholder'
import { buildMiniTheme } from './theme'
import { initTelegram } from './telegram'
import type { TelegramBridge, TelegramTheme } from './telegram'
import { ActionSheetProvider, ErrorState, Skeletons, TabBar, ToastProvider, TopBar } from './ui'

/** 一级 Tab 名称（占位页用）。 */
const TAB_NAMES: Record<TabKey, string> = {
  overview: '概览',
  bots: '机器人',
  chats: '群组',
  records: '记录',
  mine: '我的',
}

/** 二级页标题：Task 2-4 换成真实页面时同步维护。 */
const PAGE_NAMES: Record<Page['k'], string> = {
  bot: '机器人详情',
  chat: '群组详情',
  log: '记录详情',
  user: '用户资料',
  appeal: '申诉详情',
  lists: '名单管理',
  upstreams: '上游渠道',
  upstream: '上游详情',
  models: '模型定价',
  model: '模型详情',
  settings: '全局设置',
}

type Boot = { phase: 'booting' } | { phase: 'unavailable' } | { phase: 'ready'; bridge: TelegramBridge }

export default function App() {
  const [boot, setBoot] = useState<Boot>({ phase: 'booting' })

  useEffect(() => {
    let alive = true
    void initTelegram().then((bridge) => {
      if (!alive) return
      // 查询发出前注入鉴权头；不可用时 initData 为空串，也不会走到查询。
      setApiBridge(bridge)
      setBoot(bridge.available ? { phase: 'ready', bridge } : { phase: 'unavailable' })
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
              <Shell />
            </ToastProvider>
          </ActionSheetProvider>
        </NavProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}

function Shell() {
  const nav = useNav()
  // 外壳只在 bridge 就绪后挂载，这里 enabled 恒为 true 是安全的。
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
  const currentName = top ? PAGE_NAMES[top.k] : TAB_NAMES[nav.tab]

  return (
    <>
      <TopBar
        title={top ? currentName : '门神'}
        subtitle={top ? undefined : `${role} · uid ${me.uid}`}
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
        <Placeholder name={currentName} />
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
          如果你已经在 Telegram 里打开，说明官方脚本没加载出来（telegram.org
          在部分网络下不可达），换网络或挂代理后重试。
        </Typography>
      </Box>
    </Box>
  )
}
