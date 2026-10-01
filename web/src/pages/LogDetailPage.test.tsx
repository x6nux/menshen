// 记录详情行为测试：信息卡/原文/查看页链接、四个动作的参数与危险确认、
// 主管理员专属的两个联封动作、成功 note toast。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockLogDetail, mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { LogDetailPage } from './LogDetailPage'

startTestServer()

afterEach(() => {
  vi.restoreAllMocks()
})

/** 等确认面板退场动画结束：期间 MUI Modal 会把页面内容标记 aria-hidden。 */
async function waitSheetClosed() {
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
}

describe('LogDetailPage', () => {
  it('渲染信息卡与原文；查看页链接调用 openLink（无 SDK 时 window.open）', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderPage(<LogDetailPage id={9812} />)

    // 详情单独请求 log（fixtures 的详情正文比列表更完整）
    expect(await screen.findByText(/详情接口的正文比列表更完整/)).toBeInTheDocument()
    expect(screen.getByText('时间')).toBeInTheDocument()
    expect(screen.getByText('处置')).toBeInTheDocument()
    expect(screen.getByText('删除+禁言')).toBeInTheDocument()
    // 群标题来自 state.chats（fixtures：测试群）
    expect(screen.getByText(/测试群/)).toBeInTheDocument()
    expect(screen.getByText('uid 555（资料）')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '打开原文查看页' }))
    expect(openSpy).toHaveBeenCalledWith('https://example.com/log/9812', '_blank')
  })

  it('四个主操作调用 logact 的参数正确；危险标记需确认；成功 toast 服务端 note', async () => {
    const bodies: Record<string, unknown>[] = []
    let logCalls = 0
    server.use(
      http.post('*/miniapp/api/log', () => {
        logCalls += 1
        return HttpResponse.json(structuredClone(mockLogDetail))
      }),
      http.post('*/miniapp/api/logact', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true, note: '已执行（服务端）' })
      }),
    )
    renderPage(<LogDetailPage id={9812} />)
    await screen.findByText('AI 复查')

    fireEvent.click(screen.getByRole('button', { name: 'AI 复查' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ id: 9812, action: 'review' })
    expect(await screen.findByText('已执行（服务端）')).toBeInTheDocument()
    // ['log'] 失效触发详情重新请求
    await waitFor(() => expect(logCalls).toBeGreaterThanOrEqual(2))

    fireEvent.click(screen.getByRole('button', { name: '解封（判定维持）' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ id: 9812, action: 'unmute' })

    fireEvent.click(screen.getByRole('button', { name: '加白名单 24h' }))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toEqual({ id: 9812, action: 'white' })

    // 人工标记广告：旧页确认文案；取消不发请求
    fireEvent.click(screen.getByRole('button', { name: '人工标记广告' }))
    expect(await screen.findByText('不经 AI 直接按最高档处置？')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    await waitSheetClosed()
    expect(bodies).toHaveLength(3)

    fireEvent.click(screen.getByRole('button', { name: '人工标记广告' }))
    fireEvent.click(await screen.findByRole('button', { name: '确定' }))
    await waitFor(() => expect(bodies).toHaveLength(4))
    expect(bodies[3]).toEqual({ id: 9812, action: 'ban' })
  })

  it('主管理员才见联合封禁/解除联合封禁，且都需危险确认', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/logact', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true })
      }),
    )
    renderPage(<LogDetailPage id={9812} />)

    fireEvent.click(await screen.findByRole('button', { name: '联合封禁' }))
    expect(await screen.findByText('加入联合封禁名单并全平台执行？')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ id: 9812, action: 'gban' })
    await waitSheetClosed()

    fireEvent.click(screen.getByRole('button', { name: '解除联合封禁' }))
    expect(
      await screen.findByText('把该用户从你能解除的联合封禁名单里移除。'),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ id: 9812, action: 'ungban' })
  })

  it('次级管理员看不到两个联封动作', async () => {
    server.use(
      http.post('*/miniapp/api/state', () =>
        HttpResponse.json({
          ...structuredClone(mockState),
          me: { uid: 100, main: false },
        }),
      ),
    )
    renderPage(<LogDetailPage id={9812} />)

    await screen.findByText('AI 复查')
    expect(screen.queryByRole('button', { name: '联合封禁' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '解除联合封禁' })).not.toBeInTheDocument()
  })

  it('用户链接键盘可达：Enter 进用户页', async () => {
    renderPage(
      <>
        <LogDetailPage id={9812} />
        <NavProbe />
      </>,
    )

    const link = await screen.findByRole('link', { name: 'uid 555（资料）' })
    fireEvent.keyDown(link, { key: 'Enter' })
    expect(screen.getByTestId('nav-top').textContent).toBe('user')
  })

  it('主管理员 6 按钮场景底部预留 ≥210（无 ResizeObserver 时用兜底值）', async () => {
    renderPage(<LogDetailPage id={9812} />)

    await screen.findByRole('button', { name: '联合封禁' })
    const reserved = Number(
      screen.getByTestId('log-detail-page').getAttribute('data-reserved'),
    )
    expect(reserved).toBeGreaterThanOrEqual(210)
  })

  it('动作失败时 toast 服务端错误文案，按钮保留', async () => {
    server.use(
      http.post('*/miniapp/api/logact', () =>
        HttpResponse.json({ error: '该 bot 未在运行，无法执行群内操作' }, { status: 400 }),
      ),
    )
    renderPage(<LogDetailPage id={9812} />)
    await screen.findByText('AI 复查')

    fireEvent.click(screen.getByRole('button', { name: 'AI 复查' }))
    expect(await screen.findByText('该 bot 未在运行，无法执行群内操作')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'AI 复查' })).toBeEnabled()
  })
})
