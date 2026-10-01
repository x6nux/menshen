import { describe, expect, it } from 'vitest'
import {
  actionLabel,
  apAI,
  apStatus,
  fmtTS,
  kindLabel,
  muteOptLabel,
  muteText,
  punishLabel,
  settingOf,
  verdictLabel,
} from './format'

// 2023-11-14 00:05:00 UTC —— 同时验证 24 小时制不会出现 24:05。
const TS_MIDNIGHT = 1699920300
// 2023-11-14 22:13:20 UTC
const TS_DAY = 1700000000

describe('fmtTS', () => {
  it('非正数与非法值显示 —', () => {
    expect(fmtTS(0)).toBe('—')
    expect(fmtTS(-1)).toBe('—')
    expect(fmtTS(Number.NaN)).toBe('—')
  })

  it('按时区换算，同一时间戳输出不同', () => {
    expect(fmtTS(TS_DAY, 'UTC')).toBe('11-14 22:13')
    expect(fmtTS(TS_DAY, 'Asia/Shanghai')).toBe('11-15 06:13')
  })

  it('午夜用 h23，不出现 24:xx', () => {
    expect(fmtTS(TS_MIDNIGHT, 'UTC')).toBe('11-14 00:05')
  })

  it('非法时区回退本地时区且不抛错', () => {
    expect(() => fmtTS(TS_MIDNIGHT, 'Not/AZone')).not.toThrow()
    expect(fmtTS(TS_MIDNIGHT, 'Not/AZone')).toBe(fmtTS(TS_MIDNIGHT))
    expect(fmtTS(TS_MIDNIGHT, 'Not/AZone')).toMatch(/^\d{2}-\d{2} \d{2}:\d{2}$/)
  })
})

describe('文案映射', () => {
  it('verdict/kind/apStatus/apAI 与旧实现一致', () => {
    expect(verdictLabel('ad')).toBe('广告')
    expect(verdictLabel('clean')).toBe('正常')
    expect(verdictLabel('skipped')).toBe('未送检')
    expect(verdictLabel('weird')).toBe('weird')
    expect(kindLabel('scam')).toBe('诈骗')
    expect(kindLabel('none')).toBe('未分类')
    expect(apStatus('redeemed')).toBe('已兑换')
    expect(apStatus('unknown')).toBe('unknown')
    expect(apAI('uphold')).toBe('维持原判')
    expect(apAI('whatever')).toBe('未复核')
  })

  it('actionLabel 处理 dryrun: 前缀与未知值', () => {
    expect(actionLabel('deleted_muted')).toBe('删除+禁言')
    expect(actionLabel('dryrun:deleted')).toBe('演练:deleted')
    expect(actionLabel('dryrun:muted')).toBe('演练:muted')
    expect(actionLabel('custom')).toBe('custom')
  })
})

describe('muteText', () => {
  it('0/负数/非数字 = 永久禁言', () => {
    expect(muteText(0)).toBe('永久禁言')
    expect(muteText('0')).toBe('永久禁言')
    expect(muteText(-5)).toBe('永久禁言')
    expect(muteText('abc')).toBe('永久禁言')
  })

  it('整除按天、小时，其余按分钟', () => {
    expect(muteText(1440)).toBe('禁言 1 天')
    expect(muteText(2880)).toBe('禁言 2 天')
    expect(muteText(60)).toBe('禁言 1 小时')
    expect(muteText(90)).toBe('禁言 90 分钟')
    expect(muteText('10080')).toBe('禁言 7 天')
  })
})

describe('settingOf / punishLabel / muteOptLabel', () => {
  const bots = { '7': { antiad_mute_minutes: '30', antiad_ban: '1' } }
  const defaults = { antiad_mute_minutes: '1440', antiad_ban: '0' }

  it('bot 覆盖优先，空串与非 null 判定与旧实现一致', () => {
    expect(settingOf(bots, defaults, 7, 'antiad_mute_minutes')).toBe('30')
    expect(settingOf(bots, defaults, '7', 'antiad_ban')).toBe('1')
    // 未覆盖的键读全局
    expect(settingOf(bots, defaults, 7, 'antiad_so_trust')).toBe('')
    // 空串覆盖视为没有覆盖
    expect(settingOf({ '7': { antiad_mute_minutes: '' } }, defaults, 7, 'antiad_mute_minutes')).toBe('1440')
    // bot 表缺失 / 全局缺失
    expect(settingOf(undefined, defaults, 7, 'antiad_ban')).toBe('0')
    expect(settingOf(bots, undefined, 8, 'antiad_mute_minutes')).toBe('')
  })

  it('punishLabel：跟随看 antiad_ban，禁言看时长', () => {
    expect(punishLabel(bots, defaults, 7, 1)).toBe('封禁出群（永久）')
    expect(punishLabel(bots, defaults, 7, 0)).toBe('禁言 30 分钟')
    // 跟随：bot 覆盖 antiad_ban=1 → 封禁
    expect(punishLabel(bots, defaults, 7, -1)).toBe('封禁出群（永久）')
    // 跟随：未覆盖的 bot → 全局 antiad_ban=0 → 按时长
    expect(punishLabel(bots, defaults, 8, -1)).toBe('禁言 1 天')
    // 未配置时长时按 1 天兜底
    expect(muteOptLabel(undefined, {}, 1)).toBe('禁言 1 天')
  })
})
