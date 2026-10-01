import { describe, expect, it, vi } from 'vitest'
import { botIdFromURL, initTelegram } from './telegram'
import type { TelegramWebAppLike } from './telegram'

function mockWebApp(overrides: Partial<TelegramWebAppLike> = {}) {
  const handlers = new Map<string, (() => void)[]>()
  const calls: string[] = []
  const wa: TelegramWebAppLike = {
    initData: 'query_id=AAA&user=%7B%22id%22%3A1%7D',
    colorScheme: 'dark',
    themeParams: { bg_color: '#17212B' },
    ready: () => calls.push('ready'),
    expand: () => calls.push('expand'),
    onEvent: (event, cb) => {
      const list = handlers.get(event) ?? []
      list.push(cb)
      handlers.set(event, list)
    },
    offEvent: (event, cb) => {
      handlers.set(
        event,
        (handlers.get(event) ?? []).filter((h) => h !== cb),
      )
    },
    openLink: (url) => calls.push('open:' + url),
    BackButton: {
      show: () => calls.push('show'),
      hide: () => calls.push('hide'),
      onClick: (cb) => {
        const list = handlers.get('back') ?? []
        list.push(cb)
        handlers.set('back', list)
      },
    },
    ...overrides,
  }
  return { wa, calls, handlers }
}

describe('botIdFromURL', () => {
  it('读 ?bot=，缺省为 0', () => {
    expect(botIdFromURL('?bot=42')).toBe('42')
    expect(botIdFromURL('')).toBe('0')
    expect(botIdFromURL('?foo=1')).toBe('0')
  })
})

describe('initTelegram', () => {
  it('无 SDK：按 100ms×3 轮询后超时，返回 available=false', async () => {
    const sleep = vi.fn(async () => {})
    const bridge = await initTelegram({
      getWebApp: () => undefined,
      sleep,
      timeoutMs: 300,
      intervalMs: 100,
    })
    expect(bridge.available).toBe(false)
    expect(bridge.initData).toBe('')
    expect(bridge.botId).toBe('0')
    expect(sleep).toHaveBeenCalledTimes(3)
    // 不可用桥的方法也不抛错
    expect(() => bridge.backButton.show()).not.toThrow()
    expect(bridge.onThemeChanged(() => {})).toBeTypeOf('function')
  })

  it('SDK 立即就绪：ready/expand、initData、themeChanged、BackButton、openLink', async () => {
    const { wa, calls, handlers } = mockWebApp()
    const bridge = await initTelegram({ getWebApp: () => wa })

    expect(bridge.available).toBe(true)
    expect(calls).toEqual(expect.arrayContaining(['ready', 'expand']))
    expect(bridge.initData).toBe('query_id=AAA&user=%7B%22id%22%3A1%7D')
    expect(bridge.colorScheme).toBe('dark')
    expect(bridge.themeParams.bg_color).toBe('#17212B')

    // 主题订阅
    const onTheme = vi.fn()
    const offTheme = bridge.onThemeChanged(onTheme)
    expect(handlers.get('themeChanged')).toHaveLength(1)
    handlers.get('themeChanged')?.[0]()
    expect(onTheme).toHaveBeenCalledTimes(1)
    offTheme()
    expect(handlers.get('themeChanged')).toHaveLength(0)

    // BackButton 显隐与点击（点击回调由导航栈注册）
    bridge.backButton.show()
    bridge.backButton.hide()
    expect(calls).toEqual(expect.arrayContaining(['show', 'hide']))
    const onBack = vi.fn()
    const offBack = bridge.backButton.onClick(onBack)
    handlers.get('back')?.[0]()
    expect(onBack).toHaveBeenCalledTimes(1)
    offBack()

    bridge.openLink('https://t.me/x/1')
    expect(calls).toContain('open:https://t.me/x/1')
  })

  it('botId 从地址栏读取', async () => {
    window.history.replaceState({}, '', '/miniapp/?bot=77')
    const { wa } = mockWebApp()
    const bridge = await initTelegram({ getWebApp: () => wa })
    expect(bridge.botId).toBe('77')
    window.history.replaceState({}, '', '/miniapp/')
  })
})
