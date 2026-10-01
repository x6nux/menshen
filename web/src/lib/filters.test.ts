import { describe, expect, it } from 'vitest'
import { filterRows, gbanKey, matchQuery, profileOKKey, whiteKey } from './filters'

const bans = [
  { user_id: 1001, reason: '发广告', src_chat: 0, created_at: 0 },
  { user_id: 1002, reason: 'Scam Link', src_chat: -100, created_at: 0 },
]

describe('matchQuery / filterRows', () => {
  it('空查询返回全部', () => {
    expect(filterRows(bans, '', gbanKey)).toEqual(bans)
    expect(filterRows(bans, '   ', gbanKey)).toEqual(bans)
    expect(matchQuery('anything', '')).toBe(true)
  })

  it('大小写不敏感，中文可搜', () => {
    expect(filterRows(bans, '广告', gbanKey).map((b) => b.user_id)).toEqual([1001])
    expect(filterRows(bans, 'scam', gbanKey).map((b) => b.user_id)).toEqual([1002])
    expect(filterRows(bans, '1001', gbanKey).map((b) => b.user_id)).toEqual([1001])
    expect(filterRows(bans, '不存在', gbanKey)).toEqual([])
  })
})

describe('名单 key', () => {
  it('gbanKey 拼 uid 与原因', () => {
    expect(gbanKey({ user_id: 9, reason: '' })).toBe('9 ')
    expect(gbanKey({ user_id: 9, reason: '广告' })).toBe('9 广告')
  })

  it('whiteKey 拼 uid/来源/bot/群，来源大小写不敏感', () => {
    const rows = [
      { user_id: 7, source: 'adw', bot_id: 3, chat_id: -100, expires_at: 0, by_uid: 1 },
      { user_id: 8, source: 'miniapp', bot_id: 0, chat_id: 0, expires_at: 0, by_uid: 1 },
    ]
    expect(whiteKey(rows[0])).toBe('7 adw 3 -100')
    expect(filterRows(rows, 'ADW', whiteKey)).toHaveLength(1)
    expect(filterRows(rows, '-100', whiteKey)).toHaveLength(1)
    expect(filterRows(rows, 'miniapp', whiteKey)[0].user_id).toBe(8)
  })

  it('profileOKKey 用 bot 展示名参与匹配', () => {
    const rows = [
      { bot_id: 3, user_id: 7, hours: 24, reason: '资料没问题', created_at: 0, expires_at: 0 },
    ]
    const botLabel = (id: number) => (id === 3 ? '门神小助手' : `bot ${id}`)
    expect(profileOKKey(rows[0], botLabel)).toBe('7 门神小助手 资料没问题')
    expect(filterRows(rows, '小助手', (p) => profileOKKey(p, botLabel))).toHaveLength(1)
    expect(filterRows(rows, 'bot 9', (p) => profileOKKey(p, botLabel))).toHaveLength(0)
  })
})
