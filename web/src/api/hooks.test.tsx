import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import { useAppeals, useLog, useLogs, useMiniState, useUser } from './hooks'

vi.mock('./client', () => ({ api: vi.fn() }))
const mockApi = vi.mocked(api)

function makeQuery(children: ReactNode, client: QueryClient) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

function makeClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

beforeEach(() => {
  mockApi.mockClear()
})

describe('查询 hooks', () => {
  it('useLogs 把筛选/搜索/页码带进 body，并落进 queryKey', async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({ logs: [], page: 2, total: 0 } as never)
    const { result } = renderHook(() => useLogs({ filter: 'ad', q: 'hello', page: 2 }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenCalledWith('logs', { verdict: 'ad', q: 'hello', page: 2 }, expect.anything())
    expect(client.getQueryData(['logs', 'ad', 'hello', 2])).toEqual({ logs: [], page: 2, total: 0 })
  })

  it("useLogs 的 '' 与 'all' 归一化成同一个 queryKey，只请求一次", async () => {
    // staleTime 设成无限：第二次挂载若命中同一 key 就不会再发请求，
    // 因此「只请求一次」能直接证明两个筛选归一化成了同一个 queryKey。
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
    })
    mockApi.mockResolvedValue({ logs: [], page: 1, total: 0 } as never)
    const first = renderHook(() => useLogs({ filter: '' }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(first.result.current.isSuccess).toBe(true))
    const second = renderHook(() => useLogs({ filter: 'all' }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(second.result.current.isSuccess).toBe(true))

    expect(mockApi).toHaveBeenCalledTimes(1)
    expect(mockApi).toHaveBeenCalledWith('logs', { verdict: '', q: '', page: 1 }, expect.anything())
    expect(client.getQueryData(['logs', 'all', '', 1])).toBeDefined()
  })

  it('useUser 带 user_id/filter/page；useAppeals 默认 open', async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({} as never)
    const user = renderHook(() => useUser(5, 'all', 3), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(user.result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenCalledWith(
      'user',
      { user_id: 5, filter: 'all', page: 3 },
      expect.anything(),
    )

    const appeals = renderHook(() => useAppeals(), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(appeals.result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenCalledWith('appeals', { filter: 'open', page: 1 }, expect.anything())
  })

  it('id 为 0 或 enabled=false 时不发请求', async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({} as never)
    renderHook(() => useLog(0), { wrapper: ({ children }) => makeQuery(children, client) })
    renderHook(() => useAppeals({ enabled: false }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    renderHook(() => useMiniState(false), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await Promise.resolve()
    expect(mockApi).not.toHaveBeenCalled()
  })

  it('useMiniState 默认启用并发 state 请求', async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({} as never)
    const { result } = renderHook(() => useMiniState(), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenCalledWith('state', undefined, expect.anything())
  })
})
