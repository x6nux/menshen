// 查询 hooks（TanStack Query）。queryKey 统一以资源名开头，写操作按前缀失效：
// ['state'] / ['logs'] / ['log'] / ['user'] / ['appeals'] / ['appeal']。
//
// 契约：所有读 hook 都带 enabled（默认 true）。页面必须在 Telegram bridge 就绪、
// 拿到 initData 之后再传 enabled: true —— 否则请求会带着空 initData 发出，
// 服务端回 401，UI 会显示误导性的「身份失效」。外壳可以在 initTelegram()
// resolve 前先用 enabled: false 挂载页面。
import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api } from './client'
import type {
  AppealDetail,
  AppealsResp,
  LogDetail,
  LogsResp,
  RuleAgentStatusResp,
  RulesResp,
  State,
  UserDossier,
} from './types'

export const miniQueryKeys = {
  state: ['state'] as const,
  logs: (filter: string, q: string, page: number) => ['logs', filter, q, page] as const,
  log: (id: number) => ['log', id] as const,
  user: (id: number, filter: string, page: number) => ['user', id, filter, page] as const,
  appeals: (filter: string, page: number) => ['appeals', filter, page] as const,
  appeal: (id: number) => ['appeal', id] as const,
  // 规则列表与 Agent 状态分开 key：写规则只需失效 'list'，不必连带重拉运行态。
  rules: ['rules', 'list'] as const,
  rulesAgent: ['rules', 'agent'] as const,
  // 无限列表用独立 key（数据形状是 pages，不能与单页查询共 key）；
  // 前缀仍以资源名开头，写操作的 ['logs']/['user']/['appeals'] 失效照常命中。
  logsInfinite: (filter: string, q: string) => ['logs', 'infinite', filter, q] as const,
  userInfinite: (id: number, filter: string) => ['user', 'infinite', id, filter] as const,
  appealsInfinite: (filter: string) => ['appeals', 'infinite', filter] as const,
}

/**
 * 记录筛选归一化：不传（undefined）= 默认「已删除」（旧页 LOGF='deleted'）；
 * ''/'all' = 全部，统一成 'all' 共用 queryKey；其余原样透传。
 */
function normalizeLogFilter(filter?: string): string {
  if (filter === undefined) return 'deleted'
  return filter === '' || filter === 'all' ? 'all' : filter
}

/** 申诉筛选归一化：''/'all' 表示全部；非法/未知值按默认档 'open' 兜底。 */
function normalizeAppealFilter(filter: string): 'open' | 'all' {
  return filter === '' || filter === 'all' ? 'all' : 'open'
}

/** 用户页筛选归一化：'all' 是全部，其余（含 ''/未知值）按默认档 'act' 兜底。 */
function normalizeUserFilter(filter: string): 'act' | 'all' {
  return filter === 'all' ? 'all' : 'act'
}

/** useMiniState 拉取全量状态（概览/机器人/群组/名单的公共数据源）。 */
export function useMiniState(enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.state,
    queryFn: ({ signal }) => api<State>('state', undefined, signal),
    enabled,
  })
}

export interface LogsParams {
  /** deleted（默认）/ all / ad / clean / skipped，对应后端 verdict。 */
  filter?: string
  q?: string
  page?: number
  enabled?: boolean
}

export function useLogs(params: LogsParams = {}) {
  const filter = normalizeLogFilter(params.filter)
  const q = params.q ?? ''
  const page = params.page ?? 1
  return useQuery({
    queryKey: miniQueryKeys.logs(filter, q, page),
    queryFn: ({ signal }) =>
      api<LogsResp>('logs', { verdict: filter === 'all' ? '' : filter, q, page }, signal),
    enabled: params.enabled ?? true,
  })
}

export function useLog(id: number, enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.log(id),
    queryFn: ({ signal }) => api<LogDetail>('log', { id }, signal),
    enabled: enabled && id > 0,
  })
}

export function useUser(id: number, filter = 'act', page = 1, enabled = true) {
  const normalized = normalizeUserFilter(filter)
  return useQuery({
    queryKey: miniQueryKeys.user(id, normalized, page),
    queryFn: ({ signal }) =>
      api<UserDossier>('user', { user_id: id, filter: normalized, page }, signal),
    enabled: enabled && id > 0,
  })
}

export interface AppealsParams {
  /** open（默认）/ '' 或 all（全部）。 */
  filter?: string
  page?: number
  enabled?: boolean
}

export function useAppeals(params: AppealsParams = {}) {
  const filter = normalizeAppealFilter(params.filter ?? 'open')
  const page = params.page ?? 1
  return useQuery({
    queryKey: miniQueryKeys.appeals(filter, page),
    queryFn: ({ signal }) =>
      api<AppealsResp>('appeals', { filter: filter === 'open' ? 'open' : '', page }, signal),
    enabled: params.enabled ?? true,
  })
}

export function useAppeal(id: number, enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.appeal(id),
    queryFn: ({ signal }) => api<AppealDetail>('appeal', { id }, signal),
    enabled: enabled && id > 0,
  })
}

/** RULE_AGENT_POLL_MS 是规则发现 Agent 运行中的状态轮询间隔。 */
export const RULE_AGENT_POLL_MS = 2000

/**
 * useRules 拉取全部必封规则（含未启用）。rules op 只对主管理员开放，
 * 调用方必须在确认 me.main 后才 enabled（次管进页面不发请求）。
 */
export function useRules(enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.rules,
    queryFn: ({ signal }) => api<RulesResp>('rules', { action: 'list' }, signal),
    enabled,
  })
}

/**
 * useRuleAgent 拉取规则发现 Agent 的运行态与步骤日志。运行中每
 * RULE_AGENT_POLL_MS 轮询一次；结束后回调返回 false 自动停表。rules op
 * 只对主管理员开放，同样由调用方按 me.main 门控 enabled。
 */
export function useRuleAgent(enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.rulesAgent,
    queryFn: ({ signal }) =>
      api<RuleAgentStatusResp>('rules', { action: 'agent_status' }, signal),
    enabled,
    refetchInterval: (query) => (query.state.data?.agent.running ? RULE_AGENT_POLL_MS : false),
  })
}

// ---- 无限列表（记录/申诉/用户页）----
//
// 全部用 total/shown 与「已加载条数」比较来决定下一页：
// - logs/appeals 的 total 是同一 WHERE 的总数；
// - user 用 shown（当前筛选下的总数）——用 total 会因筛选偏大而多翻一页。
// 页大小 20 由后端 LIMIT 固定。keepPreviousData 让切换筛选时列表不闪空，
// 并在新一页到达前沿用旧数据（isPlaceholderData 区分）。

export interface InfiniteLogsParams {
  /** deleted（默认）/ all / ad / clean / skipped。 */
  filter?: string
  q?: string
  enabled?: boolean
}

export function useInfiniteLogs(params: InfiniteLogsParams = {}) {
  const filter = normalizeLogFilter(params.filter)
  const q = params.q ?? ''
  return useInfiniteQuery({
    queryKey: miniQueryKeys.logsInfinite(filter, q),
    queryFn: ({ pageParam, signal }) =>
      api<LogsResp>('logs', { verdict: filter === 'all' ? '' : filter, q, page: pageParam }, signal),
    initialPageParam: 1,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, page) => n + page.logs.length, 0)
      return loaded < last.total ? pages.length + 1 : undefined
    },
    enabled: params.enabled ?? true,
    placeholderData: keepPreviousData,
  })
}

export interface InfiniteAppealsParams {
  /** open（默认）/ '' 或 all（全部）。 */
  filter?: string
  enabled?: boolean
}

export function useInfiniteAppeals(params: InfiniteAppealsParams = {}) {
  const filter = normalizeAppealFilter(params.filter ?? 'open')
  return useInfiniteQuery({
    queryKey: miniQueryKeys.appealsInfinite(filter),
    queryFn: ({ pageParam, signal }) =>
      api<AppealsResp>('appeals', { filter: filter === 'open' ? 'open' : '', page: pageParam }, signal),
    initialPageParam: 1,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, page) => n + page.appeals.length, 0)
      return loaded < last.total ? pages.length + 1 : undefined
    },
    enabled: params.enabled ?? true,
    placeholderData: keepPreviousData,
  })
}

export function useInfiniteUserLogs(userId: number, filter = 'act', enabled = true) {
  const normalized = normalizeUserFilter(filter)
  return useInfiniteQuery({
    queryKey: miniQueryKeys.userInfinite(userId, normalized),
    queryFn: ({ pageParam, signal }) =>
      api<UserDossier>('user', { user_id: userId, filter: normalized, page: pageParam }, signal),
    initialPageParam: 1,
    getNextPageParam: (last, pages) => {
      const loaded = pages.reduce((n, page) => n + page.logs.length, 0)
      return loaded < last.shown ? pages.length + 1 : undefined
    },
    enabled: enabled && userId > 0,
    placeholderData: keepPreviousData,
  })
}
