// DesktopShell 是网页版（浏览器打开）的桌面外壳：左侧固定导航 + 宽内容区。
//
// 与移动端外壳共用同一套导航栈与页面组件（shellPages.tsx），区别只有：
//   - 一级入口直接列在侧栏，管理类页面（名单/上游/模型/规则/设置）一键直达；
//   - 二级页在内容区渲染，附一个「返回」按钮（浏览器里没有 Telegram BackButton）；
//   - 会话过期（401）时显示「获取登录链接」的指引而不是通用错误页。
import {
  Box,
  Button,
  Drawer,
  List,
  ListItemButton,
  ListItemText,
  Skeleton,
  Typography,
} from '@mui/material'
import { errorStatus } from './api/client'
import { useMiniState } from './api/hooks'
import { meLabel } from './lib/format'
import { useNav } from './nav'
import type { Page, TabKey } from './nav'
import { PageScope } from './nav'
import { routeSlug } from './route'
import { detailTitle, renderStackPage, renderTabPage, TAB_NAMES } from './shellPages'
import { ErrorState, Skeletons } from './ui'

const DRAWER_WIDTH = 236

const PRIMARY: { tab: TabKey; label: string }[] = [
  { tab: 'overview', label: '概览' },
  { tab: 'bots', label: '机器人' },
  { tab: 'chats', label: '群组' },
  { tab: 'records', label: '记录' },
  { tab: 'mine', label: '我的' },
]

/** 管理类页面：主管理员专属直达入口。 */
const MANAGE: { page: Page; label: string }[] = [
  { page: { k: 'lists' }, label: '名单管理' },
  { page: { k: 'upstreams' }, label: '上游渠道' },
  { page: { k: 'models' }, label: '模型定价' },
  { page: { k: 'rules' }, label: 'AI 必封规则' },
  { page: { k: 'syslog' }, label: '运行日志' },
  { page: { k: 'settings' }, label: '全局设置' },
]

/** pageKey 给无参数页面一个稳定的比较键。 */
function pageKey(p: Page): string {
  switch (p.k) {
    case 'bot':
      return `bot:${p.id}`
    case 'chat':
      return `chat:${p.botId}:${p.chatId}`
    case 'log':
      return `log:${p.id}`
    case 'user':
      return `user:${p.id}`
    case 'appeal':
      return `appeal:${p.id}`
    case 'upstream':
      return `upstream:${p.id}`
    case 'model':
      return `model:${p.name}`
    case 'rule':
      return `rule:${p.id}`
    case 'lists':
      return `lists:${p.section ?? ''}`
    default:
      return p.k
  }
}

export function DesktopShell() {
  const nav = useNav()
  const state = useMiniState(true)
  const top = nav.stack.length > 0 ? nav.stack[nav.stack.length - 1] : null

  if (state.isPending) return <DesktopSkeleton />
  if (state.isError) {
    const status = errorStatus(state.error)
    if (status === 401) return <LoginRequired onRetry={() => void state.refetch()} />
    return (
      <Box sx={{ p: 4, maxWidth: 720, mx: 'auto' }}>
        <ErrorState status={status} onRetry={() => void state.refetch()} />
      </Box>
    )
  }

  const me = state.data.me
  const title = top ? detailTitle(top, state.data) : TAB_NAMES[nav.tab]
  const topKey = top ? pageKey(top) : ''

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
        <Box sx={{ px: 2.5, py: 2.5 }}>
          <Typography sx={{ fontSize: 19, fontWeight: 700 }}>🛡 门神</Typography>
          <Typography sx={{ fontSize: 12.5, color: 'text.secondary', mt: 0.25 }}>
            {me.main ? '主管理员' : '次级管理员'} · {meLabel(me)}
          </Typography>
        </Box>
        <List sx={{ px: 1 }}>
          {PRIMARY.map((it) => (
            <ListItemButton
              key={it.tab}
              selected={!top && nav.tab === it.tab}
              onClick={() => nav.switchTab(it.tab)}
            >
              <ListItemText primary={it.label} />
            </ListItemButton>
          ))}
        </List>
        {me.main && (
          <>
            <Typography
              sx={{ px: 2.5, pt: 1.5, pb: 0.5, fontSize: 12, color: 'text.secondary' }}
            >
              管理
            </Typography>
            <List sx={{ px: 1 }}>
              {MANAGE.map((it) => (
                <ListItemButton
                  key={it.label}
                  selected={topKey === pageKey(it.page)}
                  onClick={() => {
                    nav.switchTab('mine')
                    nav.push(it.page)
                  }}
                >
                  <ListItemText primary={it.label} />
                </ListItemButton>
              ))}
            </List>
          </>
        )}
        <Box sx={{ mt: 'auto', px: 2.5, py: 2 }}>
          <Button
            size="small"
            variant="outlined"
            fullWidth
            href="/admin/logout"
            component="a"
          >
            退出登录
          </Button>
          <Typography sx={{ mt: 1, fontSize: 11.5, color: 'text.secondary', lineHeight: 1.6 }}>
            会话 12 小时有效，过期请回 Telegram 重新获取链接。
          </Typography>
        </Box>
      </Drawer>
      <Box component="main" sx={{ flex: 1, minWidth: 0 }}>
        <Box
          sx={{
            px: 4,
            py: 2,
            display: 'flex',
            alignItems: 'center',
            gap: 1.5,
            borderBottom: '1px solid',
            borderColor: 'divider',
            bgcolor: 'background.paper',
          }}
        >
          {top && (
            <Button size="small" variant="text" onClick={() => nav.pop()}>
              ← 返回
            </Button>
          )}
          <Typography sx={{ fontSize: 18, fontWeight: 700 }}>{title}</Typography>
        </Box>
        <Box sx={{ px: 4, py: 3, maxWidth: 1280, mx: 'auto' }}>
          {top ? (
            <PageScope key={routeSlug(nav.tab, nav.stack)} scope={routeSlug(nav.tab, nav.stack)} active>
              {renderStackPage(top)}
            </PageScope>
          ) : (
            <PageScope key={routeSlug(nav.tab, [])} scope={routeSlug(nav.tab, [])} active>
              {renderTabPage(nav.tab)}
            </PageScope>
          )}
        </Box>
      </Box>
    </Box>
  )
}

/** LoginRequired 是会话缺失/过期时的指引：回 Telegram 重新拿链接。 */
function LoginRequired({ onRetry }: { onRetry: () => void }) {
  return (
    <Box
      sx={{
        maxWidth: 520,
        mx: 'auto',
        mt: 10,
        p: 3,
        bgcolor: 'background.paper',
        borderRadius: 2,
      }}
    >
      <Typography sx={{ fontSize: 18, fontWeight: 700, mb: 1 }}>需要登录</Typography>
      <Typography sx={{ fontSize: 14, lineHeight: 1.8 }}>
        请在 Telegram 里打开与 bot 的私聊，点击菜单里的「🖥 网页版（浏览器打开）」
        获取登录链接；链接 10 分钟内有效，打开即登录。
      </Typography>
      <Button variant="contained" sx={{ mt: 2 }} onClick={onRetry}>
        已重新获取，重试
      </Button>
    </Box>
  )
}

function DesktopSkeleton() {
  return (
    <Box sx={{ display: 'flex', minHeight: '100dvh' }}>
      <Box sx={{ width: DRAWER_WIDTH, p: 2, borderRight: '1px solid', borderColor: 'divider' }}>
        <Skeleton variant="text" width={96} height={30} />
        <Skeletons rows={5} />
      </Box>
      <Box sx={{ flex: 1, p: 4 }}>
        <Skeleton variant="rounded" height={72} sx={{ mb: 2 }} />
        <Skeletons rows={4} />
      </Box>
    </Box>
  )
}

/** isWebMode 报告当前是否是网页版（/admin）路径。 */
export function isWebMode(): boolean {
  return typeof location !== 'undefined' && location.pathname.startsWith('/admin')
}
