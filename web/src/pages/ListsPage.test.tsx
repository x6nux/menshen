// 名单管理：可见性矩阵（次管只有联封）、四处本地搜索（过滤 + 焦点不丢，
// 对应旧 TestMiniAppListSearch 的迁移不变量）、各分段的增删参数与确认文案。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { ListsPage } from './ListsPage'

startTestServer()

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

function stateWith(overrides: Partial<State>): State {
  return { ...structuredClone(mockState), ...overrides }
}

function capturePost(op: string, bodies: Record<string, unknown>[], note?: string) {
  server.use(
    http.post(`*/miniapp/api/${op}`, async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>)
      return HttpResponse.json({ ok: true, ...(note ? { note } : {}) })
    }),
  )
}

async function renderMain() {
  renderPage(<ListsPage />)
  // 主管理员默认白名单分段
  expect(await screen.findByText('555 · bot 所有群')).toBeInTheDocument()
}

describe('ListsPage 可见性', () => {
  it('主管理员有四个分段', async () => {
    await renderMain()
    for (const name of ['白名单', '资料放行', '联合封禁', '次级管理员']) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
    }
  })

  it('section 参数直接进入联封', async () => {
    renderPage(<ListsPage section="gban" />)
    expect(await screen.findByText('全局联合封禁组')).toBeInTheDocument()
    expect(screen.queryByText('默认豁免（内置，无需配置）')).not.toBeInTheDocument()
  })

  it('次级管理员只显示联封一个分段（白名单/资料放行/次级管理员均不可见）', async () => {
    useState(
      stateWith({
        me: { uid: 200, main: false },
        bots: mockState.bots.map((b) => ({ ...b, owner_id: 200 })),
      }),
    )
    renderPage(<ListsPage />)

    expect(await screen.findByText('全局联合封禁组')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '联合封禁' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '白名单' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '资料放行' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '次级管理员' })).not.toBeInTheDocument()
    expect(screen.queryByText('默认豁免（内置，无需配置）')).not.toBeInTheDocument()
  })

  it('白名单分段带默认豁免只读卡（主管理员与各归属人）', async () => {
    await renderMain()
    expect(screen.getByText('默认豁免（内置，无需配置）')).toBeInTheDocument()
    expect(screen.getByText('主管理员（你）')).toBeInTheDocument()
    expect(screen.getByText('「门神小助手」归属人')).toBeInTheDocument()
    expect(screen.getByText('各群的群主与管理员')).toBeInTheDocument()
    expect(screen.getByText('普通 bot（工具 bot）')).toBeInTheDocument()
  })
})

describe('ListsPage 四处搜索（本地过滤 + 焦点不丢）', () => {
  it('白名单：按 uid / 来源 / 群号过滤，输入焦点不丢', async () => {
    useState(
      stateWith({
        whitelist: [
          ...mockState.whitelist,
          {
            bot_id: 1,
            chat_id: -1001234567890,
            user_id: 556,
            expires_at: 0,
            source: 'manual',
            by_uid: 100,
          },
        ],
      }),
    )
    await renderMain()

    const input = screen.getByLabelText('搜索白名单')
    input.focus()

    fireEvent.change(input, { target: { value: '555' } })
    expect(screen.getByText('555 · bot 所有群')).toBeInTheDocument()
    expect(screen.queryByText('556 · 群 -1001234567890')).not.toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: 'manual' } })
    expect(screen.getByText('556 · 群 -1001234567890')).toBeInTheDocument()
    expect(screen.queryByText('555 · bot 所有群')).not.toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '-1001234567890' } })
    expect(screen.getByText('556 · 群 -1001234567890')).toBeInTheDocument()
    expect(screen.queryByText('555 · bot 所有群')).not.toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '没有这个人' } })
    expect(screen.getByText('没有匹配的白名单')).toBeInTheDocument()
    expect(input).toHaveFocus()
  })

  it('资料放行：按 uid / 机器人 / 原因过滤，输入焦点不丢', async () => {
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '资料放行' }))

    const input = screen.getByLabelText('搜索资料放行')
    input.focus()

    fireEvent.change(input, { target: { value: '666' } })
    expect(screen.getByText('666 · 门神小助手')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '门神' } })
    expect(screen.getByText('666 · 门神小助手')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '资料没问题' } })
    expect(screen.getByText('666 · 门神小助手')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: 'zzz' } })
    expect(screen.queryByText('666 · 门神小助手')).not.toBeInTheDocument()
    expect(screen.getByText('没有资料放行')).toBeInTheDocument()
    expect(input).toHaveFocus()
  })

  it('全局组：按 uid / 原因过滤，输入焦点不丢', async () => {
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '联合封禁' }))

    const input = screen.getByLabelText('搜索全局组')
    input.focus()

    fireEvent.change(input, { target: { value: '999' } })
    expect(screen.getByText('999 · （mock）发广告')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '发广告' } })
    expect(screen.getByText('999 · （mock）发广告')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '888' } })
    expect(screen.queryByText('999 · （mock）发广告')).not.toBeInTheDocument()
    expect(screen.getByText('没有匹配的名单条目')).toBeInTheDocument()
    expect(input).toHaveFocus()
  })

  it('专属组：按 uid / 原因过滤，输入焦点不丢', async () => {
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '联合封禁' }))
    fireEvent.click(screen.getByRole('button', { name: '我的专属组' }))

    const input = screen.getByLabelText('搜索专属组')
    input.focus()

    fireEvent.change(input, { target: { value: '888' } })
    expect(screen.getByText('888 · （mock）专属组')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '专属组' } })
    expect(screen.getByText('888 · （mock）专属组')).toBeInTheDocument()
    expect(input).toHaveFocus()

    fireEvent.change(input, { target: { value: '999' } })
    expect(screen.queryByText('888 · （mock）专属组')).not.toBeInTheDocument()
    expect(screen.getByText('没有匹配的名单条目')).toBeInTheDocument()
    expect(input).toHaveFocus()
  })
})

describe('ListsPage 写操作参数', () => {
  it('白名单：添加（bot/user_id/chat_id/hours）与移除参数', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('whitelist', bodies)
    await renderMain()

    fireEvent.click(screen.getByRole('button', { name: '新增' }))
    expect(await screen.findByText('加入白名单')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('user_id'), { target: { value: '777' } })
    fireEvent.change(screen.getByLabelText('chat_id（0 = 该 bot 所有群）'), {
      target: { value: '-100555' },
    })
    fireEvent.change(screen.getByLabelText('小时（留空 = 永久）'), { target: { value: '24' } })
    fireEvent.click(screen.getByRole('button', { name: '加入' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'add',
      bot_id: 1,
      chat_id: -100555,
      user_id: '777',
      hours: 24,
    })

    // 抽屉成功后关闭，列表里的移除按钮直发删除参数
    // （Drawer 退场动画期间页面被 aria-hidden，role 查询要等它卸载）
    fireEvent.click((await screen.findAllByRole('button', { name: '移除' }))[0])
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ action: 'remove', bot_id: 1, chat_id: 0, user_id: 555 })
  })

  it('资料放行：撤销走 whitelist unprofile', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('whitelist', bodies)
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '资料放行' }))

    fireEvent.click(await screen.findByRole('button', { name: '撤销' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'unprofile', bot_id: 1, user_id: 666 })
  })

  it('全局组：添加参数 + 解除前确认（写明 uid），确认后提交 remove', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('gban', bodies)
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '联合封禁' }))

    fireEvent.click(screen.getByRole('button', { name: '新增' }))
    expect(await screen.findByText('加入全局联合封禁组')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('user_id'), { target: { value: '321' } })
    fireEvent.change(screen.getByLabelText('原因（可留空）'), { target: { value: '手工拉黑' } })
    fireEvent.click(screen.getByRole('button', { name: '加入' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'add', user_id: '321', reason: '手工拉黑' })

    fireEvent.click(await screen.findByRole('button', { name: '解除' }))
    expect(await screen.findByText('解除联合封禁？')).toBeInTheDocument()
    expect(screen.getByText(/将把 uid 999 从全局联合封禁名单移除/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '确认解除' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ action: 'remove', user_id: 999 })
  })

  it('专属组：启用开关、生效群勾选、名单增删参数', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('gbanown', bodies)
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '联合封禁' }))
    fireEvent.click(screen.getByRole('button', { name: '我的专属组' }))

    // 启用开关：乐观更新，提交 on=false
    fireEvent.click(screen.getByRole('switch', { name: '启用' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'enable', on: false })

    // 生效群：勾上未圈定的「演练群」
    fireEvent.click(screen.getByRole('switch', { name: '演练群' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ action: 'chat', chat_id: -1009876543210, on: true })

    // 名单添加
    fireEvent.click(screen.getByRole('button', { name: '新增' }))
    expect(await screen.findByText('加入我的专属组')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('user_id'), { target: { value: '4321' } })
    fireEvent.change(screen.getByLabelText('原因（可留空）'), { target: { value: '专属拉黑' } })
    fireEvent.click(screen.getByRole('button', { name: '加入' }))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toEqual({ action: 'add', user_id: '4321', reason: '专属拉黑' })

    // 名单移除（等抽屉退场后再查列表按钮）
    fireEvent.click(await screen.findByRole('button', { name: '移除' }))
    await waitFor(() => expect(bodies).toHaveLength(4))
    expect(bodies[3]).toEqual({ action: 'remove', user_id: 888 })
  })

  it('次级管理员：专属组启用开关与生效群也走 gbanown', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('gbanown', bodies)
    useState(
      stateWith({
        me: { uid: 200, main: false },
        bots: mockState.bots.map((b) => ({ ...b, owner_id: 200 })),
      }),
    )
    renderPage(<ListsPage />)
    await screen.findByText('全局联合封禁组')

    fireEvent.click(screen.getByRole('button', { name: '我的专属组' }))
    fireEvent.click(screen.getByRole('switch', { name: '测试群' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'chat', chat_id: -1001234567890, on: false })
  })

  it('次级管理员：添加管理员/移除参数', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('admin', bodies)
    await renderMain()
    fireEvent.click(screen.getByRole('button', { name: '次级管理员' }))

    fireEvent.click(screen.getByRole('button', { name: '新增' }))
    expect(await screen.findByText('添加次级管理员')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('user_id'), { target: { value: '300' } })
    fireEvent.change(screen.getByLabelText('备注（可留空）'), { target: { value: '值班号' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'add', user_id: '300', note: '值班号' })

    const row = screen.getByText('（mock）备用管理员').closest('li')
    expect(row).not.toBeNull()
    fireEvent.click(
      await within(row as HTMLElement).findByRole('button', { name: '移除' }),
    )
    expect(await screen.findByText('移除次级管理员？')).toBeInTheDocument()
    expect(screen.getByText(/将移除 uid 200/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '确认移除' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ action: 'remove', user_id: 200 })
  })
})
