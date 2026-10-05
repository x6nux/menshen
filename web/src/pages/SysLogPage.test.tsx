// 运行日志页：级别下限筛选（含更高档）、搜索防抖、结构化字段渲染、
// 各级统计、非主管理员的权限空态（且不发请求）。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State, SysLogsResp } from '../api/types'
import { mockState, mockSysLogs } from '../mocks/fixtures'
import { renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { SysLogPage } from './SysLogPage'

startTestServer()

const EMPTY: SysLogsResp = {
  logs: [],
  page: 1,
  total: 0,
  counts: { debug: 0, info: 0, warn: 0, error: 0 },
}

describe('SysLogPage', () => {
  it('渲染级别徽标、消息与结构化字段', async () => {
    renderPage(<SysLogPage />)

    const errorRow = await screen.findByTestId('syslog-row-4')
    expect(errorRow).toHaveTextContent('（mock）上游连续失败')
    expect(errorRow).toHaveTextContent('err=context deadline exceeded')
    // 级别徽标按色调分级：错误红、警告黄、信息绿、调试中性。
    expect(errorRow.querySelector('[data-tone="no"]')).toHaveTextContent('错误')
    expect(screen.getByTestId('syslog-row-3').querySelector('[data-tone="warn"]')).toHaveTextContent('警告')
    expect(screen.getByTestId('syslog-row-2').querySelector('[data-tone="ok"]')).toHaveTextContent('信息')
    expect(screen.getByTestId('syslog-row-1').querySelector('[data-tone="neutral"]')).toHaveTextContent('调试')
  })

  it('各级统计反映当前搜索（不受级别筛选影响）', async () => {
    renderPage(<SysLogPage />)
    expect(await screen.findByTestId('syslog-counts')).toHaveTextContent(
      '各级：调试 1 · 信息 1 · 警告 1 · 错误 1',
    )
  })

  it('级别 chips 发「不低于」下限，列表随之收窄', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/syslog', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(EMPTY)
      }),
    )
    renderPage(<SysLogPage />)

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ level: '', q: '', page: 1 })

    fireEvent.click(screen.getByTestId('syslog-level-warn'))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toMatchObject({ level: 'warn', page: 1 })

    fireEvent.click(screen.getByTestId('syslog-level-error'))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toMatchObject({ level: 'error', page: 1 })
  })

  it('选「警告」后只剩 WARN 与 ERROR 两行', async () => {
    renderPage(<SysLogPage />)
    await screen.findByTestId('syslog-row-1')

    fireEvent.click(screen.getByTestId('syslog-level-warn'))

    await waitFor(() => expect(screen.queryByTestId('syslog-row-1')).not.toBeInTheDocument())
    expect(screen.queryByTestId('syslog-row-2')).not.toBeInTheDocument()
    expect(screen.getByTestId('syslog-row-3')).toBeInTheDocument()
    expect(screen.getByTestId('syslog-row-4')).toBeInTheDocument()
    expect(screen.getByTestId('syslog-total')).toHaveTextContent('共 2 条')
  })

  it('搜索 300ms 防抖只发一次；回车立即且防抖不再重复', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/syslog', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(EMPTY)
      }),
    )
    renderPage(<SysLogPage />)
    await waitFor(() => expect(bodies).toHaveLength(1))

    const input = screen.getByLabelText('搜索运行日志')
    fireEvent.change(input, { target: { value: '上游' } })
    fireEvent.change(input, { target: { value: '上游失败' } })
    await waitFor(() => expect(bodies).toHaveLength(2), { timeout: 2000 })
    expect(bodies[1]).toMatchObject({ q: '上游失败', page: 1 })
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(bodies).toHaveLength(2)

    fireEvent.change(input, { target: { value: 'webhook' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toMatchObject({ q: 'webhook' })
    await new Promise((resolve) => setTimeout(resolve, 400))
    expect(bodies).toHaveLength(3)
  })

  it('字段可被搜索命中（服务端口径）', async () => {
    renderPage(<SysLogPage />)
    await screen.findByTestId('syslog-row-1')

    fireEvent.change(screen.getByLabelText('搜索运行日志'), { target: { value: 'deadline' } })
    await waitFor(() => expect(screen.queryByTestId('syslog-row-1')).not.toBeInTheDocument())
    const row = screen.getByTestId('syslog-row-4')
    expect(within(row).getByText('（mock）上游连续失败')).toBeInTheDocument()
  })

  it('次级管理员：不发请求，显示权限空态', async () => {
    const bodies: number[] = []
    server.use(
      http.post('*/miniapp/api/state', () =>
        HttpResponse.json<State>({ ...structuredClone(mockState), me: { uid: 200, main: false } }),
      ),
      http.post('*/miniapp/api/syslog', () => {
        bodies.push(1)
        return HttpResponse.json(EMPTY)
      }),
    )
    renderPage(<SysLogPage />)

    expect(await screen.findByText('需要主管理员权限')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)
  })

  it('每页 50 条：还有下一页时可加载更多', async () => {
    const pages: number[] = []
    server.use(
      http.post('*/miniapp/api/syslog', async ({ request }) => {
        const body = (await request.json()) as { page: number }
        pages.push(body.page)
        if (body.page === 1) {
          const logs = Array.from({ length: 50 }, (_, i) => ({
            ...mockSysLogs[1],
            seq: 1000 + i,
            message: `行${1000 + i}`,
          }))
          return HttpResponse.json<SysLogsResp>({
            logs,
            page: 1,
            total: 55,
            counts: { debug: 0, info: 55, warn: 0, error: 0 },
          })
        }
        const logs = Array.from({ length: 5 }, (_, i) => ({
          ...mockSysLogs[1],
          seq: 900 + i,
          message: `行${900 + i}`,
        }))
        return HttpResponse.json<SysLogsResp>({
          logs,
          page: 2,
          total: 55,
          counts: { debug: 0, info: 55, warn: 0, error: 0 },
        })
      }),
    )
    renderPage(<SysLogPage />)

    expect(await screen.findByText('行1049')).toBeInTheDocument()
    expect(screen.queryByText('行900')).not.toBeInTheDocument()
    expect(screen.getByTestId('syslog-total')).toHaveTextContent('共 55 条')

    fireEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText('行900')).toBeInTheDocument()
    expect(pages).toEqual([1, 2])
    expect(screen.getByText('没有更多了')).toBeInTheDocument()
  })
})
