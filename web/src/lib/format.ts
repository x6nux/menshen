// 展示格式化函数：从旧内联页（internal/panel/miniapp_html.go）的 JS 等价迁移。
// 行为保持一致，仅 fmtTS 增加了可选的展示时区（对应全局设置 tz_name）。
import type { State } from '../api/types'

/** 设置值从 JSON 来，可能是字符串或数字；统一用宽松类型接住。 */
export type SettingValue = string | number | null | undefined

export type BotSettingsMap = Record<string, Record<string, SettingValue>>
export type SettingsMap = Record<string, SettingValue>

/**
 * displayTz 取全站展示时区：优先顶层 tz_name（主/次管都有）；回退 global
 * 是为了兼容旧响应（顶层字段上线前只给主管理员下发过 global）。
 */
export function displayTz(state: Pick<State, 'tz_name' | 'global'>): string | undefined {
  return state.tz_name ?? state.global?.tz_name
}

const FORMAT_LOCALE = 'en-CA'

// JS Date 的上限（Unix 秒）：8.64e15 毫秒。超过它会得到 Invalid Date，
// Intl.formatToParts 直接抛 RangeError（脏数据、字段错位都可能触发）。
const MAX_UNIX_SECONDS = 8.64e12

// 同一时区反复格式化很多行，Intl 实例建一次就够；非法时区缓存的是本地
// 时区的实例，与「非法回退本地」的行为一致。
const formatterCache = new Map<string, Intl.DateTimeFormat>()

function formatterFor(tzName?: string): Intl.DateTimeFormat {
  const key = tzName ?? ''
  const cached = formatterCache.get(key)
  if (cached) return cached
  const options: Intl.DateTimeFormatOptions = {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    // 固定 24 小时制（h23），避免个别本地化的午夜显示成 24:xx。
    hourCycle: 'h23',
  }
  let fmt: Intl.DateTimeFormat
  if (tzName) {
    try {
      fmt = new Intl.DateTimeFormat(FORMAT_LOCALE, { ...options, timeZone: tzName })
    } catch {
      // 非法 IANA 名（后端已校验，这里兜底）回退浏览器本地时区，不抛错。
      fmt = new Intl.DateTimeFormat(FORMAT_LOCALE, options)
    }
  } else {
    fmt = new Intl.DateTimeFormat(FORMAT_LOCALE, options)
  }
  formatterCache.set(key, fmt)
  return fmt
}

/** fmtTS 把 Unix 秒格式化成「MM-DD HH:mm」；非正数、非法值或超范围值显示 —。 */
export function fmtTS(at: number, tzName?: string): string {
  if (!Number.isFinite(at) || at <= 0 || at > MAX_UNIX_SECONDS) return '—'
  const parts = formatterFor(tzName).formatToParts(new Date(at * 1000))
  const get = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((p) => p.type === type)?.value ?? ''
  return `${get('month')}-${get('day')} ${get('hour')}:${get('minute')}`
}

const VERDICT_LABELS: Record<string, string> = {
  ad: '广告',
  none: '正常',
  clean: '正常',
  error: '失败',
  skipped: '未送检',
}

/** verdictLabel 判定结果文案；未知值原样返回。 */
export function verdictLabel(v: string): string {
  return VERDICT_LABELS[v] || v
}

const ACTION_LABELS: Record<string, string> = {
  none: '无处置',
  muted: '禁言',
  banned: '封禁出群',
  deleted: '删除',
  deleted_muted: '删除+禁言',
  deleted_banned: '删除+封禁',
  undone: '已撤销',
}

/** actionLabel 处置动作文案；dryrun: 前缀渲染成「演练:xxx」。 */
export function actionLabel(a: string): string {
  const known = ACTION_LABELS[a]
  if (known) return known
  return a.startsWith('dryrun:') ? '演练:' + a.slice(7) : a
}

const KIND_LABELS: Record<string, string> = {
  crypto: '加密货币',
  porn: '色情招揽',
  porn_bait: '色情内容',
  gambling: '博彩',
  scam: '诈骗',
  promo: '推广引流',
  spam_flood: '刷屏',
  manual: '人工标记',
  impersonate: '冒用领导人',
  none: '未分类',
}

/** kindLabel 广告分类文案；未知值原样返回。 */
export function kindLabel(k: string): string {
  return KIND_LABELS[k] || k
}

const APPEAL_STATUS_LABELS: Record<string, string> = {
  statement: '待写理由',
  ai: 'AI 复核中',
  web: '等网页验证',
  noweb: '待人工处理',
  code: '已发解禁码',
  redeemed: '已兑换',
  lifted: '已解除',
  rejected: '已驳回',
  expired: '已过期',
}

/** apStatus 申诉状态文案；未知值原样返回。 */
export function apStatus(s: string): string {
  return APPEAL_STATUS_LABELS[s] || s
}

const APPEAL_AI_LABELS: Record<string, string> = {
  uphold: '维持原判',
  overturn: '撤销原判',
  error: '复核出错',
  skipped: '跳过复核',
}

/** apAI AI 复核结论文案；未知值显示「未复核」。 */
export function apAI(r: string): string {
  return APPEAL_AI_LABELS[r] || '未复核'
}

/** durationText 把分钟数渲染成「N 天 / N 小时 / N 分钟」（不做正负判断）。 */
export function durationText(n: number): string {
  if (n % 1440 === 0) return n / 1440 + ' 天'
  if (n % 60 === 0) return n / 60 + ' 小时'
  return n + ' 分钟'
}

/** muteText 禁言时长人话：0/非正数/非数字 = 永久禁言；能整除的按天、小时。 */
export function muteText(v: number | string): string {
  const n = parseInt(String(v), 10)
  if (!Number.isFinite(n) || n <= 0) return '永久禁言'
  return '禁言 ' + durationText(n)
}

/**
 * settingOf 取某个 bot 的实际生效值：bot 覆盖优先（非 null 且非空串），
 * 否则读全局默认；两处都没有返回空串。
 */
export function settingOf(
  botSettings: BotSettingsMap | null | undefined,
  globalDefaults: SettingsMap | null | undefined,
  botId: number | string,
  key: string,
): string {
  const overrides = botSettings?.[String(botId)]
  const v = overrides?.[key]
  if (v != null && v !== '') return String(v)
  const d = globalDefaults?.[key]
  return d != null ? String(d) : ''
}

function muteOf(
  botSettings: BotSettingsMap | null | undefined,
  globalDefaults: SettingsMap | null | undefined,
  botId: number | string,
): string {
  // 未配置时与旧版一致按 1 天兜底展示。
  return muteText(settingOf(botSettings, globalDefaults, botId, 'antiad_mute_minutes') || '1440')
}

/**
 * punishLabel 把「跟随/禁言/封禁」渲染成实际会执行的动作。
 * punish: -1 跟随 bot（看 antiad_ban）、0 禁言、1 封禁出群。
 */
export function punishLabel(
  botSettings: BotSettingsMap | null | undefined,
  globalDefaults: SettingsMap | null | undefined,
  botId: number | string,
  punish: number,
): string {
  if (punish === 1) return '封禁出群（永久）'
  if (punish === 0) return muteOf(botSettings, globalDefaults, botId)
  return settingOf(botSettings, globalDefaults, botId, 'antiad_ban') === '1'
    ? '封禁出群（永久）'
    : muteOf(botSettings, globalDefaults, botId)
}

/** muteOptLabel 当前禁言档位文案（处罚方式选择器里的「禁言 N 天」）。 */
export function muteOptLabel(
  botSettings: BotSettingsMap | null | undefined,
  globalDefaults: SettingsMap | null | undefined,
  botId: number | string,
): string {
  return muteOf(botSettings, globalDefaults, botId)
}

/** meLabel 渲染管理员身份：优先 @username，没有才回退 uid。 */
export function meLabel(me: { uid: number; username?: string }): string {
  return me.username ? `@${me.username}` : `uid ${me.uid}`
}
