// Mini App API 的响应类型：按 internal/panel/miniapp.go 各 op 的实际 JSON
// 字段手写（state/logs/log/user/appeals/appeal 与写操作返回）。字段名与
// 后端保持一致（snake_case），不做前端改名。

// ---- 基础 ----

export interface Me {
  uid: number
  main: boolean
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
  supports_chat: boolean
  supports_systemone: boolean
}

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

// ---- 写操作 ----

/** OkResp 是大多数写操作的返回；note 是服务端给用户看的提示文案。 */
export interface OkResp {
  ok: boolean
  note?: string
}
