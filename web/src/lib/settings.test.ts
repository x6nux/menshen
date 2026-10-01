import { describe, expect, it } from 'vitest'
import type { Spec } from '../api/types'
import {
  controlKind,
  minutePresets,
  overrideKeys,
  specHint,
  specUnit,
  specValueText,
} from './settings'

function spec(partial: Partial<Spec> & Pick<Spec, 'key'>): Spec {
  return {
    label: partial.key,
    hint: '',
    min: 0,
    max: 0,
    group: '',
    ...partial,
  }
}

describe('controlKind', () => {
  it('0/1 是开关，其余是数字输入', () => {
    expect(controlKind(spec({ key: 'antiad_cold', min: 0, max: 1, group: 'both' }))).toBe('toggle')
    expect(controlKind(spec({ key: 'antiad_so_trust', min: 0, max: 100, group: 'antiad' }))).toBe('number')
    expect(controlKind(spec({ key: 'antiad_mute_minutes', min: 0, max: 0, group: 'antiad' }))).toBe('number')
    expect(controlKind(spec({ key: 'antiad_hedge_minutes', min: 1, max: 1440, group: 'both' }))).toBe('number')
  })
})

describe('minutePresets', () => {
  it('无上限才有永久与 7 天', () => {
    const mute = spec({ key: 'antiad_mute_minutes', min: 0, max: 0, group: 'antiad' })
    expect(minutePresets(mute).map((p) => p.value)).toEqual([0, 10, 60, 1440, 10080])
    expect(minutePresets(mute)[0].label).toBe('永久')
  })

  it('min=1,max=1440 过滤掉永久与 7 天', () => {
    const hedge = spec({ key: 'antiad_hedge_minutes', min: 1, max: 1440, group: 'both' })
    expect(minutePresets(hedge).map((p) => p.value)).toEqual([10, 60, 1440])
  })

  it('上限小于 1 小时时进一步收窄', () => {
    const tight = spec({ key: 'custom_minutes', min: 1, max: 30 })
    expect(minutePresets(tight)).toEqual([{ label: '10 分钟', value: 10 }])
  })
})

describe('overrideKeys', () => {
  const specs = [
    spec({ key: 'antiad_mute_minutes', group: 'antiad' }),
    spec({ key: 'antiad_cold', group: 'both' }),
    spec({ key: 'gban_global', group: 'antiad' }),
    spec({ key: 'log_retention_days', group: '' }),
  ]

  it('只保留 antiad/both 且显式覆盖的 spec 键', () => {
    const overrides = {
      antiad_mute_minutes: '30',
      gban_global: '1',
      // 非 spec 键：不在 specs 里，必须被排除
      antiad_exempt_users: '[1,2]',
      antiad_alert_last_id: '99',
      // 全局项：不能按 bot 覆盖
      log_retention_days: '7',
    }
    expect(overrideKeys(specs, overrides).map((s) => s.key)).toEqual([
      'antiad_mute_minutes',
      'gban_global',
    ])
  })

  it('值为空串视为未覆盖', () => {
    expect(overrideKeys(specs, { antiad_cold: '' })).toEqual([])
  })

  it('无覆盖表返回空数组', () => {
    expect(overrideKeys(specs, undefined)).toEqual([])
    expect(overrideKeys(specs, null)).toEqual([])
    expect(overrideKeys(specs, {})).toEqual([])
  })
})

describe('specUnit / specHint / specValueText', () => {
  it('按键名推断单位', () => {
    expect(specUnit(spec({ key: 'antiad_mute_minutes' }))).toBe('分钟')
    expect(specUnit(spec({ key: 'antiad_so_timeout_ms' }))).toBe('毫秒')
    expect(specUnit(spec({ key: 'antiad_new_hours' }))).toBe('小时')
    expect(specUnit(spec({ key: 'antiad_alert_ttl' }))).toBe('秒')
    expect(specUnit(spec({ key: 'antiad_alert_rpm' }))).toBe('次/分钟')
    expect(specUnit(spec({ key: 'antiad_so_trust' }))).toBe('')
  })

  it('hint 缺单位时补上，已有则不动', () => {
    expect(specHint(spec({ key: 'x_hours', hint: '正整数' }))).toBe('正整数（单位：小时）')
    expect(specHint(spec({ key: 'x_minutes', hint: '0 = 永久' }))).toBe('0 = 永久（单位：分钟）')
    expect(specHint(spec({ key: 'x_minutes', hint: '按分钟计' }))).toBe('按分钟计')
  })

  it('时长值给出换算文案', () => {
    const mute = spec({ key: 'antiad_mute_minutes' })
    expect(specValueText(60, mute)).toBe('60 分钟（= 1 小时）')
    expect(specValueText(1440, mute)).toBe('1440 分钟（= 1 天）')
    expect(specValueText(90, mute)).toBe('90')
    expect(specValueText(0, mute)).toBe('0 分钟（永久）')
    expect(specValueText('12', spec({ key: 'antiad_so_trust' }))).toBe('12')
  })
})
