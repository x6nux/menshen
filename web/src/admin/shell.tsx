// 管理面板的桌面外壳：左侧固定导航 + 顶部标题栏 + 内容区。
//
// 这是给桌面端单独写的一套外壳，不用 Mini App 的 TabBar/TopBar/导航栈。
// 路由是简单的一层：一个 section（侧栏项）或它的一个详情页。
import { Box, Button, Drawer, List, ListItemButton, ListItemText, Skeleton, Typography } from '@mui/material'
import { useMiniState } from '../api/hooks'
import { ApiError } from '../api/client'
import { meLabel } from '../lib/format'
import { ErrorState } from '../ui'
import { PageHeader } from './components'
import { useAdminNav } from './nav'
import { sectionOf } from './route'
import type { AdminRoute, Section } from './route'
import { AppealDetailView } from './views/AppealDetailView'
import { BotDetailView } from './views/BotDetailView'
import { BotsView } from './views/BotsView'
import { ChatDetailView } from './views/ChatDetailView'
import { ChatsView } from './views/ChatsView'
import { ListsView } from './views/ListsView'
import { LogDetailView } from './views/LogDetailView'
import { ModelsView, ModelDetailView } from './views/ModelsView'
import { OverviewView } from './views/OverviewView'
import { RecordsView } from './views/RecordsView'
import { RuleDetailView, RulesView } from './views/RulesView'
import { SettingsView } from './views/SettingsView'
import { SysLogView } from './views/SysLogView'
import { UpstreamDetailView } from './views/UpstreamDetailView'
import { UpstreamsView } from './views/UpstreamsView'
import { UserView } from './views/UserView'

const DRAWER_WIDTH = 224

const PRIMARY: { section: Section; label: string }[] = [
  { section: 'overview', label: '概览' },
  { section: 'bots', label: '机器人' },
  { section: 'chats', label: '群组' },
  { section: 'records', label: '记录' },
]

/** 管理类入口；主管理员全见，次级管理员只看名单（联合封禁）。 */
const MANAGE: { section: Section; label: string; mainOnly: boolean }[] = [
  { section: 'lists', label: '名单管理', mainOnly: false },
  { section: 'upstreams', label: '上游渠道', mainOnly: true },
  { section: 'models', label: '模型定价', mainOnly: true },
  { section: 'rules', label: 'AI 必封规则', mainOnly: true },
  { section: 'syslog', label: '运行日志', mainOnly: true },
  { section: 'settings', label: '全局设置', mainOnly: true },
]

const SECTION_TITLES: Record<Section, string> = {
  overview: '概览',
  bots: '机器人',
  chats: '群组',
  records: '记录',
  lists: '名单管理',
  upstreams: '上游渠道',
  models: '模型定价',
  rules: 'AI 必封规则',
  settings: '全局设置',
  syslog: '运行日志',
}

/** MAIN_ONLY_ROUTES 是主管理员专属路由集合；DETAIL_TITLES 是二级页标题（具体名称由各详情视图补全）。 */
const MAIN_ONLY_ROUTES = new Set<AdminRoute['k']>([
  'upstreams',
  'upstream',
  'models',
  'model',
  'rules',
  'rule',
  'settings',
  'syslog',
])
const DETAIL_TITLES: Partial<Record<AdminRoute['k'], string>> = {
  bot: '机器人详情',
  chat: '群组详情',
  log: '判定记录',
  user: '用户资料',
  appeal: '申诉详情',
  upstream: '上游详情',
  model: '模型详情',
  rule: '规则详情',
}

export function AdminShell() {
  const state = useMiniState(true)

  if (state.isPending) return <ShellSkeleton />
  if (state.isError) {
    const status = state.error instanceof ApiError ? state.error.status : 0
    if (status === 401) return <LoginRequired onRetry={() => void state.refetch()} />
    return (
      <Box sx={{ p: 4, maxWidth: 720, mx: 'auto' }}>
        <ErrorState status={status} onRetry={() => void state.refetch()} />
      </Box>
    )
  }

  const me = state.data.me
  return <Layout main={me.main} identityLabel={`${me.main ? '主管理员' : '次级管理员'} · ${meLabel(me)}`} />
}

function Layout({ main, identityLabel }: { main: boolean; identityLabel: string }) {
  const { route, go } = useAdminNav()
  const section = sectionOf(route)
  const isDetail = route.k !== section

  return (
    <Box sx={{ display: 'flex', minHeight: '100dvh', bgcolor: 'background.default' }}>
      <Drawer
        variant="permanent"
        sx={{
          width: DRAWER_WIDTH,
          flexShrink: 0,
          '& .MuiDrawer-paper': {
            width: DRAWER_WIDTH,
            boxSizing: 'border-box',
            borderRight: '1px solid',
            borderColor: 'divider',
            display: 'flex',
          },
        }}
      >
        <Box sx={{ px: 2, py: 2.5 }}>
          <Typography sx={{ fontSize: 18, fontWeight: 700 }}>🛡 门神</Typography>
          <Typography sx={{ fontSize: 12, color: 'text.secondary', mt: 0.25 }}>{identityLabel}</Typography>
        </Box>
        <List sx={{ px: 1 }}>
          {PRIMARY.map((item) => (
            <ListItemButton
              key={item.section}
              selected={section === item.section && !isDetail}
              onClick={() => go(sectionRoute(item.section))}
            >
              <ListItemText primary={item.label} slotProps={{ primary: { sx: { fontSize: 14 } } }} />
            </ListItemButton>
          ))}
        </List>
        <Typography sx={{ px: 2, pt: 1.5, pb: 0.5, fontSize: 12, color: 'text.secondary' }}>管理</Typography>
        <List sx={{ px: 1 }}>
          {MANAGE.filter((item) => main || !item.mainOnly).map((item) => (
            <ListItemButton
              key={item.section}
              selected={section === item.section}
              onClick={() => go(sectionRoute(item.section))}
            >
              <ListItemText primary={item.label} slotProps={{ primary: { sx: { fontSize: 14 } } }} />
            </ListItemButton>
          ))}
        </List>
        <Box sx={{ mt: 'auto', px: 2, py: 2 }}>
          <Button size="small" variant="outlined" fullWidth href="/admin/logout" component="a">
            退出登录
          </Button>
          <Typography sx={{ mt: 1, fontSize: 11.5, color: 'text.secondary', lineHeight: 1.6 }}>
            会话 12 小时有效，过期请回 Telegram 重新获取链接。
          </Typography>
        </Box>
      </Drawer>
      <Box component="main" sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <Box
          sx={{
            px: 3,
            py: 1.75,
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            borderBottom: '1px solid',
            borderColor: 'divider',
            bgcolor: 'background.paper',
            position: 'sticky',
            top: 0,
            zIndex: 1,
          }}
        >
          {isDetail && (
            <Button size="small" variant="text" onClick={() => go(sectionRoute(section), true)}>
              ← 返回
            </Button>
          )}
          <Typography sx={{ fontSize: 17, fontWeight: 700 }}>
            {DETAIL_TITLES[route.k] ?? SECTION_TITLES[section]}
          </Typography>
          <Typography sx={{ fontSize: 13, color: 'text.secondary', ml: 'auto' }}>
            {SECTION_TITLES[section]}
          </Typography>
        </Box>
        <Box sx={{ px: 3, py: 3, flex: 1 }}>
          <Box sx={{ maxWidth: 1360, mx: 'auto' }}>
            {renderRoute(route, main)}
          </Box>
        </Box>
      </Box>
    </Box>
  )
}

/** sectionRoute 返回侧栏项对应的根路由。 */
function sectionRoute(section: Section): AdminRoute {
  switch (section) {
    case 'lists':
      return { k: 'lists' }
    case 'overview':
      return { k: 'overview' }
    default:
      return { k: section } as AdminRoute
  }
}

/** renderRoute 把路由映射到对应视图。主管理员专属页对次级管理员给一句说明，
 *  也挡住侧栏之外的手改地址直达（服务端权限仍是最终裁决）。 */
function renderRoute(route: AdminRoute, main: boolean) {
  if (!main && MAIN_ONLY_ROUTES.has(route.k)) return <MainOnly />
  switch (route.k) {
    case 'overview':
      return <OverviewView />
    case 'bots':
      return <BotsView />
    case 'bot':
      return <BotDetailView botId={route.id} />
    case 'chats':
      return <ChatsView />
    case 'chat':
      return <ChatDetailView botId={route.botId} chatId={route.chatId} />
    case 'records':
      return <RecordsView />
    case 'log':
      return <LogDetailView id={route.id} />
    case 'user':
      return <UserView id={route.id} />
    case 'appeal':
      return <AppealDetailView id={route.id} />
    case 'lists':
      return <ListsView section={route.section} main={main} />
    case 'upstreams':
      return <UpstreamsView />
    case 'upstream':
      return <UpstreamDetailView id={route.id} />
    case 'models':
      return <ModelsView />
    case 'model':
      return <ModelDetailView name={route.name} />
    case 'rules':
      return <RulesView />
    case 'rule':
      return <RuleDetailView id={route.id} />
    case 'settings':
      return <SettingsView />
    case 'syslog':
      return <SysLogView />
  }
}

/** MainOnly 次级管理员访问主管理员专属页时的说明。 */
export function MainOnly() {
  return (
    <Box>
      <PageHeader title="仅主管理员" />
      <Typography sx={{ fontSize: 13.5, color: 'text.secondary', lineHeight: 1.8 }}>
        该功能由主管理员统一配置。你是次级管理员，可以管理自己名下的机器人与群组。
      </Typography>
    </Box>
  )
}

/** LoginRequired 会话缺失/过期时的指引：回 Telegram 重新拿链接。 */
function LoginRequired({ onRetry }: { onRetry: () => void }) {
  return (
    <Box sx={{ maxWidth: 520, mx: 'auto', mt: 12, p: 3, bgcolor: 'background.paper', borderRadius: 2 }}>
      <Typography sx={{ fontSize: 18, fontWeight: 700, mb: 1 }}>需要登录</Typography>
      <Typography sx={{ fontSize: 14, lineHeight: 1.8 }}>
        请在 Telegram 里打开与 bot 的私聊，点击菜单里的「🖥 网页版（浏览器打开）」获取登录链接；
        链接 10 分钟内有效，打开即登录。
      </Typography>
      <Button variant="contained" sx={{ mt: 2 }} onClick={onRetry}>
        已重新获取，重试
      </Button>
    </Box>
  )
}

function ShellSkeleton() {
  return (
    <Box sx={{ display: 'flex', minHeight: '100dvh' }}>
      <Box sx={{ width: DRAWER_WIDTH, p: 2, borderRight: '1px solid', borderColor: 'divider' }}>
        <Skeleton variant="text" width={96} height={30} />
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} variant="text" height={30} />
        ))}
      </Box>
      <Box sx={{ flex: 1, p: 3 }}>
        <Skeleton variant="rounded" height={64} sx={{ mb: 2 }} />
        <Skeleton variant="rounded" height={240} />
      </Box>
    </Box>
  )
}
