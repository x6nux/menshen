// /miniapp/api/* 的 fetch 封装：注入 Telegram 鉴权头、解包 {error}、
// 按 HTTP 状态抛 ApiError（网络失败 status=0），错误交给 UI 层分流。
import type { TelegramBridge } from '../telegram'

export class ApiError extends Error {
  /** HTTP 状态码；0 表示网络层失败（断网/被墙），不是服务端返回的。 */
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export interface ApiAuth {
  initData: string
  botId: string
}

let auth: ApiAuth = { initData: '', botId: '0' }

/** setApiAuth 注入请求头；start 时 telegram 桥未就绪就传空串与 '0'。 */
export function setApiAuth(next: Partial<ApiAuth>): void {
  auth = { ...auth, ...next }
}

/** setApiBridge 从 Telegram 桥取鉴权信息（main/外壳在 initTelegram 后调用）。 */
export function setApiBridge(bridge: TelegramBridge | null): void {
  setApiAuth({ initData: bridge?.initData ?? '', botId: bridge?.botId ?? '0' })
}

/** apiURL 拼出请求地址：浏览器相对地址即可；vitest（Node fetch）不接受
 * 相对 URL，这里按当前 origin 换算成绝对地址，语义等价。 */
function apiURL(op: string): string {
  const path = `/miniapp/api/${encodeURIComponent(op)}`
  const origin = typeof location !== 'undefined' ? location.origin : ''
  if (!origin || origin === 'null') return path
  return origin + path
}

function errorMessage(data: unknown): string | null {
  if (data && typeof data === 'object' && 'error' in data) {
    const err = (data as { error?: unknown }).error
    if (typeof err === 'string' && err) return err
  }
  return null
}

/** api 调用一个 op；非 2xx 抛 ApiError，body 的 error 文案优先。
 * 网络失败与主动 abort 统一是 status 0；2xx 但响应不是 JSON 时抛
 * ApiError(status, '响应格式错误')。signal 原样透传给 fetch。 */
export async function api<T>(op: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let res: Response
  try {
    res = await fetch(apiURL(op), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Tg-Init-Data': auth.initData,
        'X-Bot-Id': auth.botId,
      },
      body: JSON.stringify(body ?? {}),
      signal,
    })
  } catch (err) {
    throw new ApiError(0, err instanceof Error ? err.message : '网络异常')
  }

  let data: unknown = null
  let parsed = true
  try {
    data = await res.json()
  } catch {
    parsed = false
  }
  if (!res.ok) {
    // 错误响应体不是 JSON 时用 HTTP 状态兜底。
    throw new ApiError(res.status, errorMessage(data) ?? `HTTP ${res.status}`)
  }
  if (!parsed) throw new ApiError(res.status, '响应格式错误')
  return data as T
}
