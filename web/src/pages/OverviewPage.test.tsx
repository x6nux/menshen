// 概览页行为测试：待办跳转（带意图）、近 24h 指标、最近命中取前 3 条并可进详情。
import { fireEvent, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { LogRow } from '../api/types'
import { mockLogs } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { OverviewPage } from './OverviewPage'

startTestServer()

function hit(id: number): LogRow {
  return { ...mockLogs[0], id }
}

describe('OverviewPage', () => {
  it('渲染指标与最近命中（只取前 3 条），点行进入 log 详情、全部记录进记录页', async () => {
    server.use(
      http.post('*/miniapp/api/logs', () =>
        HttpResponse.json({ logs: [hit(9001), hit(9002), hit(9003), hit(9004)], page: 1, total: 4 }),
      ),
    )
    renderPage(
      <>
        <OverviewPage />
        <NavProbe />
      </>,
    )

    // 近 24h 指标（fixtures：checked=1284 / hits=12 / cost_text=$0.42 / chats=8）
    expect(await screen.findByText('送检')).toBeInTheDocument()
    expect(screen.getByText('1284')).toBeInTheDocument()
    expect(screen.getByText('12')).toBeInTheDocument()
    expect(screen.getByText('$0.42')).toBeInTheDocument()
    expect(screen.getByText('8')).toBeInTheDocument()

    // 最近命中只渲染前 3 条
    expect(await screen.findByText('#9001 广告')).toBeInTheDocument()
    expect(screen.getByText('#9003 广告')).toBeInTheDocument()
    expect(screen.queryByText('#9004 广告')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('#9001 广告'))
    expect(screen.getByTestId('nav-top').textContent).toBe('log')

    fireEvent.click(screen.getByText('全部记录'))
    expect(screen.getByTestId('nav-tab').textContent).toBe('records')
  })

  it('待办三项分别跳到记录/群组/机器人并带意图', async () => {
    renderPage(
      <>
        <OverviewPage />
        <NavProbe />
      </>,
    )

    fireEvent.click(await screen.findByText('未结申诉'))
    expect(screen.getByTestId('nav-tab').textContent).toBe('records')
    expect(screen.getByTestId('nav-intent').textContent).toBe('appeals')

    fireEvent.click(screen.getByText('演练中的群'))
    expect(screen.getByTestId('nav-tab').textContent).toBe('chats')
    expect(screen.getByTestId('nav-intent').textContent).toBe('dryrun')

    fireEvent.click(screen.getByText('停用的机器人'))
    expect(screen.getByTestId('nav-tab').textContent).toBe('bots')
    expect(screen.getByTestId('nav-intent').textContent).toBe('')
  })

  it('最近命中加载失败显示错误卡，重试后恢复', async () => {
    let failed = false
    server.use(
      http.post('*/miniapp/api/logs', () => {
        if (!failed) {
          failed = true
          return HttpResponse.json({ error: '服务暂时不可用' }, { status: 500 })
        }
        return HttpResponse.json({ logs: [hit(9001)], page: 1, total: 1 })
      }),
    )
    renderPage(<OverviewPage />)

    expect(await screen.findByText('网络异常')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('#9001 广告')).toBeInTheDocument()
  })
})
