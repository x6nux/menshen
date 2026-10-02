// 我的页：身份卡与入口列表，可见性按 2.1 矩阵（次管没有上游/模型/设置入口），
// 入口点击进入对应二级页（lists 带 section）。
import { fireEvent, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { MinePage } from './MinePage'

startTestServer()

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

describe('MinePage', () => {
  it('主管理员：身份卡、四个入口与页脚说明', async () => {
    renderPage(<MinePage />)

    expect(await screen.findByText('主管理员')).toBeInTheDocument()
    expect(screen.getByText('@menshen_admin')).toBeInTheDocument()
    expect(screen.getByText('可以管理全部机器人、群组、名单与全局设置。')).toBeInTheDocument()
    for (const entry of ['名单管理', '上游渠道', '模型定价', '全局设置', 'AI 必封规则']) {
      expect(screen.getByText(entry)).toBeInTheDocument()
    }
    expect(screen.getByText('本页仅管理员可见；接入新 bot 请在私聊面板操作。')).toBeInTheDocument()
  })

  it('主管理员：入口点击进入对应二级页', async () => {
    renderPage(
      <>
        <MinePage />
        <NavProbe />
      </>,
    )

    await screen.findByText('名单管理')
    fireEvent.click(screen.getByText('名单管理'))
    expect(screen.getByTestId('nav-top').textContent).toBe('lists')
    // 主管理员默认进白名单分段（section 会透传给 ListsPage）
    expect(screen.getByTestId('nav-intent').textContent).toBe('')

    fireEvent.click(screen.getByText('上游渠道'))
    expect(screen.getByTestId('nav-top').textContent).toBe('upstreams')
    fireEvent.click(screen.getByText('模型定价'))
    expect(screen.getByTestId('nav-top').textContent).toBe('models')
    fireEvent.click(screen.getByText('全局设置'))
    expect(screen.getByTestId('nav-top').textContent).toBe('settings')
    fireEvent.click(screen.getByText('AI 必封规则'))
    expect(screen.getByTestId('nav-top').textContent).toBe('rules')
  })

  it('次级管理员：不出现上游/模型/设置入口，名单入口仍在', async () => {
    useState({
      ...structuredClone(mockState),
      me: { uid: 200, main: false },
      bots: mockState.bots.map((b) => ({ ...b, owner_id: 200 })),
    })
    renderPage(<MinePage />)

    expect(await screen.findByText('次级管理员')).toBeInTheDocument()
    expect(screen.getByText('可以管理自己名下的机器人、群组，以及联合封禁名单。')).toBeInTheDocument()
    expect(screen.getByText('名单管理')).toBeInTheDocument()
    expect(screen.getByText('联合封禁名单')).toBeInTheDocument()
    expect(screen.queryByText('上游渠道')).not.toBeInTheDocument()
    expect(screen.queryByText('模型定价')).not.toBeInTheDocument()
    expect(screen.queryByText('全局设置')).not.toBeInTheDocument()
    expect(screen.queryByText('AI 必封规则')).not.toBeInTheDocument()
  })
})
