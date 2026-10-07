// 列表行状态徽标的文案与色调（机器人/群组/记录/申诉共用，避免各页各写一套）。
import type { Bot, Chat } from '../api/types'
import { apAI, apStatus, logLevelLabel, verdictLabel } from './format'

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

/** verdictInfo：判定徽标。命中（ad）红，其余（正常/未送检/失败）绿。 */
export function verdictInfo(verdict: string): StatusInfo {
  return { label: verdictLabel(verdict), tone: verdict === 'ad' ? 'no' : 'ok' }
}

/** appealStatusInfo：申诉状态徽标。已解除/已兑换绿、已驳回红，处理中中性。 */
export function appealStatusInfo(status: string): StatusInfo {
  if (status === 'lifted' || status === 'redeemed') return { label: apStatus(status), tone: 'ok' }
  if (status === 'rejected') return { label: apStatus(status), tone: 'no' }
  return { label: apStatus(status), tone: 'neutral' }
}

/** appealSummary：列表行的 AI 结论摘要，由结论与截断到 40 字的理由拼接。 */
export function appealSummary(aiResult: string, aiReason: string): string {
  const head = apAI(aiResult)
  return aiReason ? `${head} · ${aiReason.slice(0, 40)}` : head
}

/**
 * logLevelInfo：运行日志级别徽标。错误红、警告黄、信息绿、调试中性。
 */
export function logLevelInfo(level: string): StatusInfo {
  const label = logLevelLabel(level)
  switch (level) {
    case 'ERROR':
      return { label, tone: 'no' }
    case 'WARN':
      return { label, tone: 'warn' }
    case 'INFO':
      return { label, tone: 'ok' }
    default:
      return { label, tone: 'neutral' }
  }
}
