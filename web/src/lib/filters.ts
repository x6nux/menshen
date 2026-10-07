// 名单类列表的本地过滤与各类行的搜索键。
import type { GbanRow, ProfileOKRow, WhiteRow } from '../api/types'

/** matchQuery 大小写不敏感的包含匹配；查询为空（或全空白）返回 true。 */
export function matchQuery(key: string, q: string): boolean {
  const query = q.trim().toLowerCase()
  if (!query) return true
  return key.toLowerCase().includes(query)
}

/** filterRows 本地过滤：空查询返回全部，keyOf 负责拼出可搜索的字段串。 */
export function filterRows<T>(items: readonly T[], q: string, keyOf: (item: T) => string): T[] {
  return items.filter((item) => matchQuery(String(keyOf(item) ?? ''), q))
}

/** gbanKey 联合封禁行的搜索键：uid + 原因。 */
export function gbanKey(g: Pick<GbanRow, 'user_id' | 'reason'>): string {
  return `${g.user_id} ${g.reason || ''}`
}

/** whiteKey 白名单行的搜索键：uid + 来源 + bot + 群。 */
export function whiteKey(w: Pick<WhiteRow, 'user_id' | 'source' | 'bot_id' | 'chat_id'>): string {
  return `${w.user_id} ${w.source || ''} ${w.bot_id} ${w.chat_id}`
}

/** profileOKKey 资料放行行的搜索键：uid + bot 展示名 + 原因。 */
export function profileOKKey(
  p: Pick<ProfileOKRow, 'user_id' | 'bot_id' | 'reason'>,
  botLabel: (botId: number) => string,
): string {
  return `${p.user_id} ${botLabel(p.bot_id)} ${p.reason || ''}`
}
