// Mini App API 的响应类型：按 internal/panel/miniapp.go 各 op 的实际 JSON
// 字段手写（state/logs/log/user/appeals/appeal 与写操作返回）。字段名与
// 后端保持一致（snake_case），不做前端改名。

// ---- 基础 ----

export interface Me {
  uid: number
  main: boolean
  /** 来自 initData 的 @username；没设用户名时为空串，界面回退成 uid。 */
  username?: string
}

export interface Bot {
  bot_id: number
  username: string
  label: string
  owner_id: number
  enabled: boolean
  is_main: boolean
  so_models: string[]
  llm_models: string[]
  live: boolean
}

export interface Chat {
  bot_id: number
  chat_id: number
  title: string
  enabled: boolean
  dryrun: boolean
  group_alert: boolean
  punish: number
}

export interface Spec {
  key: string
  label: string
  hint: string
  min: number
  max: number
  group: string
}

export interface SpecSection {
  name: string
  specs: Spec[]
}

export interface Stats {
  checked: number
  hits: number
  cost: number
  cost_text: string
  chats: number
}

export interface Todo {
  open_appeals: number
  dryrun_chats: number
  disabled_bots: number
}

export interface GbanRow {
  user_id: number
  reason: string
  src_chat: number
  created_at: number
}

export interface GbanOwn {
  enabled: boolean
  chats: number[]
  bans: GbanRow[]
}

export interface WhiteRow {
  bot_id: number
  chat_id: number
  user_id: number
  expires_at: number
  source: string
  by_uid: number
}

export interface ProfileOKRow {
  bot_id: number
  user_id: number
  hours: number
  reason: string
  created_at: number
  expires_at: number
}

export interface Upstream {
  id: number
  name: string
  base_url: string
  /** 后端下发的是掩码后的 key（upstream.MaskKey），不是明文。 */
  api_key: string
  weight: number
  status: boolean
  /** 渠道类型：决定端点路径、鉴权与请求/响应协议。 */
  kind: UpstreamKind
  supports_chat: boolean
  supports_systemone: boolean
}

export type UpstreamKind =
  | 'openai'
  | 'openai-responses'
  | 'anthropic'
  | 'gemini'
  | 'cloudflare'

export interface Model {
  name: string
  enabled: boolean
  prompt_price: number
  completion_price: number
  cache_read_price: number
  cache_write_price: number
  /** 由模型名解析出的上游展示名；旧格式模型可能为空串。 */
  upstream: string
  model_id: string
}

/**
 * UpstreamTestResp 是 upstream {action:'test'} 的结果：请求本身失败（网络、
 * 鉴权、上游 5xx）时 HTTP 仍是 200，但 ok=false、error 是可读原因；
 * 配置类问题（没有可用模型等）走 400，由 client 抛 ApiError。
 */
export interface UpstreamTestResp {
  ok: boolean
  latency_ms?: number
  /** <上游名>/<模型ID>，成功时返回。 */
  model?: string
  error?: string
}

export interface AdminRow {
  user_id: number
  note: string
}

export interface OwnerOpt {
  user_id: number
  label: string
}

/**
 * State 是 state op 的响应。global/digest/digest_fix/upstreams/models/
 * admins/owner_opts 仅主管理员下发，所以标成可选。
 */
export interface State {
  me: Me
  bots: Bot[]
  chats: Chat[]
  bot_settings: Record<string, Record<string, string>>
  specs: Spec[]
  sections: SpecSection[]
  stats: Stats
  todo: Todo
  global_defaults: Record<string, string>
  global?: Record<string, string>
  /** tz_name 是展示时区（IANA），所有管理员都会下发（次管没有 global）。 */
  tz_name?: string
  /**
   * settings_set 是显式写过的设置键（仅主管理员下发）。快照的 global 里铺了
   * 代码默认值，判断「已设置」只能看这个集合。
   */
  settings_set?: string[]
  digest?: string
  digest_fix?: string
  whitelist: WhiteRow[]
  profile_ok: ProfileOKRow[]
  gban: GbanRow[]
  gban_own: GbanOwn
  upstreams?: Upstream[]
  models?: Model[]
  admins?: AdminRow[]
  owner_opts?: OwnerOpt[]
}

// ---- 记录 ----

/** LogRow 是 logs 列表行与 log 详情共用的字段。 */
export interface LogRow {
  id: number
  bot_id: number
  chat_id: number
  user_id: number
  message_id: number
  verdict: string
  confidence: number
  kind: string
  action: string
  reason: string
  text: string
  decider: string
  cost_text: string
  created_at: number
  view_url: string
}

/** LogDetail 与列表行同字段；详情接口在正文为空时会回查留底。 */
export type LogDetail = LogRow

export interface LogsResp {
  logs: LogRow[]
  page: number
  total: number
}

// ---- 用户页 ----

export interface UserLogRow {
  id: number
  chat_id: number
  verdict: string
  confidence: number
  kind: string
  action: string
  reason: string
  text: string
  created_at: number
}

export interface UserDossier {
  user_id: number
  name: string
  username: string
  bio: string
  kept: number
  msgs: number
  hits: number
  chats: number
  first_seen: number
  last_msg: number
  /** total 是该用户全部记录数；processed 是被处置过的；shown 是当前筛选下的总数。 */
  total: number
  processed: number
  shown: number
  page: number
  filter: 'act' | 'all'
  logs: UserLogRow[]
}

// ---- 申诉 ----

/** AppealRow 是 appeals 列表行（statement/ai_reason 已被后端截到 300 字）。 */
export interface AppealRow {
  id: number
  bot_id: number
  user_id: number
  status: string
  statement: string
  ai_result: string
  ai_conf: number
  ai_reason: string
  ai_model: string
  web_attempts: number
  has_code: boolean
  code_expires: number
  created_at: number
  updated_at: number
  detail_url: string
}

export interface AppealRedeem {
  chat_id: number
  by_uid: number
  at: number
}

/** AppealDetail 是 appeal op 的完整响应：含解禁码、兑换与网页验证记录。 */
export interface AppealDetail extends AppealRow {
  code: string
  ai_cost: string
  web_checks: number
  web_passes: number
  redeems: AppealRedeem[]
}

export interface AppealsResp {
  appeals: AppealRow[]
  page: number
  total: number
}

// ---- AI 必封规则（主管理员专属）----

/**
 * Rule 是一条必封规则。enforce=true 命中即最高档处置；false 只把命中作为
 * 证据注入判定 prompt。last_tp/last_fp/last_undone/last_scanned 是最近一轮
 * 全库测试写回的结果；last_tested_at=0 表示从未测试过（与 last_fp=0
 *「测过且干净」不是一回事，所以强制开关的门要先看 last_tested_at）。
 */
export interface Rule {
  id: number
  name: string
  pattern: string
  category: string
  note: string
  /** 来源：新建默认 'ai'（AI 发现/手动新增在后端都记这篇）。 */
  source: string
  enabled: boolean
  enforce: boolean
  hits: number
  last_matched: number
  last_tp: number
  last_fp: number
  last_undone: number
  last_scanned: number
  last_tested_at: number
  /** 最近测试的覆盖率分母（已确认广告总数）；0 = 没测过/无广告。 */
  last_ads_total: number
  /** 最近测试的按类型细分（落库数据，无 coverage 字段，前端现算）。 */
  last_kinds: RuleKindStat[]
  created_at: number
}

/** RuleKindStat 是按 ad_kind 细分的覆盖率。 */
export interface RuleKindStat {
  kind: string
  total: number
  matched: number
  /** 工具/测试响应里带；落库列表数据没有，前端按 matched/total 现算。 */
  coverage?: number
}

export interface RulesResp {
  rules: Rule[]
}

/** RuleSample 是全库测试里的一条命中样本（正文已截断到 200 字）。 */
export interface RuleSample {
  id: number
  verdict: string
  action: string
  kind: string
  user_id: number
  chat_id: number
  created_at: number
  text: string
}

/**
 * RuleTest 是一条规则跑全库测试的结果。分类口径：
 * TP=verdict 'ad'；FP=verdict 'clean'/'none' 或 action 'undone'；
 * Undone=action 'undone'（同时计入 FP）；Neutral=verdict 'skipped'/'error'。
 */
export interface RuleTest {
  pattern: string
  scanned: number
  matched: number
  tp: number
  fp: number
  undone: number
  neutral: number
  /** 扫描窗口内已确认广告总数（覆盖率分母）。 */
  ads_total: number
  /** 总体覆盖率（0-1）；ads_total=0 时为 0。 */
  coverage: number
  /** 按 ad_kind 的覆盖率细分。 */
  kinds: RuleKindStat[]
  tp_samples: RuleSample[]
  fp_samples: RuleSample[]
  undone_samples: RuleSample[]
}

export interface RuleAgent {
  running: boolean
  started_at: number
  finished_at: number
  result: string
  error: string
  /** 本轮第一条创建的规则（旧字段，兼容保留）。 */
  created_rule_id: number
  /** 本轮创建的全部规则 id；无创建时是空数组。 */
  created_rule_ids: number[]
  /** 已执行的步骤数；步骤内容不再下发（前端只显示运行状态）。 */
  steps_count: number
  /** 本轮重点针对的判定记录 id（指定记录模式）；0 = 全库模式。 */
  target_log_id: number
}

export interface RuleAgentStatusResp {
  agent: RuleAgent
}

/** RuleAgentStartResp 是 agent_start 的返回：带回刚启动时的初始运行态。 */
export interface RuleAgentStartResp {
  ok: boolean
  agent: RuleAgent
}

/** RuleSaveResp 是 save 的返回：新 id 与保存时自动跑的那轮全库测试。 */
export interface RuleSaveResp {
  ok: boolean
  id: number
  test: RuleTest
}

// ---- 运行日志（主管理员专属）----

/** SysLogLevel 是 slog 的四档级别；后端还有未知级别时按字符串兜底。 */
export type SysLogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

/** SysLogAttr 是一条日志的结构化字段，v 已由后端渲染成字符串。 */
export interface SysLogAttr {
  k: string
  v: string
}

/** SysLogRow 是运行日志的一行；seq 单调递增，供列表 key 用。 */
export interface SysLogRow {
  seq: number
  at: number
  level: SysLogLevel | string
  message: string
  attrs: SysLogAttr[]
}

/** SysLogCounts 是搜索命中里的各档条数（不受级别筛选影响）。 */
export interface SysLogCounts {
  debug: number
  info: number
  warn: number
  error: number
}

export interface SysLogsResp {
  logs: SysLogRow[]
  page: number
  total: number
  counts: SysLogCounts
}

// ---- 写操作 ----

/** OkResp 是大多数写操作的返回；note 是服务端给用户看的提示文案。 */
export interface OkResp {
  ok: boolean
  note?: string
}
