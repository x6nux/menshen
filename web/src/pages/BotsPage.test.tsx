// 机器人列表：本地搜索（label/username/bot_id）、状态徽标、行点击进详情、空态。
import { fireEvent, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { BotsPage } from './BotsPage'

startTestServer()

describe('BotsPage', () => {
  it('按 label/username/bot_id 本地过滤，行徽标展示状态', async () => {
    renderPage(<BotsPage />)

    expect(await screen.findByText('门神小助手')).toBeInTheDocument()
    expect(screen.getByText('演示机器人')).toBeInTheDocument()
    // 状态徽标：主 bot 运行中 / 次 bot 已停用
    expect(screen.getByText('运行中')).toBeInTheDocument()
    expect(screen.getByText('已停用')).toBeInTheDocument()

    const input = screen.getByLabelText('搜索机器人')
    fireEvent.change(input, { target: { value: 'second' } })
    expect(screen.queryByText('门神小助手')).not.toBeInTheDocument()
    expect(screen.getByText('演示机器人')).toBeInTheDocument()

    fireEvent.change(input, { target: { value: '2' } })
    expect(screen.getByText('演示机器人')).toBeInTheDocument()

    fireEvent.change(input, { target: { value: '门神' } })
    expect(screen.getByText('门神小助手')).toBeInTheDocument()
    expect(screen.queryByText('演示机器人')).not.toBeInTheDocument()

    fireEvent.change(input, { target: { value: '不存在的关键词' } })
    expect(screen.getByText('没有匹配的机器人')).toBeInTheDocument()
  })

  it('点行进入机器人详情', async () => {
    renderPage(
      <>
        <BotsPage />
        <NavProbe />
      </>,
    )
    fireEvent.click(await screen.findByText('演示机器人'))
    expect(screen.getByTestId('nav-top').textContent).toBe('bot')
  })

  it('没有任何机器人时显示接入引导', async () => {
    server.use(
      http.post('*/miniapp/api/state', () =>
        HttpResponse.json({ ...structuredClone(mockState), bots: [], chats: [] }),
      ),
    )
    renderPage(<BotsPage />)
    expect(await screen.findByText('还没有可管理的机器人')).toBeInTheDocument()
    expect(screen.getByText(/私聊主 bot/)).toBeInTheDocument()
  })
})
