// 列表行状态徽标的文案与色调（机器人/群组/记录/申诉共用，避免各页各写一套）。
import type { Bot, Chat } from '../api/types'
import { apAI, apStatus, verdictLabel } from './format'

/** StatusTone 与 ui/Badge 的 BadgeTone 结构一致，避免 lib 依赖组件层。 */
export type StatusTone = 'ok' | 'no' | 'warn' | 'neutral'

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

/** verdictInfo：判定徽标。命中（ad）红，其余（正常/未送检/失败）绿（沿用旧页口径）。 */
export function verdictInfo(verdict: string): StatusInfo {
  return { label: verdictLabel(verdict), tone: verdict === 'ad' ? 'no' : 'ok' }
}

/** appealStatusInfo：申诉状态徽标。已解除/已兑换绿、已驳回红，处理中中性。 */
export function appealStatusInfo(status: string): StatusInfo {
  if (status === 'lifted' || status === 'redeemed') return { label: apStatus(status), tone: 'ok' }
  if (status === 'rejected') return { label: apStatus(status), tone: 'no' }
  return { label: apStatus(status), tone: 'neutral' }
}

/** appealSummary：列表行的 AI 结论摘要（结论 + 截断的理由），与旧页 40 字口径一致。 */
export function appealSummary(aiResult: string, aiReason: string): string {
  const head = apAI(aiResult)
  return aiReason ? `${head} · ${aiReason.slice(0, 40)}` : head
}
