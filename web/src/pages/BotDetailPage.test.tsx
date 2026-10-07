// 机器人详情：参数覆盖视图（只显示覆盖项/非 spec 键不渲染/添加与恢复走 set）、
// 启用开关的乐观更新失败回滚、is_main 例外与 `管理其群组` 跳转。
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { BotDetailPage } from './BotDetailPage'

startTestServer()

function stateWith(overrides: Partial<State>): State {
  return { ...structuredClone(mockState), ...overrides }
}

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

function capturePost(op: string, bodies: Record<string, unknown>[], note?: string) {
  server.use(
    http.post(`*/miniapp/api/${op}`, async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>)
      return HttpResponse.json({ ok: true, ...(note ? { note } : {}) })
    }),
  )
}

describe('BotDetailPage', () => {
  it('参数覆盖只显示覆盖项，非 spec 键绝不渲染', async () => {
    useState(
      stateWith({
        bot_settings: {
          '2': {
            antiad_mute_minutes: '0',
            // 非 spec 键：不在设置页里，不应渲染（走 set 必然 400）
            antiad_exempt_users: '[1,2]',
            antiad_alert_last_id: '99',
          },
        },
      }),
    )
    renderPage(<BotDetailPage botId={2} />)

    expect(await screen.findByText('禁言时长（分钟）')).toBeInTheDocument()
    expect(screen.getByText('0 分钟（永久）')).toBeInTheDocument()
    // 只有一行覆盖项，每行一个 `恢复全局`
    expect(screen.getAllByText('恢复全局')).toHaveLength(1)
    // 未覆盖的 spec 不出现
    expect(screen.queryByText('并发模式持续（分钟）')).not.toBeInTheDocument()
    expect(screen.queryByText('进群冷判定')).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain('antiad_exempt_users')
    expect(document.body.textContent).not.toContain('antiad_alert_last_id')
  })

  it('添加参数覆盖：抽屉只列 antiad/both，提交 set(scope=bot) 且档位按 min/max 过滤', async () => {
    useState(stateWith({ bot_settings: { '2': { antiad_mute_minutes: '30' } } }))
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    renderPage(<BotDetailPage botId={2} />)

    fireEvent.click(await screen.findByText('＋ 添加参数覆盖'))
    // log_retention_days 不在 antiad/both 里，不应出现
    expect(screen.queryByText('记录保留天数')).not.toBeInTheDocument()
    fireEvent.click(await screen.findByText('并发模式持续（分钟）'))

    expect(await screen.findByText('设置：并发模式持续（分钟）')).toBeInTheDocument()
    // min=1,max=1440：永久/7 天档位被过滤
    expect(screen.getByText('1 天')).toBeInTheDocument()
    expect(screen.queryByText('永久')).not.toBeInTheDocument()
    expect(screen.queryByText('7 天')).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('并发模式持续（分钟）'), { target: { value: '120' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      scope: 'bot',
      bot_id: 2,
      key: 'antiad_hedge_minutes',
      value: '120',
    })
  })

  it('恢复全局：set 传空串', async () => {
    useState(stateWith({ bot_settings: { '2': { antiad_mute_minutes: '30' } } }))
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    renderPage(<BotDetailPage botId={2} />)

    fireEvent.click(await screen.findByText('恢复全局'))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ scope: 'bot', bot_id: 2, key: 'antiad_mute_minutes', value: '' })
    // 服务端 note 优先展示
    expect(await screen.findByText('已恢复全局')).toBeInTheDocument()
  })

  it('启用开关乐观翻转，服务端 500 时回滚并 toast', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.post('*/miniapp/api/bot', async () => {
        await gate
        return HttpResponse.json({ error: '注册表不可用' }, { status: 500 })
      }),
    )
    renderPage(<BotDetailPage botId={2} />)

    const sw = await screen.findByRole('switch', { name: '启用' })
    expect(sw).not.toBeChecked()
    expect(screen.getByText('已停用')).toBeInTheDocument()

    fireEvent.click(sw)
    // 乐观：服务端响应被 gate 拦住时，UI 已经翻转
    await waitFor(() => expect(sw).toBeChecked())
    expect(screen.getByText('未运行')).toBeInTheDocument()

    act(() => release())
    await waitFor(() => expect(screen.getByRole('switch', { name: '启用' })).not.toBeChecked())
    expect(screen.getByText('已停用')).toBeInTheDocument()
    expect(await screen.findByText('注册表不可用')).toBeInTheDocument()
  })

  it('主 bot：显示徽标、不改派、无移除入口；「管理其群组」带 bot 意图切群组页', async () => {
    renderPage(
      <>
        <BotDetailPage botId={1} />
        <NavProbe />
      </>,
    )

    expect(await screen.findByText('主 bot')).toBeInTheDocument()
    expect(screen.queryByText('移除该 bot')).not.toBeInTheDocument()
    expect(screen.getByText('主 bot 的归属由配置文件决定，不能改派')).toBeInTheDocument()
    expect(screen.getByText('2 个')).toBeInTheDocument()

    fireEvent.click(screen.getByText('管理其群组'))
    expect(screen.getByTestId('nav-tab').textContent).toBe('chats')
    expect(screen.getByTestId('nav-intent').textContent).toBe('bot:1')
  })

  it('非主 bot 主管理员可改派归属并移除（确认文案含对象名与后果）', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('bot', bodies)
    renderPage(<BotDetailPage botId={2} />)

    fireEvent.click(await screen.findByText('归属'))
    fireEvent.click(await screen.findByRole('button', { name: '保存' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ bot_id: 2, action: 'owner', owner_id: 100 })

    fireEvent.click(screen.getByText('移除该 bot'))
    expect(await screen.findByText('移除该 bot？')).toBeInTheDocument()
    expect(screen.getByText(/将删除「演示机器人」的群配置与阈值/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ bot_id: 2, action: 'remove' })
  })

  it('模型抽屉：勾选已启用模型并提交 bot action=models(which=so)', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('bot', bodies)
    renderPage(<BotDetailPage botId={1} />)

    expect(await screen.findByText('判定模型')).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole('button', { name: '编辑' })[0])
    // fixtures：bot 1 无模型覆盖（卡片显示 `跟随全局`），快选 chip 唯一
    fireEvent.click(await screen.findByText('demo/gpt-5-mini'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      bot_id: 1,
      action: 'models',
      which: 'so',
      value: 'demo/gpt-5-mini',
    })
  })

  it('次级管理员：模型卡只读，没有编辑入口', async () => {
    useState(
      stateWith({
        me: { uid: 200, main: false },
        bots: mockState.bots.map((b) => ({ ...b, owner_id: 200 })),
      }),
    )
    renderPage(<BotDetailPage botId={2} />)

    expect(await screen.findByText('判定模型')).toBeInTheDocument()
    expect(screen.getAllByText('由主管理员配置')).toHaveLength(2)
    expect(screen.queryByRole('button', { name: '编辑' })).not.toBeInTheDocument()
  })

  it('恢复全局（空输入）时 0 档不高亮，输入 0 才高亮', async () => {
    useState(stateWith({ bot_settings: { '2': { antiad_mute_minutes: '30' } } }))
    renderPage(<BotDetailPage botId={2} />)

    fireEvent.click(await screen.findByRole('button', { name: '30' }))
    expect(await screen.findByText('设置：禁言时长（分钟）')).toBeInTheDocument()
    expect(screen.getByTestId('preset-0')).not.toHaveAttribute('data-selected', 'true')

    const input = screen.getByLabelText('禁言时长（分钟）')
    fireEvent.change(input, { target: { value: '' } })
    expect(screen.getByTestId('preset-0')).not.toHaveAttribute('data-selected', 'true')

    fireEvent.change(input, { target: { value: '0' } })
    expect(screen.getByTestId('preset-0')).toHaveAttribute('data-selected', 'true')
  })
})
