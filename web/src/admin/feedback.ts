// 操作反馈：所有写操作统一「成功提示 / 失败提示（用服务端文案）」，
// 避免每个视图各写一遍 toast 分流。
import { ApiError } from '../api/client'
import { useToast } from '../ui'

export type Run = (promise: Promise<unknown>, okText?: string) => void

export function useRunFeedback(): Run {
  const toast = useToast()
  return (promise, okText = '已保存') => {
    promise
      .then(() => toast(okText))
      .catch((err) => toast(err instanceof ApiError ? err.message : '操作失败，请重试'))
  }
}
