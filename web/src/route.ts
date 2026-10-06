// 路由编码：导航位置（一级 tab + 二级页栈）与 URL 路径互转。
//
// 这是 Mini App 的路由，应用挂载在 /miniapp 下，路径一律相对该前缀。
// 桌面端管理面板有自己的路由（src/admin/route.ts，挂在 /admin）。
//
//   /            概览            /bots           机器人
//   /bots/12     机器人详情      /chats/3/456    群组详情
//   /records     记录            /logs/9         记录详情
//   /lists/gban  名单·联合封禁   /settings       全局设置
//
// 页面内的筛选、搜索、分段不进路径——那是同一位置的不同视图，由各页通过
// nav.usePageParam 写进查询串（见 nav.tsx）。
import type { Page, TabKey } from './nav'

/** TAB_SLUG 一级 tab 的路径片段；概览是根路径，片段为空串。 */
const TAB_SLUG: Record<TabKey, string> = {
  overview: '',
  bots: 'bots',
  chats: 'chats',
  records: 'records',
  mine: 'mine',
}

/** appBase 返回 Mini App 的挂载前缀（开发服务器与线上都是 /miniapp）。 */
export function appBase(): string {
  return '/miniapp'
}

/** toInt 解析整数路径段；负数（chat_id）也算，非整数返回 null。 */
function toInt(s: string | undefined): number | null {
  if (s === undefined || !/^-?\d+$/.test(s)) return null
  const n = Number(s)
  return Number.isSafeInteger(n) ? n : null
}

/** pageSlug 把二级页编成路径片段（无前导斜杠）。 */
function pageSlug(page: Page): string {
  switch (page.k) {
    case 'bot':
      return `bots/${page.id}`
    case 'chat':
      return `chats/${page.botId}/${page.chatId}`
    case 'log':
      return `logs/${page.id}`
    case 'user':
      return `users/${page.id}`
    case 'appeal':
      return `appeals/${page.id}`
    case 'lists':
      // 分段（白名单/联封…）是页面内视图，走查询参数 ?seg=，不进路径。
      return 'lists'
    case 'upstreams':
      return 'upstreams'
    case 'upstream':
      return `upstreams/${page.id}`
    case 'models':
      return 'models'
    // 模型 ID 可能含 '/'（如 @cf/cloudflare/clef），编码后仍占一个路径段。
    case 'model':
      return `models/${encodeURIComponent(page.name)}`
    case 'rules':
      return 'rules'
    case 'rule':
      return `rules/${page.id}`
    case 'settings':
      return 'settings'
    case 'syslog':
      return 'syslog'
  }
}

/** routeSlug 返回「tab + 栈」对应的路径片段；栈非空时以栈顶为准。 */
export function routeSlug(tab: TabKey, stack: Page[]): string {
  const top = stack.length > 0 ? stack[stack.length - 1] : null
  return top ? pageSlug(top) : TAB_SLUG[tab]
}

/** pathOf 返回完整 URL 路径（含前缀，不含查询串）。 */
export function pathOf(tab: TabKey, stack: Page[]): string {
  const slug = routeSlug(tab, stack)
  return slug === '' ? `${appBase()}/` : `${appBase()}/${slug}`
}

/** Route 是解析出来的导航位置。 */
export interface Route {
  tab: TabKey
  stack: Page[]
}

/**
 * parseRoute 解析路径。未知路径回退到概览；二级页同时给出它下层的 tab
 * （如 /logs/9 的 tab 是「记录」），这样刷新后返回上一级落在合理的列表页。
 */
export function parseRoute(pathname: string): Route {
  const base = appBase()
  let rest = pathname.startsWith(base) ? pathname.slice(base.length) : pathname
  rest = rest.replace(/^\/+/, '').replace(/\/+$/, '')
  if (rest === '') return { tab: 'overview', stack: [] }

  const seg = rest.split('/')
  const head = seg[0]

  switch (head) {
    case 'bots': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'bots', stack: [{ k: 'bot', id }] }
      return { tab: 'bots', stack: [] }
    }
    case 'chats': {
      const botId = toInt(seg[1])
      const chatId = toInt(seg[2])
      if (seg.length === 3 && botId !== null && chatId !== null) {
        return { tab: 'chats', stack: [{ k: 'chat', botId, chatId }] }
      }
      return { tab: 'chats', stack: [] }
    }
    case 'logs': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'records', stack: [{ k: 'log', id }] }
      return { tab: 'records', stack: [] }
    }
    case 'users': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'records', stack: [{ k: 'user', id }] }
      return { tab: 'records', stack: [] }
    }
    case 'appeals': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'records', stack: [{ k: 'appeal', id }] }
      return { tab: 'records', stack: [] }
    }
    case 'lists': {
      if (seg.length === 2) {
        return { tab: 'mine', stack: [{ k: 'lists', section: decodeURIComponent(seg[1]) }] }
      }
      return { tab: 'mine', stack: [{ k: 'lists' }] }
    }
    case 'upstreams': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'mine', stack: [{ k: 'upstream', id }] }
      return { tab: 'mine', stack: [{ k: 'upstreams' }] }
    }
    case 'models': {
      if (seg.length === 2) {
        return { tab: 'mine', stack: [{ k: 'model', name: decodeURIComponent(seg[1]) }] }
      }
      return { tab: 'mine', stack: [{ k: 'models' }] }
    }
    case 'rules': {
      const id = toInt(seg[1])
      if (seg.length === 2 && id !== null) return { tab: 'mine', stack: [{ k: 'rule', id }] }
      return { tab: 'mine', stack: [{ k: 'rules' }] }
    }
    case 'settings':
      return { tab: 'mine', stack: [{ k: 'settings' }] }
    case 'syslog':
      return { tab: 'mine', stack: [{ k: 'syslog' }] }
    case 'records':
      return { tab: 'records', stack: [] }
    case 'mine':
      return { tab: 'mine', stack: [] }
    default:
      return { tab: 'overview', stack: [] }
  }
}
