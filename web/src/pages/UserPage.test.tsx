// 用户页行为测试（5.1 迁移不变量）：默认只看被处置过的、记录行可进详情、
// hasMore 用 shown 判断；资料卡字段完整渲染。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { UserDossier, UserLogRow } from '../api/types'
import { mockUser } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { UserPage } from './UserPage'

startTestServer()

function userLog(id: number): UserLogRow {
  return { ...mockUser.logs[0], id }
}

function dossier(overrides: Partial<UserDossier> = {}): UserDossier {
  return { ...structuredClone(mockUser), ...overrides }
}

describe('UserPage', () => {
  it('默认过滤 act，资料卡完整渲染，记录行进记录详情', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/user', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(dossier({ shown: 1 }))
      }),
    )
    renderPage(
      <>
        <UserPage id={555} />
        <NavProbe />
      </>,
    )

    expect(await screen.findByText('演示用户')).toBeInTheDocument()
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ user_id: 555, filter: 'act', page: 1 })

    // 资料卡字段
    expect(screen.getByText('@demo_user')).toBeInTheDocument()
    expect(screen.getByText('（mock）简介')).toBeInTheDocument()
    expect(screen.getByText(/留底 12 条/)).toBeInTheDocument()
    expect(screen.getByText(/画像累计 30 条/)).toBeInTheDocument()
    expect(screen.getByText(/历史命中 2 次/)).toBeInTheDocument()
    expect(screen.getByText(/1 个/)).toBeInTheDocument()
    expect(screen.getByText(/共 2 条，其中被处置过 1 条/)).toBeInTheDocument()

    // 默认分段选中「只看被处置过的」
    expect(screen.getByRole('button', { name: '只看被处置过的' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )

    // shown=1：没有下一页
    expect(await screen.findByText('没有更多了')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /#9812/ }))
    expect(screen.getByTestId('nav-top').textContent).toBe('log')
  })

  it('切「全部判定记录」后请求 filter=all', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/user', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(dossier())
      }),
    )
    renderPage(<UserPage id={555} />)
    await screen.findByText('演示用户')

    fireEvent.click(screen.getByRole('button', { name: '全部判定记录' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ user_id: 555, filter: 'all', page: 1 })
  })

  it('hasMore 用 shown：shown=21 时加载第二页，加载满后停止', async () => {
    const pages: number[] = []
    server.use(
      http.post('*/miniapp/api/user', async ({ request }) => {
        const body = (await request.json()) as { page: number }
        pages.push(body.page)
        if (body.page === 1) {
          return HttpResponse.json(
            dossier({ shown: 21, logs: Array.from({ length: 20 }, (_, i) => userLog(9500 + i)) }),
          )
        }
        return HttpResponse.json(dossier({ shown: 21, logs: [userLog(9520)] }))
      }),
    )
    renderPage(<UserPage id={555} />)

    expect(await screen.findByRole('button', { name: /#9519/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByRole('button', { name: /#9520/ })).toBeInTheDocument()

    expect(pages).toEqual([1, 2])
    expect(screen.getByText('没有更多了')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(pages).toEqual([1, 2])
  })
})
