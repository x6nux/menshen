// 写操作 hooks：一个通用 useMiniMutation + 各 op 薄封装。
// 失效规则：一切写操作失效 ['state']；logact 追加 ['log']/['logs']/['user']；
// appealact 追加 ['appeal']/['appeals']。错误不吞：直接冒泡给调用方，
// toast/回滚由 UI 层处理。
import { useMutation, useQueryClient } from '@tanstack/react-query'
import type { QueryKey } from '@tanstack/react-query'
import { api } from './client'
import type { OkResp } from './types'

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
  return useMutation<TResp, Error, TBody>({
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
