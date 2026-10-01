import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mockState } from '../mocks/fixtures'
import { api } from './client'
import { useLogactMutation, useMiniMutation, useOptimisticMiniMutation } from './mutations'
import type { State } from './types'

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

describe('useOptimisticMiniMutation', () => {
  it('set 开关：先乐观写 state，失败回滚到调用前快照并冒泡', async () => {
    const { client, invalidate, wrapper } = setup()
    client.setQueryData(['state'], structuredClone(mockState))

    let reject!: (reason: unknown) => void
    mockApi.mockReturnValue(
      new Promise((_resolve, rej) => {
        reject = rej
      }) as never,
    )
    const { result } = renderHook(() => useOptimisticMiniMutation('set'), { wrapper })

    act(() => {
      result.current.mutate({
        body: { scope: 'bot', bot_id: 2, key: 'antiad_mute_minutes', value: '0' },
        apply: (s) => ({
          ...s,
          bot_settings: {
            ...s.bot_settings,
            '2': { ...s.bot_settings['2'], antiad_mute_minutes: '0' },
          },
        }),
      })
    })

    // 服务端还没回包，state 已经是乐观值（fixtures 原值 30）
    await waitFor(() =>
      expect(
        (client.getQueryData(['state']) as State).bot_settings['2'].antiad_mute_minutes,
      ).toBe('0'),
    )

    await act(async () => {
      reject(new Error('保存失败'))
    })
    await waitFor(() =>
      expect(
        (client.getQueryData(['state']) as State).bot_settings['2'].antiad_mute_minutes,
      ).toBe('30'),
    )
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['state'] })
  })
})
