// 列表行状态徽标的文案与色调（机器人/群组两处共用，避免各页各写一套）。
import type { Bot, Chat } from '../api/types'

/** StatusTone 与 ui/Badge 的 BadgeTone 结构一致，避免 lib 依赖组件层。 */
export type StatusTone = 'ok' | 'no' | 'warn'

export interface StatusInfo {
  label: string
  tone: StatusTone
}

/** botStatus：已停用 / 运行中（enabled 且注册表在线）/ 未运行（enabled 但不在线）。 */
export function botStatus(b: Pick<Bot, 'enabled' | 'live'>): StatusInfo {
  if (!b.enabled) return { label: '已停用', tone: 'no' }
  return b.live ? { label: '运行中', tone: 'ok' } : { label: '未运行', tone: 'warn' }
}

/** chatStatus：演练 / 判定中 / 停用；演练优先于启用状态展示。 */
export function chatStatus(c: Pick<Chat, 'enabled' | 'dryrun'>): StatusInfo {
  if (c.dryrun) return { label: '演练', tone: 'warn' }
  return c.enabled ? { label: '判定中', tone: 'ok' } : { label: '停用', tone: 'no' }
}
