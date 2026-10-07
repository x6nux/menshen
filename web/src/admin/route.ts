// 管理面板的路由：桌面端自己的路径模型，与 Mini App 的 tab/栈导航无关。
//
// 路径挂在 /admin 下，刷新与深链都能还原：
//
//   /admin                概览            /admin/bots/12          机器人详情
//   /admin/bots           机器人           /admin/chats/3/456      群组详情
//   /admin/chats          群组            /admin/logs/9           判定记录详情
//   /admin/records        记录            /admin/users/9          用户资料
//   /admin/lists/gban     名单·联合封禁    /admin/appeals/9        申诉详情
//   /admin/upstreams      上游渠道         /admin/upstreams/3      上游详情
//   /admin/models         模型定价         /admin/models/<name>    模型详情
//   /admin/rules          必封规则         /admin/rules/5          规则详情
//   /admin/settings       全局设置         /admin/syslog           运行日志
//
// 页面内的搜索词与筛选不写 URL（桌面端刷新丢失筛选是可接受的取舍），只有
// 只有看哪一页进路径。登录链接带的 ?bot= 是目标 bot，所有导航都原样带着它。
const BASE = '/admin'

export type Section =
  | 'overview'
  | 'bots'
  | 'chats'
  | 'records'
  | 'lists'
  | 'upstreams'
  | 'models'
  | 'rules'
  | 'settings'
  | 'syslog'

export type AdminRoute =
  | { k: 'overview' }
  | { k: 'bots' }
  | { k: 'bot'; id: number }
  | { k: 'chats' }
  | { k: 'chat'; botId: number; chatId: number }
  | { k: 'records' }
  | { k: 'log'; id: number }
  | { k: 'user'; id: number }
  | { k: 'appeal'; id: number }
  | { k: 'lists'; section?: string }
  | { k: 'upstreams' }
  | { k: 'upstream'; id: number }
  | { k: 'models' }
  | { k: 'model'; name: string }
  | { k: 'rules' }
  | { k: 'rule'; id: number }
  | { k: 'settings' }
  | { k: 'syslog' }

/** sectionOf 把路由归到侧栏的哪一项（详情页归属它的列表所在分区）。 */
export function sectionOf(route: AdminRoute): Section {
  switch (route.k) {
    case 'bot':
      return 'bots'
    case 'chat':
      return 'chats'
    case 'log':
    case 'user':
    case 'appeal':
      return 'records'
    case 'upstream':
      return 'upstreams'
    case 'model':
      return 'models'
    case 'rule':
      return 'rules'
    default:
      return route.k
  }
}

/** toInt 解析整数路径段；负数（chat_id）也算，非整数返回 null。 */
function toInt(s: string | undefined): number | null {
  if (s === undefined || !/^-?\d+$/.test(s)) return null
  const n = Number(s)
  return Number.isSafeInteger(n) ? n : null
}

/** parseAdminRoute 解析 /admin 下的路径；未知路径回退到概览。 */
export function parseAdminRoute(pathname: string): AdminRoute {
  let rest = pathname.startsWith(BASE) ? pathname.slice(BASE.length) : pathname
  rest = rest.replace(/^\/+/, '').replace(/\/+$/, '')
  if (rest === '') return { k: 'overview' }
  const seg = rest.split('/')
  const id = (i: number) => toInt(seg[i])

  switch (seg[0]) {
    case 'bots': {
      const botID = id(1)
      return seg.length === 2 && botID !== null ? { k: 'bot', id: botID } : { k: 'bots' }
    }
    case 'chats': {
      const botID = id(1)
      const chatID = id(2)
      if (seg.length === 3 && botID !== null && chatID !== null) {
        return { k: 'chat', botId: botID, chatId: chatID }
      }
      return { k: 'chats' }
    }
    case 'records':
      return { k: 'records' }
    case 'logs': {
      const logID = id(1)
      return seg.length === 2 && logID !== null ? { k: 'log', id: logID } : { k: 'records' }
    }
    case 'users': {
      const uid = id(1)
      return seg.length === 2 && uid !== null ? { k: 'user', id: uid } : { k: 'records' }
    }
    case 'appeals': {
      const aid = id(1)
      return seg.length === 2 && aid !== null ? { k: 'appeal', id: aid } : { k: 'records' }
    }
    case 'lists':
      return seg.length === 2
        ? { k: 'lists', section: decodeURIComponent(seg[1]) }
        : { k: 'lists' }
    case 'upstreams': {
      const upID = id(1)
      return seg.length === 2 && upID !== null ? { k: 'upstream', id: upID } : { k: 'upstreams' }
    }
    case 'models':
      return seg.length === 2
        ? { k: 'model', name: decodeURIComponent(seg[1]) }
        : { k: 'models' }
    case 'rules': {
      const rid = id(1)
      return seg.length === 2 && rid !== null ? { k: 'rule', id: rid } : { k: 'rules' }
    }
    case 'settings':
      return { k: 'settings' }
    case 'syslog':
      return { k: 'syslog' }
    default:
      return { k: 'overview' }
  }
}

/** slugOf 返回路由对应的路径片段（无前导斜杠）。 */
function slugOf(route: AdminRoute): string {
  switch (route.k) {
    case 'overview':
      return ''
    case 'bot':
      return `bots/${route.id}`
    case 'chat':
      return `chats/${route.botId}/${route.chatId}`
    case 'log':
      return `logs/${route.id}`
    case 'user':
      return `users/${route.id}`
    case 'appeal':
      return `appeals/${route.id}`
    case 'lists':
      return route.section ? `lists/${encodeURIComponent(route.section)}` : 'lists'
    case 'upstream':
      return `upstreams/${route.id}`
    case 'model':
      return `models/${encodeURIComponent(route.name)}`
    case 'rule':
      return `rules/${route.id}`
    default:
      return route.k
  }
}

/** botParam 读当前地址上的 ?bot=（登录链接带进来的目标 bot）。 */
function botParam(): string | null {
  if (typeof location === 'undefined') return null
  return new URLSearchParams(location.search).get('bot')
}

/** adminPath 拼出完整路径；保留 ?bot=，保证刷新后仍是同一个 bot 上下文。 */
export function adminPath(route: AdminRoute): string {
  const slug = slugOf(route)
  const path = slug === '' ? `${BASE}/` : `${BASE}/${slug}`
  const bot = botParam()
  return bot ? `${path}?bot=${encodeURIComponent(bot)}` : path
}
