import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import { useLogactMutation, useMiniMutation } from './mutations'

vi.mock('./client', () => ({ api: vi.fn() }))
const mockApi = vi.mocked(api)

function setup() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { client, invalidate, wrapper }
}

beforeEach(() => {
  mockApi.mockReset()
})

describe('useMiniMutation', () => {
  it('成功后失效 [state]（通用 op）', async () => {
    mockApi.mockResolvedValue({ ok: true, note: '已保存' } as never)
    const { invalidate, wrapper } = setup()
    const { result } = renderHook(() => useMiniMutation('set'), { wrapper })

    await act(async () => {
      await result.current.mutateAsync({ key: 'antiad_enabled', value: '1' })
    })

    expect(mockApi).toHaveBeenCalledWith('set', { key: 'antiad_enabled', value: '1' })
    expect(invalidate).toHaveBeenCalledTimes(1)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['state'] })
  })

  it('logact 追加失效 log/logs/user', async () => {
    mockApi.mockResolvedValue({ ok: true } as never)
    const { invalidate, wrapper } = setup()
    const { result } = renderHook(() => useLogactMutation(), { wrapper })

    await act(async () => {
      await result.current.mutateAsync({ id: 9, action: 'review' })
    })

    expect(mockApi).toHaveBeenCalledWith('logact', { id: 9, action: 'review' })
    expect(invalidate.mock.calls.map((c) => c[0])).toEqual([
      { queryKey: ['state'] },
      { queryKey: ['log'] },
      { queryKey: ['logs'] },
      { queryKey: ['user'] },
    ])
  })

  it('失败时冒泡错误且不失效任何缓存', async () => {
    mockApi.mockRejectedValue(new Error('取值非法：0-100 的整数'))
    const { invalidate, wrapper } = setup()
    const { result } = renderHook(() => useMiniMutation('set'), { wrapper })

    await act(async () => {
      await expect(result.current.mutateAsync({ key: 'x', value: '999' })).rejects.toThrow(
        '取值非法：0-100 的整数',
      )
    })
    expect(invalidate).not.toHaveBeenCalled()
  })
})
