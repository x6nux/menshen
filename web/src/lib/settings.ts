// 设置项（state.specs/sections）的纯逻辑：控件类型推断、时长预设、覆盖键集合。
// 规则见实施计划 3.5；这里不依赖任何 React 组件。
import type { Spec } from '../api/types'
import { durationText } from './format'

/**
 * CAPTCHA_DEMO_PATH 是人机验证测试台（_w/demo）的固定路径：
 * 相对同源，网页版 /admin 与 Mini App 里打开都指向本部署。
 */
export const CAPTCHA_DEMO_PATH = '/_w/demo/1/x'

/**
 * APPEAL_DEMO_PATH 是申诉验证测试台（_w/apdemo）的固定路径：
 * 校验走申诉页同一条 Turnstile 路径，用于排查申诉验证不通过。
 */
export const APPEAL_DEMO_PATH = '/_w/apdemo/1/x'

export type ControlKind = 'toggle' | 'number' | 'text' | 'select'

/**
 * controlKind 推断控件：后端下发的 kind 优先（字符串型设置是 text），
 * 缺省按 min/max 推断。时区、模型列表等特殊卡不在 specs 里，由页面单独渲染。
 */
export function controlKind(spec: Spec): ControlKind {
  if (spec.kind === 'text') return 'text'
  if (spec.kind === 'toggle') return 'toggle'
  if (spec.min === 0 && spec.max === 1) return 'toggle'
  return 'number'
}

export interface MinutePreset {
  label: string
  value: number
}

/** MINUTE_PRESETS 时长类常用档位；是否可用由 spec.min/max 过滤决定。 */
export const MINUTE_PRESETS: readonly MinutePreset[] = [
  { label: '永久', value: 0 },
  { label: '10 分钟', value: 10 },
  { label: '1 小时', value: 60 },
  { label: '1 天', value: 1440 },
  { label: '7 天', value: 10080 },
]

/**
 * minutePresets 返回适用于该 spec 的时长档位。
 * 必须按 min/max 过滤：antiad_hedge_minutes 等是 min=1,max=1440，
 * 提交越界档位（0=永久、7 天）后端会回 400。
 */
export function minutePresets(spec: Spec): MinutePreset[] {
  const max = spec.max > 0 ? spec.max : Number.POSITIVE_INFINITY
  return MINUTE_PRESETS.filter((p) => p.value >= spec.min && p.value <= max).map((p) => ({ ...p }))
}

/**
 * overrideKeys 从 specs 里挑出该 bot 已显式覆盖的项（bot 参数页的「已覆盖」视图）。
 * 条件：分组是 antiad/both 且键存在于覆盖表。这样 antiad_exempt_users、
 * antiad_alert_last_id 这类不在 specs 的键不会漏出来（走 set 必然 400）。
 */
export function overrideKeys(
  specs: readonly Spec[],
  overrides: Record<string, unknown> | null | undefined,
): Spec[] {
  if (!overrides) return []
  return specs.filter(
    (sp) =>
      (sp.group === 'antiad' || sp.group === 'both') &&
      Object.hasOwn(overrides, sp.key) &&
      overrides[sp.key] != null &&
      overrides[sp.key] !== '',
  )
}

/** specUnit 从键名推断数值单位，用于输入后缀与换算文案。 */
export function specUnit(spec: Spec): string {
  const k = spec.key
  if (k.endsWith('_minutes')) return '分钟'
  if (k.endsWith('_ms')) return '毫秒'
  if (k.endsWith('_hours')) return '小时'
  if (k.endsWith('_days')) return '天'
  if (k.endsWith('_seconds') || k.endsWith('_ttl') || k.endsWith('_base')) return '秒'
  if (k.endsWith('_rpm')) return '次/分钟'
  return ''
}

/** specHint 设置项说明；后端 hint 没写单位时补上，表单不必每处手写。
 * T1b 的设置表单会用它做输入框辅助文案（当前 T1a 暂无调用点）。 */
export function specHint(spec: Spec): string {
  const unit = specUnit(spec)
  return unit && !spec.hint.includes(unit) ? `${spec.hint}（单位：${unit}）` : spec.hint
}

/**
 * specValueText 把设置值换算成人话：时长类把分钟数换算成「= 1 天」这类文案，
 * 其余类型原样返回。T1b 的设置行/抽屉用它展示「= 1 天」换算（当前暂无调用点）。
 */
export function specValueText(value: number | string, spec: Spec): string {
  const raw = String(value)
  if (specUnit(spec) !== '分钟') return raw
  const n = parseInt(raw, 10)
  if (!Number.isFinite(n)) return raw
  if (n <= 0) return `${n} 分钟（永久）`
  const pretty = durationText(n)
  return pretty === `${n} 分钟` ? raw : `${n} 分钟（= ${pretty}）`
}

/**
 * validateSpecValue 校验参数覆盖抽屉里的输入；返回错误文案，合法返回 null。
 * 空串表示「跟随全局/恢复全局」，是合法提交；范围与后端 miniSet 一致：
 * n < min 或（max != 0 且 n > max）都会被服务端 400，这里提前拦住。
 */
export function validateSpecValue(spec: Spec, raw: string): string | null {
  const val = raw.trim()
  if (val === '') return null
  if (!/^\d+$/.test(val)) return '请输入非负整数'
  const n = parseInt(val, 10)
  if (n < spec.min) return `不能小于 ${spec.min}`
  if (spec.max !== 0 && n > spec.max) return `不能大于 ${spec.max}`
  return null
}
