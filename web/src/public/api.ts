// 公开网页（_w）的数据接口与路由解析。
//
// 页面壳 GET 时由 Go 直接发；数据一律走同路径的 ?json=1 与 POST：
//   - ap  ：GET ?json=1 → 验证页数据；POST {token,signals} → 签发解禁码
//   - v   ：GET ?json=1 → 门槛数据；POST {e,k} → 原文与判定详情
//   - apv ：GET ?json=1 → 门槛数据；POST {e,k} → 申诉详情与用户资料

/** WebApiError 带上 HTTP 状态，页面据此区分失效（404/410）与普通失败。 */
export class WebApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'WebApiError'
  }
}

async function parseError(resp: Response): Promise<WebApiError> {
  let msg = `请求失败（${resp.status}）`
  try {
    const data = (await resp.json()) as { error?: string }
    if (data.error) msg = data.error
  } catch {
    // 非 JSON 响应保持通用文案
  }
  return new WebApiError(resp.status, msg)
}

export async function getJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url, {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
  })
  if (!resp.ok) throw await parseError(resp)
  return (await resp.json()) as T
}

export async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const resp = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(body),
  })
  if (!resp.ok) throw await parseError(resp)
  return (await resp.json()) as T
}

export type WebRouteKind = 'ap' | 'apv' | 'v'

export interface WebRoute {
  kind: WebRouteKind
  id: number
  sig: string
  /** 原始路径（不含查询串），POST 与 ?json=1 都发给它。 */
  path: string
}

/** parseRoute 从路径里解析 _w/<kind>/<id>/<sig>；形状不对返回 null。 */
export function parseRoute(pathname: string): WebRoute | null {
  const segs = pathname.split('/').filter(Boolean)
  const i = segs.indexOf('_w')
  if (i < 0 || i + 3 >= segs.length) return null
  const kind = segs[i + 1]
  if (kind !== 'ap' && kind !== 'apv' && kind !== 'v') return null
  const id = Number(segs[i + 2])
  if (!Number.isInteger(id) || id <= 0) return null
  return { kind, id, sig: segs[i + 3], path: '/' + segs.slice(0, i + 4).join('/') }
}

// ---- 接口数据类型 ----

export interface AppealLimit {
  type: string
  chat_id: number
  time: string
  reason: string
  text: string
}

export interface AppealMessage {
  chat_id: number
  title: string
  text: string
  time: string
}

export interface AppealData {
  appeal_id: number
  uid: number
  sitekey: string
  cdata: string
  days: number
  account: { uid: number; name: string }
  limits: AppealLimit[]
  messages: AppealMessage[]
  ai: {
    result: string
    label: string
    conf: number
    model: string
    reason: string
    statement: string
  }
}

export interface Gate {
  title: string
  warn: string
  exp: number
  k: string
  ttl_seconds: number
}

export interface LogRecordView {
  record: {
    id: number
    user_id: number
    user_name: string
    chat_id: number
    chat: string
    message_id: number
    text: string
    verdict: string
    confidence: number
    decider: string
    kind: string
    action: string
    action_label: string
    reason: string
    created: string
  }
  member: { joined: string; msgs: number; hits: number }
  history: { message_id: number; text: string; at: number; time: string; blocked: boolean }[]
  logs: {
    id: number
    verdict: string
    confidence: number
    action: string
    action_label: string
    at: number
    time: string
  }[]
}

export interface AppealPenalty {
  type: string
  label: string
  chat_id: number
  chat: string
  text: string
  reason: string
  at: number
  time: string
}

export interface AppealDossierView {
  id: number
  uid: number
  status: string
  statement: string
  ai_result: string
  ai_conf: number
  ai_reason: string
  ai_model: string
  web_attempts: number
  code: string
  code_expires: string
  created: string
  u_name: string
  bot: string
  first_seen: string
  joined: string
  last_msg: string
  msgs: number
  hits: number
  chats: number
  gban: string
  limits?: AppealPenalty[]
  penalties?: AppealPenalty[]
  history?: {
    chat: string
    text: string
    at: number
    time: string
    mark: string
    blocked: boolean
  }[]
  history_more?: {
    chat: string
    text: string
    at: number
    time: string
    mark: string
    blocked: boolean
  }[]
  history_count: number
  logs?: {
    id: number
    chat_id: number
    chat: string
    verdict: string
    confidence: number
    action: string
    action_label: string
    reason: string
    at: number
    time: string
  }[]
  checks?: {
    result: string
    flags: string
    ip: string
    fp: string
    ua: string
    at: number
    time: string
  }[]
  strong?: { uid: number; mark: string }[]
  weak?: { uid: number; mark: string }[]
}
