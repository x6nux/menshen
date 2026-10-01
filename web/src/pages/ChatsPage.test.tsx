// 群组列表：搜索（标题/chat_id/bot）、intent 过滤与清除、添加群抽屉、
// 批量模式的同 bot 约束与 bulk_update 参数。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { useNav } from '../nav'
import { IntentSetter, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { ChatsPage } from './ChatsPage'

startTestServer()

function captureChat(bodies: Record<string, unknown>[], note?: string) {
  server.use(
    http.post('*/miniapp/api/chat', async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>)
      return HttpResponse.json({ ok: true, ...(note ? { note } : {}) })
    }),
  )
}

/** IntentChanger 点击后切换带意图的 tab，用于验证意图变化时的选中清理。 */
function IntentChanger() {
  const nav = useNav()
  return (
    <button type="button" onClick={() => nav.switchTab('chats', 'dryrun')}>
      intent-dryrun
    </button>
  )
}

describe('ChatsPage', () => {
  it('本地搜索按群名/chat_id/机器人名过滤', async () => {
    renderPage(<ChatsPage />)

    expect(await screen.findByText('测试群')).toBeInTheDocument()
    expect(screen.getByText('演练群')).toBeInTheDocument()
    expect(screen.getByText('第二个群')).toBeInTheDocument()

    const input = screen.getByLabelText('搜索群组')

    fireEvent.change(input, { target: { value: '演练' } })
    expect(screen.getByText('演练群')).toBeInTheDocument()
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()

    fireEvent.change(input, { target: { value: '-1001111' } })
    expect(screen.getByText('第二个群')).toBeInTheDocument()
    expect(screen.queryByText('演练群')).not.toBeInTheDocument()

    fireEvent.change(input, { target: { value: '门神小助手' } })
    expect(screen.getByText('测试群')).toBeInTheDocument()
    expect(screen.getByText('第二个群')).toBeInTheDocument()
    expect(screen.queryByText('演练群')).not.toBeInTheDocument()
  })

  it('intent 带 bot 过滤：显示过滤条，可清除', async () => {
    renderPage(
      <>
        <ChatsPage />
        <IntentSetter tab="chats" intent="bot:2" />
      </>,
    )

    expect(await screen.findByText('只看：演示机器人 的群')).toBeInTheDocument()
    expect(screen.getByText('演练群')).toBeInTheDocument()
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }))
    await waitFor(() => expect(screen.getByText('测试群')).toBeInTheDocument())
    expect(screen.queryByText('只看：演示机器人 的群')).not.toBeInTheDocument()
  })

  it('添加群：提交 bot_id/chat_id/action，成功 toast 服务端 note', async () => {
    const bodies: Record<string, unknown>[] = []
    captureChat(bodies, '已添加，默认演练')
    renderPage(<ChatsPage />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '添加群' }))
    expect(await screen.findByText(/添加后默认「演练」/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('群 ID'), { target: { value: '-1005555555555' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ bot_id: 1, chat_id: '-1005555555555', action: 'add' })
    expect(await screen.findByText('已添加，默认演练')).toBeInTheDocument()
  })

  it('批量：同 bot 两个群提交 bot_id/chat_ids/fields，成功后退出批量', async () => {
    const bodies: Record<string, unknown>[] = []
    captureChat(bodies, '已更新 2 个群')
    renderPage(<ChatsPage />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    fireEvent.click(screen.getByText('第二个群'))
    expect(screen.getByText(/已选 2 个群/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '启用' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'bulk_update',
      bot_id: 1,
      chat_ids: [-1001234567890, -1001111111111],
      fields: { enabled: true },
    })
    expect(await screen.findByText('已更新 2 个群')).toBeInTheDocument()
    // 成功后退出批量模式
    expect(screen.queryByText(/已选 2 个群/)).not.toBeInTheDocument()
  })

  it('批量：跨 bot 选择被忽略并 toast 说明', async () => {
    renderPage(<ChatsPage />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    fireEvent.click(screen.getByText('演练群'))

    expect(screen.getByText(/已选 1 个群/)).toBeInTheDocument()
    expect(
      await screen.findByText('一次只能批量管理同一个机器人的群，请先取消已选中的群'),
    ).toBeInTheDocument()
  })

  it('批量处罚方式：底部抽屉选择后提交 punish', async () => {
    const bodies: Record<string, unknown>[] = []
    captureChat(bodies)
    renderPage(<ChatsPage />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    fireEvent.click(screen.getByRole('button', { name: '处罚方式' }))

    expect(await screen.findByText('批量设置处罚方式')).toBeInTheDocument()
    fireEvent.click(screen.getByText('封禁出群（永久）'))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'bulk_update',
      bot_id: 1,
      chat_ids: [-1001234567890],
      fields: { punish: 1 },
    })
  })

  it('intent=dryrun 预置演练过滤，只显示演练中的群', async () => {
    renderPage(
      <>
        <ChatsPage />
        <IntentSetter tab="chats" intent="dryrun" />
      </>,
    )

    expect(await screen.findByText('只看：演练中的群')).toBeInTheDocument()
    expect(screen.getByText('演练群')).toBeInTheDocument()
    expect(screen.getByText('第二个群')).toBeInTheDocument()
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()
  })

  it('搜索变化时清理不可见的批量选中项', async () => {
    renderPage(<ChatsPage />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    expect(screen.getByText(/已选 1 个群/)).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('搜索群组'), { target: { value: '演练' } })
    await waitFor(() => expect(screen.getByText(/已选 0 个群/)).toBeInTheDocument())
    // 被过滤掉的群不应保持勾选状态
    expect(screen.getByRole('checkbox', { name: '选择 演练群' })).not.toBeChecked()
  })

  it('意图变化时清理不可见的批量选中项', async () => {
    renderPage(
      <>
        <ChatsPage />
        <IntentChanger />
      </>,
    )

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    expect(screen.getByText(/已选 1 个群/)).toBeInTheDocument()

    fireEvent.click(screen.getByText('intent-dryrun'))
    await waitFor(() => expect(screen.getByText(/已选 0 个群/)).toBeInTheDocument())
    expect(screen.getByText('只看：演练中的群')).toBeInTheDocument()
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()
  })

  it('批量选满上限后拒绝继续选择并 toast', async () => {
    renderPage(<ChatsPage bulkLimit={2} />)

    await screen.findByText('测试群')
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    fireEvent.click(screen.getByText('测试群'))
    fireEvent.click(screen.getByText('第二个群'))
    expect(screen.getByText(/已选 2 个群/)).toBeInTheDocument()

    fireEvent.click(screen.getByText('演练群'))
    expect(await screen.findByText('一次最多批量管理 2 个群')).toBeInTheDocument()
    expect(screen.getByText(/已选 2 个群/)).toBeInTheDocument()
  })

  it('批量模式为操作条预留底部高度（data-reserved）', async () => {
    renderPage(<ChatsPage />)
    await screen.findByText('测试群')
    expect(screen.getByTestId('chats-page')).toHaveAttribute('data-reserved', '0')

    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    expect(screen.getByTestId('batch-bar')).toBeInTheDocument()
    const reserved = Number(screen.getByTestId('chats-page').getAttribute('data-reserved'))
    expect(reserved).toBeGreaterThanOrEqual(140)
  })
})
