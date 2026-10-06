// 上游渠道类型的展示名与默认能力开关：被列表页与详情页共用，抽成模块避免
// 「页面文件同时导出组件与常量」的 fast-refresh 警告。
import type { UpstreamKind } from '../../api/types'

export const KINDS: { value: UpstreamKind; label: string }[] = [
  { value: 'openai', label: 'OpenAI Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'cloudflare', label: 'Cloudflare' },
]

/** kindCaps 渠道类型决定的默认能力开关（与后端约束一致）。 */
export function kindCaps(kind: UpstreamKind): { chat: boolean; systemone: boolean } {
  if (kind === 'cloudflare') return { chat: false, systemone: true }
  if (kind === 'openai-responses' || kind === 'anthropic' || kind === 'gemini') {
    return { chat: true, systemone: false }
  }
  return { chat: true, systemone: false }
}
