import { describe, expect, it } from 'vitest'
import { BULK_CHAT_LIMIT, toggleChatSelection } from './selection'

interface Row {
  bot_id: number
  chat_id: number
}

const same = (a: Row, b: Row) => a.bot_id === b.bot_id && a.chat_id === b.chat_id
const botOf = (row: Row) => row.bot_id

function rows(botID: number, count: number, start = 0): Row[] {
  return Array.from({ length: count }, (_, i) => ({ bot_id: botID, chat_id: -(start + i + 1) }))
}

describe('toggleChatSelection', () => {
  it('切换已选中项（取消永远允许，即使已到上限）', () => {
    const selected = rows(1, 2)
    const outcome = toggleChatSelection(selected, selected[0], same, botOf, 2)
    expect(outcome.rejected).toBeUndefined()
    expect(outcome.selected).toEqual([selected[1]])

    const full = rows(1, 2)
    const off = toggleChatSelection(full, full[1], same, botOf, 2)
    expect(off.selected).toEqual([full[0]])
  })

  it('同 bot 正常加入，跨 bot 拒绝', () => {
    const selected = rows(1, 1)
    const ok = toggleChatSelection(selected, { bot_id: 1, chat_id: -99 }, same, botOf)
    expect(ok.selected).toHaveLength(2)

    const bad = toggleChatSelection(selected, { bot_id: 2, chat_id: -100 }, same, botOf)
    expect(bad.rejected).toBe('bot')
    expect(bad.selected).toEqual(selected)
  })

  it('选满上限拒绝继续加入，取消一个后可再加', () => {
    const full = rows(1, BULK_CHAT_LIMIT)
    const extra = { bot_id: 1, chat_id: -999999 }
    const rejected = toggleChatSelection(full, extra, same, botOf)
    expect(rejected.rejected).toBe('limit')
    expect(rejected.selected).toHaveLength(BULK_CHAT_LIMIT)

    const freed = toggleChatSelection(full, full[0], same, botOf)
    const added = toggleChatSelection(freed.selected, extra, same, botOf)
    expect(added.rejected).toBeUndefined()
    expect(added.selected).toHaveLength(BULK_CHAT_LIMIT)
    expect(added.selected.at(-1)).toEqual(extra)
  })

  it('自定义 limit 生效（测试注入用）', () => {
    const selected = rows(1, 2)
    expect(toggleChatSelection(selected, { bot_id: 1, chat_id: -3 }, same, botOf, 2).rejected).toBe(
      'limit',
    )
  })
})
