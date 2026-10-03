// 页面映射与标题：移动端外壳（App.tsx）与网页版桌面外壳（DesktopShell）
// 共用同一份，避免两套导航各映射一套页面。
import type { ReactNode } from 'react'
import type { State } from './api/types'
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
import { RuleDetailPage, RulesPage } from './pages/RulesPage'
import { SettingsPage } from './pages/SettingsPage'
import { UpstreamDetailPage, UpstreamsPage } from './pages/UpstreamsPage'
import { UserPage } from './pages/UserPage'

/** 一级 Tab 名称（标题与侧栏用）。 */
export const TAB_NAMES: Record<TabKey, string> = {
  overview: '概览',
  bots: '机器人',
  chats: '群组',
  records: '记录',
  mine: '我的',
}

/** 二级页标题：列表数据里找不到具体名称时的回退。 */
export const PAGE_NAMES: Record<Page['k'], string> = {
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
  rules: 'AI 必封规则',
  rule: '规则详情',
  settings: '全局设置',
}

/** renderTabPage 是一级 Tab 的页面映射。 */
export function renderTabPage(tab: TabKey): ReactNode {
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
export function renderStackPage(page: Page): ReactNode {
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
    case 'rules':
      return <RulesPage />
    case 'rule':
      return <RuleDetailPage key={page.id} id={page.id} />
    case 'settings':
      return <SettingsPage />
  }
}

/** detailTitle 用列表数据给二级页一个具体标题，找不到再回退通用名。 */
export function detailTitle(page: Page, state: State): string {
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
    case 'rule':
      return `规则 #${page.id}`
    default:
      return PAGE_NAMES[page.k]
  }
}
