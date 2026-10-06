// 管理面板路由的解析与反解：路径 ↔ 路由，且导航保留 ?bot= 上下文。
import { describe, expect, it } from 'vitest'
import { adminPath, parseAdminRoute, sectionOf } from './route'

describe('admin route', () => {
  it('parseAdminRoute 还原各页面', () => {
    expect(parseAdminRoute('/admin')).toEqual({ k: 'overview' })
    expect(parseAdminRoute('/admin/')).toEqual({ k: 'overview' })
    expect(parseAdminRoute('/admin/bots')).toEqual({ k: 'bots' })
    expect(parseAdminRoute('/admin/bots/12')).toEqual({ k: 'bot', id: 12 })
    expect(parseAdminRoute('/admin/chats/3/-100123')).toEqual({ k: 'chat', botId: 3, chatId: -100123 })
    expect(parseAdminRoute('/admin/logs/9')).toEqual({ k: 'log', id: 9 })
    expect(parseAdminRoute('/admin/users/9')).toEqual({ k: 'user', id: 9 })
    expect(parseAdminRoute('/admin/appeals/9')).toEqual({ k: 'appeal', id: 9 })
    expect(parseAdminRoute('/admin/lists/gban')).toEqual({ k: 'lists', section: 'gban' })
    expect(parseAdminRoute('/admin/upstreams/2')).toEqual({ k: 'upstream', id: 2 })
    expect(parseAdminRoute('/admin/models/pai%2Fllm')).toEqual({ k: 'model', name: 'pai/llm' })
    expect(parseAdminRoute('/admin/rules/5')).toEqual({ k: 'rule', id: 5 })
    expect(parseAdminRoute('/admin/settings')).toEqual({ k: 'settings' })
    expect(parseAdminRoute('/admin/syslog')).toEqual({ k: 'syslog' })
  })

  it('未知路径与不完整详情回退到列表', () => {
    expect(parseAdminRoute('/admin/nope')).toEqual({ k: 'overview' })
    expect(parseAdminRoute('/admin/bots/abc')).toEqual({ k: 'bots' })
    expect(parseAdminRoute('/admin/chats/3')).toEqual({ k: 'chats' })
  })

  it('sectionOf 把详情归到所属侧栏项', () => {
    expect(sectionOf({ k: 'bot', id: 1 })).toBe('bots')
    expect(sectionOf({ k: 'log', id: 1 })).toBe('records')
    expect(sectionOf({ k: 'appeal', id: 1 })).toBe('records')
    expect(sectionOf({ k: 'upstream', id: 1 })).toBe('upstreams')
    expect(sectionOf({ k: 'rule', id: 1 })).toBe('rules')
    expect(sectionOf({ k: 'settings' })).toBe('settings')
  })

  it('adminPath 拼路径并保留 ?bot=', () => {
    window.history.replaceState(null, '', '/admin/?bot=42')
    expect(adminPath({ k: 'overview' })).toBe('/admin/?bot=42')
    expect(adminPath({ k: 'bot', id: 12 })).toBe('/admin/bots/12?bot=42')
    expect(adminPath({ k: 'model', name: 'pai/llm' })).toBe('/admin/models/pai%2Fllm?bot=42')

    window.history.replaceState(null, '', '/admin/')
    expect(adminPath({ k: 'settings' })).toBe('/admin/settings')
  })
})
