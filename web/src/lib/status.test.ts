import { describe, expect, it } from 'vitest'
import { appealStatusInfo, appealSummary, botStatus, chatStatus, verdictInfo } from './status'

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

describe('verdictInfo', () => {
  it('命中红、其余绿，文案走 verdictLabel', () => {
    expect(verdictInfo('ad')).toEqual({ label: '广告', tone: 'no' })
    expect(verdictInfo('clean')).toEqual({ label: '正常', tone: 'ok' })
    expect(verdictInfo('none')).toEqual({ label: '正常', tone: 'ok' })
    expect(verdictInfo('skipped')).toEqual({ label: '未送检', tone: 'ok' })
  })
})

describe('appealStatusInfo', () => {
  it('已解除/已兑换绿、已驳回红、其余中性', () => {
    expect(appealStatusInfo('lifted')).toEqual({ label: '已解除', tone: 'ok' })
    expect(appealStatusInfo('redeemed')).toEqual({ label: '已兑换', tone: 'ok' })
    expect(appealStatusInfo('rejected')).toEqual({ label: '已驳回', tone: 'no' })
    expect(appealStatusInfo('noweb')).toEqual({ label: '待人工处理', tone: 'neutral' })
    expect(appealStatusInfo('ai')).toEqual({ label: 'AI 复核中', tone: 'neutral' })
  })
})

describe('appealSummary', () => {
  it('结论 + 40 字理由摘要；无理由只留结论', () => {
    expect(appealSummary('uphold', '')).toBe('维持原判')
    expect(appealSummary('overturn', 'x'.repeat(50))).toBe(`撤销原判 · ${'x'.repeat(40)}`)
  })
})
