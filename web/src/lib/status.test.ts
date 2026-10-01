import { describe, expect, it } from 'vitest'
import { botStatus, chatStatus } from './status'

describe('botStatus', () => {
  it('停用优先，其次看注册表在线状态', () => {
    expect(botStatus({ enabled: false, live: true })).toEqual({ label: '已停用', tone: 'no' })
    expect(botStatus({ enabled: true, live: true })).toEqual({ label: '运行中', tone: 'ok' })
    expect(botStatus({ enabled: true, live: false })).toEqual({ label: '未运行', tone: 'warn' })
  })
})

describe('chatStatus', () => {
  it('演练优先于启用，其次判定中/停用', () => {
    expect(chatStatus({ enabled: false, dryrun: true })).toEqual({ label: '演练', tone: 'warn' })
    expect(chatStatus({ enabled: true, dryrun: false })).toEqual({ label: '判定中', tone: 'ok' })
    expect(chatStatus({ enabled: false, dryrun: false })).toEqual({ label: '停用', tone: 'no' })
  })
})
