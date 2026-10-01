// 记录页行为测试：分段/筛选/搜索防抖/无限滚动/intent 消费/uid 链接。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { LogRow } from '../api/types'
import { mockAppeals, mockLogs } from '../mocks/fixtures'
import { IntentSetter, NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { RecordsPage } from './RecordsPage'

startTestServer()

function hit(id: number): LogRow {
  return { ...mockLogs[0], id }
}

function hits(ids: number[]): LogRow[] {
  return ids.map(hit)
}

describe('RecordsPage', () => {
  it('筛选 chips 切换发出的请求参数正确（默认已删除）', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/logs', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ logs: [hit(9001)], page: 1, total: 1 })
      }),
    )
    renderPage(<RecordsPage />)

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ verdict: 'deleted', q: '', page: 1 })

    fireEvent.click(screen.getByText('全部'))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toMatchObject({ verdict: '' })

    fireEvent.click(screen.getByText('命中'))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toMatchObject({ verdict: 'ad' })

    fireEvent.click(screen.getByText('正常'))
    await waitFor(() => expect(bodies).toHaveLength(4))
    expect(bodies[3]).toMatchObject({ verdict: 'clean' })

    fireEvent.click(screen.getByText('跳过'))
    await waitFor(() => expect(bodies).toHaveLength(5))
    expect(bodies[4]).toMatchObject({ verdict: 'skipped' })
  })

  it('搜索 300ms 防抖只发一次；回车立即且防抖不再重复', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/logs', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ logs: [], page: 1, total: 0 })
      }),
    )
    renderPage(<RecordsPage />)
    await waitFor(() => expect(bodies).toHaveLength(1))

    const input = screen.getByLabelText('搜索记录')
    fireEvent.change(input, { target: { value: 'abc' } })
    fireEvent.change(input, { target: { value: 'abcd' } })
    await waitFor(() => expect(bodies).toHaveLength(2), { timeout: 2000 })
    expect(bodies[1]).toMatchObject({ q: 'abcd', page: 1 })
    // 防抖窗口过去也没有第三次请求
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(bodies).toHaveLength(2)

    // 回车立即：立刻发一次，且已排队 300ms 的防抖被取消
    fireEvent.change(input, { target: { value: 'xyz' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toMatchObject({ q: 'xyz' })
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(bodies).toHaveLength(3)
  })

  it('无限滚动加载第二页；加载数 ≥ total 后不再请求', async () => {
    const pages: number[] = []
    server.use(
      http.post('*/miniapp/api/logs', async ({ request }) => {
        const body = (await request.json()) as { page: number }
        pages.push(body.page)
        if (body.page === 1) {
          return HttpResponse.json({ logs: hits(Array.from({ length: 20 }, (_, i) => 9001 + i)), page: 1, total: 25 })
        }
        return HttpResponse.json({
          logs: hits(Array.from({ length: 5 }, (_, i) => 9021 + i)),
          page: 2,
          total: 25,
        })
      }),
    )
    renderPage(<RecordsPage />)

    // 第一页 20 条，还有下一页
    expect(await screen.findByRole('button', { name: /#9020/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /#9021/ })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByRole('button', { name: /#9025/ })).toBeInTheDocument()
    expect(pages).toEqual([1, 2])

    // 25 ≥ total=25：没有下一页，也不再发请求
    expect(screen.getByText('没有更多了')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '加载更多' })).not.toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(pages).toEqual([1, 2])
  })

  it('uid 链接进用户页，点行进记录详情', async () => {
    server.use(
      http.post('*/miniapp/api/logs', () =>
        HttpResponse.json({ logs: [hit(9812)], page: 1, total: 1 }),
      ),
    )
    renderPage(
      <>
        <RecordsPage />
        <NavProbe />
      </>,
    )

    fireEvent.click(await screen.findByText('uid 555（资料）'))
    expect(screen.getByTestId('nav-top').textContent).toBe('user')

    fireEvent.click(screen.getByRole('button', { name: /#9812/ }))
    expect(screen.getByTestId('nav-top').textContent).toBe('log')
  })

  it("intent='appeals' 默认申诉分段并消费意图；重挂载后不再恢复", async () => {
    renderPage(
      <>
        <IntentSetter tab="records" intent="appeals" />
        <RecordsHost />
        <NavProbe />
      </>,
    )

    // 申诉分段：未结 chip 可见且意图已被清掉
    expect(await screen.findByText('未结')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('nav-intent').textContent).toBe(''))

    // 手动切回判定记录
    fireEvent.click(screen.getByRole('button', { name: '判定记录' }))
    expect(await screen.findByLabelText('搜索记录')).toBeInTheDocument()

    // 模拟导航往返重挂载：意图已消费，默认回到判定记录
    fireEvent.click(screen.getByRole('button', { name: 'hide' }))
    fireEvent.click(screen.getByRole('button', { name: 'show' }))
    expect(await screen.findByLabelText('搜索记录')).toBeInTheDocument()
    expect(screen.queryByText('未结')).not.toBeInTheDocument()
  })

  it('申诉分段：未结角标来自 state.todo，行可进申诉详情，筛选切 all', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/appeals', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ appeals: mockAppeals, page: 1, total: mockAppeals.length })
      }),
    )
    renderPage(
      <>
        <RecordsPage />
        <NavProbe />
      </>,
    )

    // fixtures：todo.open_appeals=3
    expect(await screen.findByTestId('appeals-count')).toHaveTextContent('3')

    fireEvent.click(screen.getByRole('button', { name: /申诉/ }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ filter: 'open', page: 1 })

    // 状态徽标 + AI 结论摘要
    expect(await screen.findByText('#77 · uid 555')).toBeInTheDocument()
    expect(screen.getByText('待人工处理')).toBeInTheDocument()
    expect(screen.getByText(/维持原判/)).toBeInTheDocument()

    fireEvent.click(screen.getByText('全部'))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ filter: '', page: 1 })

    fireEvent.click(screen.getByRole('button', { name: /#77/ }))
    expect(screen.getByTestId('nav-top').textContent).toBe('appeal')
  })
})

/** RecordsHost 提供隐藏/显示按钮，模拟页面因导航往返被卸载重挂载。 */
function RecordsHost() {
  const [show, setShow] = useState(true)
  return (
    <>
      <button onClick={() => setShow((s) => !s)}>{show ? 'hide' : 'show'}</button>
      {show && <RecordsPage />}
    </>
  )
}
