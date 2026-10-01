// App 外壳：initTelegram() 的可用性驱动三种界面——
//   1) 初始化中：整页骨架；
//   2) SDK 不可用（被墙/没在 Telegram 里打开）：引导页（沿用旧页文案）；
//   3) 就绪：装配主题（getTheme 初读 + onThemeChanged 实时重建）、数据查询、
//      导航与全局 Provider，渲染 TopBar + 当前页 + TabBar。
//
// 契约：initData 就绪前不得发请求（api 鉴权头为空会 401）——本组件只在
// bridge.available 之后才挂载 Shell，Shell 内 useMiniState(true) 必已带上 initData。
// 页面映射集中在 Shell 一处。
import { Box, CssBaseline, Skeleton, Typography } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import type { Theme } from '@mui/material/styles'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { ApiError, setApiBridge } from './api/client'
import { useMiniState } from './api/hooks'
import type { State } from './api/types'
import { NavProvider, useNav } from './nav'
import type { Page, TabKey } from './nav'
import { AppealDetailPage } from './pages/AppealDetailPage'
import { BotDetailPage } from './pages/BotDetailPage'
import { BotsPage } from './pages/BotsPage'
import { ChatDetailPage } from './pages/ChatDetailPage'
import { ChatsPage } from './pages/ChatsPage'
import { ListsPage } from './pages/ListsPage'
import { LogDetailPage } from './pages/LogDetailPage'
import { MinePage } from './pages/MinePage'
import { ModelDetailPage, ModelsPage } from './pages/ModelsPage'
import { OverviewPage } from './pages/OverviewPage'
import { RecordsPage } from './pages/RecordsPage'
import { SettingsPage } from './pages/SettingsPage'
import { UpstreamDetailPage, UpstreamsPage } from './pages/UpstreamsPage'
import { UserPage } from './pages/UserPage'
import { buildMiniTheme } from './theme'
import { initTelegram } from './telegram'
import type { TelegramBridge, TelegramTheme } from './telegram'
import { ActionSheetProvider, ErrorState, Skeletons, TabBar, ToastProvider, TopBar } from './ui'

/** 一级 Tab 名称（TopBar 标题用）。 */
const TAB_NAMES: Record<TabKey, string> = {
  overview: '概览',
  bots: '机器人',
  chats: '群组',
  records: '记录',
  mine: '我的',
}

/** 二级页标题：列表数据里找不到具体名称时的回退。 */
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
  const currentName = top ? detailTitle(top, state.data) : TAB_NAMES[nav.tab]

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
        {/* Tab 根页常驻：push 二级页时只隐藏不卸载，返回后搜索词/批量选择等内存状态不丢。 */}
        <Box sx={{ display: top ? 'none' : 'block' }}>{renderTabPage(nav.tab)}</Box>
        {top && renderStackPage(top)}
      </Box>
      <TabBar />
    </>
  )
}

/** renderTabPage 是一级 Tab 的页面映射。 */
function renderTabPage(tab: TabKey): ReactNode {
  switch (tab) {
    case 'overview':
      return <OverviewPage />
    case 'bots':
      return <BotsPage />
    case 'chats':
      return <ChatsPage />
    case 'records':
      return <RecordsPage />
    case 'mine':
      return <MinePage />
  }
}

/** renderStackPage 渲染导航栈顶的二级页。 */
function renderStackPage(page: Page): ReactNode {
  switch (page.k) {
    case 'bot':
      return <BotDetailPage botId={page.id} />
    case 'chat':
      return <ChatDetailPage botId={page.botId} chatId={page.chatId} />
    case 'log':
      return <LogDetailPage key={page.id} id={page.id} />
    case 'user':
      return <UserPage key={page.id} id={page.id} />
    case 'appeal':
      return <AppealDetailPage key={page.id} id={page.id} />
    case 'lists':
      // key 带上 section：白名单 → 联封等分段切换时重挂载，初始化到正确的分段。
      return <ListsPage key={page.section ?? 'default'} section={page.section} />
    case 'upstreams':
      return <UpstreamsPage />
    case 'upstream':
      return <UpstreamDetailPage key={page.id} id={page.id} />
    case 'models':
      return <ModelsPage />
    case 'model':
      return <ModelDetailPage key={page.name} name={page.name} />
    case 'settings':
      return <SettingsPage />
  }
}

/** detailTitle 用列表数据给二级页一个具体标题（bot 名 / 群名 / 上游名），找不到再回退通用名。 */
function detailTitle(page: Page, state: State): string {
  switch (page.k) {
    case 'bot':
      return state.bots.find((b) => b.bot_id === page.id)?.label ?? PAGE_NAMES.bot
    case 'chat': {
      const chat = state.chats.find(
        (c) => c.bot_id === page.botId && c.chat_id === page.chatId,
      )
      return chat?.title || (chat ? String(chat.chat_id) : PAGE_NAMES.chat)
    }
    case 'log':
      return `记录 #${page.id}`
    case 'user':
      return `用户 uid ${page.id}`
    case 'appeal':
      return `申诉 #${page.id}`
    case 'upstream':
      return (state.upstreams ?? []).find((u) => u.id === page.id)?.name ?? PAGE_NAMES.upstream
    case 'model':
      return page.name || PAGE_NAMES.model
    default:
      return PAGE_NAMES[page.k]
  }
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
