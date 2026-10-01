// 查询 hooks（TanStack Query）。queryKey 统一以资源名开头，写操作按前缀失效：
// ['state'] / ['logs'] / ['log'] / ['user'] / ['appeals'] / ['appeal']。
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

/** useMiniState 拉取全量状态（概览/机器人/群组/名单的公共数据源）。 */
export function useMiniState() {
  return useQuery({
    queryKey: miniQueryKeys.state,
    queryFn: () => api<State>('state'),
  })
}

export interface LogsParams {
  /** deleted（默认）/ all / ad / clean / skipped，对应后端 verdict。 */
  filter?: string
  q?: string
  page?: number
}

export function useLogs(params: LogsParams = {}) {
  const filter = params.filter ?? 'deleted'
  const q = params.q ?? ''
  const page = params.page ?? 1
  return useQuery({
    queryKey: miniQueryKeys.logs(filter, q, page),
    queryFn: () =>
      api<LogsResp>('logs', { verdict: filter === 'all' ? '' : filter, q, page }),
  })
}

export function useLog(id: number, enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.log(id),
    queryFn: () => api<LogDetail>('log', { id }),
    enabled: enabled && id > 0,
  })
}

export function useUser(id: number, filter = 'act', page = 1) {
  return useQuery({
    queryKey: miniQueryKeys.user(id, filter, page),
    queryFn: () => api<UserDossier>('user', { user_id: id, filter, page }),
    enabled: id > 0,
  })
}

export interface AppealsParams {
  /** open（默认）/ ''（全部）。 */
  filter?: string
  page?: number
  enabled?: boolean
}

export function useAppeals(params: AppealsParams = {}) {
  const filter = params.filter ?? 'open'
  const page = params.page ?? 1
  return useQuery({
    queryKey: miniQueryKeys.appeals(filter, page),
    queryFn: () => api<AppealsResp>('appeals', { filter, page }),
    enabled: params.enabled ?? true,
  })
}

export function useAppeal(id: number, enabled = true) {
  return useQuery({
    queryKey: miniQueryKeys.appeal(id),
    queryFn: () => api<AppealDetail>('appeal', { id }),
    enabled: enabled && id > 0,
  })
}
