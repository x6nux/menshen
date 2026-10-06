// 路由编解码：路径 ↔ (tab + 二级页栈)。覆盖刷新/深链要还原的每一种页面。
import { describe, expect, it } from 'vitest'
import type { Page, TabKey } from './nav'
import { parseRoute, pathOf, routeSlug } from './route'

/** roundTrip 断言 (tab, 栈) 编成路径再解析回来完全一致。 */
function roundTrip(tab: TabKey, stack: Page[]) {
  const path = pathOf(tab, stack)
  return { path, parsed: parseRoute(path) }
}

describe('route', () => {
  it('一级 tab 编成路径', () => {
    expect(pathOf('overview', [])).toBe('/miniapp/')
    expect(pathOf('bots', [])).toBe('/miniapp/bots')
    expect(pathOf('chats', [])).toBe('/miniapp/chats')
    expect(pathOf('records', [])).toBe('/miniapp/records')
    expect(pathOf('mine', [])).toBe('/miniapp/mine')
  })

  it('二级页带参数编进路径，且能解析回来', () => {
    const cases: { tab: TabKey; page: Page }[] = [
      { tab: 'bots', page: { k: 'bot', id: 12 } },
      { tab: 'chats', page: { k: 'chat', botId: 3, chatId: -100123 } },
      { tab: 'records', page: { k: 'log', id: 9 } },
      { tab: 'records', page: { k: 'user', id: 5 } },
      { tab: 'records', page: { k: 'appeal', id: 2 } },
      { tab: 'mine', page: { k: 'lists' } },
      { tab: 'mine', page: { k: 'upstreams' } },
      { tab: 'mine', page: { k: 'upstream', id: 3 } },
      { tab: 'mine', page: { k: 'models' } },
      { tab: 'mine', page: { k: 'rules' } },
      { tab: 'mine', page: { k: 'rule', id: 7 } },
      { tab: 'mine', page: { k: 'settings' } },
      { tab: 'mine', page: { k: 'syslog' } },
    ]
    for (const c of cases) {
      const { path, parsed } = roundTrip(c.tab, [c.page])
      expect(parsed.stack, path).toEqual([c.page])
      expect(parsed.tab, path).toBe(c.tab)
    }
  })

  it('模型 ID 含斜杠时仍作为一个路径段往返', () => {
    const name = '@cf/cloudflare/clef'
    const { path, parsed } = roundTrip('mine', [{ k: 'model', name }])
    expect(path).toBe(`/miniapp/models/${encodeURIComponent(name)}`)
    expect(parsed.stack).toEqual([{ k: 'model', name }])
  })

  it('名单分段留在查询串，路径固定为 /lists', () => {
    // 分段是页面内视图：路径不因它变化，深链 /lists/gban 仍能解析出分段。
    expect(pathOf('mine', [{ k: 'lists', section: 'gban' }])).toBe('/miniapp/lists')
    expect(parseRoute('/miniapp/lists/gban')).toEqual({
      tab: 'mine',
      stack: [{ k: 'lists', section: 'gban' }],
    })
  })

  it('Mini App 只认 /miniapp 前缀：路径恒定，/admin 不归它管', () => {
    // 桌面端面板有独立路由（src/admin/route.ts）。Mini App 的 pathOf 不随
    // 当前地址变化；误把 /admin 路径喂进来按未知路径回退到概览。
    window.history.replaceState(null, '', '/admin/')
    expect(pathOf('bots', [{ k: 'bot', id: 1 }])).toBe('/miniapp/bots/1')
    expect(pathOf('overview', [])).toBe('/miniapp/')
    expect(parseRoute('/admin/bots/1')).toEqual({ tab: 'overview', stack: [] })
  })

  it('未知路径回退到概览', () => {
    expect(parseRoute('/miniapp/nope/deep')).toEqual({ tab: 'overview', stack: [] })
    expect(parseRoute('/whatever')).toEqual({ tab: 'overview', stack: [] })
  })

  it('routeSlug 对一级 tab 与二级页各不相同', () => {
    expect(routeSlug('bots', [])).toBe('bots')
    expect(routeSlug('bots', [{ k: 'bot', id: 1 }])).toBe('bots/1')
    expect(routeSlug('records', [{ k: 'log', id: 1 }])).toBe('logs/1')
  })
})
