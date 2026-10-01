// 群组详情：5.1 迁移不变量（实际执行 + 永久禁言/封禁出群）、处罚切换、
// 补全入群时间（服务端 note）、移除确认（含对象名与后果）。
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { ChatDetailPage } from './ChatDetailPage'

startTestServer()

const BOT_ID = 2
const CHAT_ID = -1009876543210

/** 迁移自旧 Go 测试：bot 禁言时长为 0（永久）时，群详情必须说清实际执行。 */
function punishState(): State {
  const state = structuredClone(mockState)
  state.bot_settings = { [String(BOT_ID)]: { antiad_mute_minutes: '0' } }
  state.chats = state.chats.map((c) =>
    c.bot_id === BOT_ID && c.chat_id === CHAT_ID ? { ...c, punish: 0 } : c,
  )
  return state
}

describe('ChatDetailPage', () => {
  it('实际执行：跟随档位显示永久禁言；切成封禁出群后显示封禁出群（永久）', async () => {
    let current = punishState()
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/state', () => HttpResponse.json(current)),
      http.post('*/miniapp/api/chat', async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>
        bodies.push(body)
        if (body.action === 'update' && 'punish' in body) {
          current = {
            ...current,
            chats: current.chats.map((c) =>
              c.bot_id === BOT_ID && c.chat_id === CHAT_ID
                ? { ...c, punish: Number(body.punish) }
                : c,
            ),
          }
        }
        return HttpResponse.json({ ok: true })
      }),
    )

    renderPage(<ChatDetailPage botId={BOT_ID} chatId={CHAT_ID} />)

    expect(await screen.findByText('实际执行')).toBeInTheDocument()
    expect(screen.getByTestId('effective-punish')).toHaveTextContent('永久禁言')
    // 处罚方式选择器含「跟随 bot 设置」
    fireEvent.mouseDown(screen.getByRole('combobox', { name: '处罚方式' }))
    expect(await screen.findByRole('option', { name: '跟随 bot 设置' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('option', { name: '封禁出群（永久）' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toMatchObject({
      action: 'update',
      bot_id: BOT_ID,
      chat_id: CHAT_ID,
      punish: 1,
    })
    await waitFor(() =>
      expect(screen.getByTestId('effective-punish')).toHaveTextContent('封禁出群（永久）'),
    )
  })

  it('显示防错提示，补全按钮成功后 toast 服务端 note', async () => {
    const note = '已开始补全，结果会私聊发给管理员（大群可能要几分钟）'
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/chat', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true, note })
      }),
    )

    renderPage(<ChatDetailPage botId={BOT_ID} chatId={CHAT_ID} />)

    // fixtures：bot 2 覆盖 antiad_mute_minutes=30 → 实际执行「禁言 30 分钟」
    expect(await screen.findByText('实际执行')).toBeInTheDocument()
    expect(screen.getByTestId('effective-punish')).toHaveTextContent('禁言 30 分钟')
    expect(screen.getByText(/要改成永久禁言/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '补全历史入群时间' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'backfill', bot_id: BOT_ID, chat_id: CHAT_ID })
    expect(await screen.findByText(note)).toBeInTheDocument()
  })

  it('移除该群：确认面板含对象名与后果，确认后提交并返回', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/chat', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true })
      }),
    )
    renderPage(
      <>
        <ChatDetailPage botId={BOT_ID} chatId={CHAT_ID} />
        <NavProbe />
      </>,
    )

    fireEvent.click(await screen.findByText('移除该群'))
    expect(await screen.findByText('移除该群？')).toBeInTheDocument()
    expect(screen.getByText(/将从「演练群」停止判定与处置/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'remove', bot_id: BOT_ID, chat_id: CHAT_ID })
    // 成功后 pop 回列表（此处栈本来就是空的，pop 不越界即可）
    expect(screen.getByTestId('nav-top').textContent).toBe('')
  })

  it('启用判定开关乐观翻转，失败回滚并 toast', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.post('*/miniapp/api/chat', async () => {
        await gate
        return HttpResponse.json({ error: '注册表不可用' }, { status: 500 })
      }),
    )
    renderPage(<ChatDetailPage botId={BOT_ID} chatId={CHAT_ID} />)

    // fixtures：演练群 enabled=false
    const sw = await screen.findByRole('switch', { name: '启用判定' })
    expect(sw).not.toBeChecked()
    fireEvent.click(sw)
    // 乐观：服务端响应被 gate 拦住时，UI 已经翻转
    await waitFor(() => expect(sw).toBeChecked())

    act(() => release())
    await waitFor(() => expect(screen.getByRole('switch', { name: '启用判定' })).not.toBeChecked())
    expect(await screen.findByText('注册表不可用')).toBeInTheDocument()
  })
})
