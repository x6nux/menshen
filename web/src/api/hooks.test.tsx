import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import {
  useAppeals,
  useInfiniteAppeals,
  useInfiniteLogs,
  useInfiniteUserLogs,
  useLog,
  useLogs,
  useMiniState,
  useUser,
} from './hooks'

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

  it("useLogs() 无参默认 deleted（与旧页 LOGF='deleted' 一致）", async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({ logs: [], page: 1, total: 0 } as never)
    const { result } = renderHook(() => useLogs(), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenCalledWith(
      'logs',
      { verdict: 'deleted', q: '', page: 1 },
      expect.anything(),
    )
    // queryKey 与显式传 'deleted' 相同
    expect(client.getQueryData(['logs', 'deleted', '', 1])).toEqual({
      logs: [],
      page: 1,
      total: 0,
    })
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

describe('无限列表 hooks', () => {
  function rows(n: number) {
    return Array.from({ length: n }, (_, i) => ({ id: i + 1 }))
  }

  it('useInfiniteLogs：pageParam 从 1 递增，loaded < total 才有下一页', async () => {
    const client = makeClient()
    mockApi.mockImplementation((async (_op: string, body: { page: number }) =>
      body.page === 1
        ? { logs: rows(20), page: 1, total: 25 }
        : { logs: rows(5), page: 2, total: 25 }) as never)
    const { result } = renderHook(() => useInfiniteLogs({ filter: 'ad', q: 'hello' }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    expect(mockApi).toHaveBeenLastCalledWith(
      'logs',
      { verdict: 'ad', q: 'hello', page: 1 },
      expect.anything(),
    )
    expect(result.current.hasNextPage).toBe(true)

    await act(() => result.current.fetchNextPage())
    await waitFor(() => expect(result.current.hasNextPage).toBe(false))
    expect(mockApi).toHaveBeenLastCalledWith(
      'logs',
      { verdict: 'ad', q: 'hello', page: 2 },
      expect.anything(),
    )
    // 加载数（25）≥ total（25）后 fetchNextPage 不再发请求
    await act(() => result.current.fetchNextPage())
    expect(mockApi).toHaveBeenCalledTimes(2)
  })

  it('useInfiniteLogs 默认 deleted；useInfiniteAppeals 默认 open 且 all 归一化', async () => {
    const client = makeClient()
    mockApi.mockResolvedValue({ logs: [], page: 1, total: 0, appeals: [] } as never)
    const logs = renderHook(() => useInfiniteLogs(), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(logs.result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenLastCalledWith(
      'logs',
      { verdict: 'deleted', q: '', page: 1 },
      expect.anything(),
    )

    const appeals = renderHook(() => useInfiniteAppeals({ filter: '' }), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(appeals.result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenLastCalledWith(
      'appeals',
      { filter: '', page: 1 },
      expect.anything(),
    )
  })

  it('useInfiniteUserLogs：用 shown 判断下一页；默认 filter=act', async () => {
    const client = makeClient()
    mockApi.mockImplementation((async (_op: string, body: { page: number }) =>
      body.page === 1
        ? { logs: rows(20), page: 1, shown: 21, total: 99 }
        : { logs: rows(1), page: 2, shown: 21, total: 99 }) as never)
    const { result } = renderHook(() => useInfiniteUserLogs(555), {
      wrapper: ({ children }) => makeQuery(children, client),
    })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(mockApi).toHaveBeenLastCalledWith(
      'user',
      { user_id: 555, filter: 'act', page: 1 },
      expect.anything(),
    )
    // total=99 但 shown=21：只按 shown 翻一页
    expect(result.current.hasNextPage).toBe(true)
    await act(() => result.current.fetchNextPage())
    await waitFor(() => expect(result.current.hasNextPage).toBe(false))
    expect(mockApi).toHaveBeenCalledTimes(2)
  })
})
