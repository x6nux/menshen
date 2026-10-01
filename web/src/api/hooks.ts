// 查询 hooks（TanStack Query）。queryKey 统一以资源名开头，写操作按前缀失效：
// ['state'] / ['logs'] / ['log'] / ['user'] / ['appeals'] / ['appeal']。
//
// 契约：所有读 hook 都带 enabled（默认 true）。页面必须在 Telegram bridge 就绪、
// 拿到 initData 之后再传 enabled: true —— 否则请求会带着空 initData 发出，
// 服务端回 401，UI 会显示误导性的「身份失效」。外壳可以在 initTelegram()
// resolve 前先用 enabled: false 挂载页面。
import { useQuery } from '@tanstack/react-query'
import { api } from './client'
import type {
  AppealDetail,
  AppealsResp,
  LogDetail,
  LogsResp,
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
}

/** 记录筛选归一化：'' 与 'all' 都表示不筛，统一成 'all' 共用 queryKey/请求。 */
function normalizeLogFilter(filter?: string): string {
  return !filter || filter === 'all' ? 'all' : filter
}

/** 申诉筛选归一化：''/'all' 都表示全部，其余（默认 'open'）统一成 'open'。 */
function normalizeAppealFilter(filter: string): 'open' | 'all' {
  return filter === '' || filter === 'all' ? 'all' : 'open'
}

/** 用户页筛选归一化：''/'act' 都是只看被处置过的，其余按 all。 */
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
