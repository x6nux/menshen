// 写操作 hooks：一个通用 useMiniMutation + 各 op 薄封装。
// 失效规则：一切写操作失效 ['state']；logact 追加 ['log']/['logs']/['user']；
// appealact 追加 ['appeal']/['appeals']。错误不吞：直接冒泡给调用方，
// toast/回滚由 UI 层处理。
//
// 注意：mutation 没有内置 loading 去重——提交按钮必须用 mutation.isPending
// 禁用，否则连点会发多次请求（后端不是幂等的）。
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { QueryKey } from '@tanstack/react-query'
import { api } from './client'
import type { ApiError } from './client'
import type { OkResp, State } from './types'

export type MiniMutationOp =
  | 'set'
  | 'bot'
  | 'chat'
  | 'upstream'
  | 'model'
  | 'admin'
  | 'gban'
  | 'gbanown'
  | 'whitelist'
  | 'digest'
  | 'logact'
  | 'appealact'

export interface MiniMutationOptions {
  /** 除 ['state'] 外还要失效的 queryKey 前缀。 */
  invalidate?: readonly QueryKey[]
}

export function useMiniMutation<TBody = Record<string, unknown>, TResp = OkResp>(
  op: MiniMutationOp,
  options: MiniMutationOptions = {},
) {
  const queryClient = useQueryClient()
  const extraKeys = options.invalidate
  return useMutation<TResp, ApiError, TBody>({
    mutationFn: (body) => api<TResp>(op, body),
    onSuccess: () => {
      // 写操作都会影响 state（概览数字、列表、明细的公共缓存）。
      void queryClient.invalidateQueries({ queryKey: ['state'] })
      for (const key of extraKeys ?? []) {
        void queryClient.invalidateQueries({ queryKey: key })
      }
    },
  })
}

export function useSetMutation() {
  return useMiniMutation('set')
}
export function useBotMutation() {
  return useMiniMutation('bot')
}
export function useChatMutation() {
  return useMiniMutation('chat')
}
export function useUpstreamMutation() {
  return useMiniMutation('upstream')
}
export function useModelMutation() {
  return useMiniMutation('model')
}
export function useAdminMutation() {
  return useMiniMutation('admin')
}
export function useGbanMutation() {
  return useMiniMutation('gban')
}
export function useGbanOwnMutation() {
  return useMiniMutation('gbanown')
}
export function useWhitelistMutation() {
  return useMiniMutation('whitelist')
}
export function useDigestMutation() {
  return useMiniMutation('digest')
}

/** 记录操作：详情/列表/用户页都会变。 */
export function useLogactMutation() {
  return useMiniMutation('logact', { invalidate: [['log'], ['logs'], ['user']] })
}

/** 申诉操作：详情与列表都要刷新。 */
export function useAppealactMutation() {
  return useMiniMutation('appealact', { invalidate: [['appeal'], ['appeals']] })
}

export interface OptimisticVars<TBody> {
  body: TBody
  /** apply 基于当前 ['state'] 缓存放回乐观结果；必须返回新对象，不要原地改。 */
  apply: (state: State) => State
}

/**
 * useOptimisticMiniMutation 是「开关类」写操作的乐观更新：立即把预期结果写进
 * ['state']，失败回滚到调用前的快照并冒泡错误（页面 toast），无论成败最后都
 * 失效 ['state'] 用服务端真值校正。
 *
 * 只适用于结果可从前端本地数据推断的简单翻转（启用开关等）；表单类写操作
 * 必须等服务端确认后再刷新。
 */
export function useOptimisticMiniMutation<TBody = Record<string, unknown>, TResp = OkResp>(
  op: MiniMutationOp,
) {
  const queryClient = useQueryClient()
  return useMutation<TResp, ApiError, OptimisticVars<TBody>, { prev: State | undefined }>({
    mutationFn: ({ body }) => api<TResp>(op, body),
    onMutate: async ({ apply }) => {
      // 有在途的 state 请求时先取消：否则它的旧响应可能盖掉乐观更新。
      await queryClient.cancelQueries({ queryKey: ['state'] })
      const prev = queryClient.getQueryData<State>(['state'])
      queryClient.setQueryData<State>(['state'], (s) => (s ? apply(s) : s))
      return { prev }
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev !== undefined) queryClient.setQueryData(['state'], ctx.prev)
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['state'] })
    },
  })
}
