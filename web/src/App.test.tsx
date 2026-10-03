// App 外壳测试：三条主路径（不可用引导 / 就绪外壳 / 错误分流）+ 主题实时性。
// telegram 与 api/client 只替换必要的导出（api 换成 spy，ApiError/setApiBridge 保真）。
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { ApiError, api } from './api/client'
import { mockAppealDetail, mockAppeals, mockLogDetail, mockLogs, mockState, mockUser } from './mocks/fixtures'
import { initTelegram } from './telegram'
import type { TelegramBridge, TelegramTheme } from './telegram'

vi.mock('./telegram', async (importOriginal) => {
  const mod = await importOriginal<typeof import('./telegram')>()
  return { ...mod, initTelegram: vi.fn() }
})
vi.mock('./api/client', async (importOriginal) => {
  const mod = await importOriginal<typeof import('./api/client')>()
  return { ...mod, api: vi.fn() }
})

const mockInit = vi.mocked(initTelegram)
const mockApi = vi.mocked(api)

/** 被 mock 的 api 按 op 返回最小可用响应：外壳与概览都会发请求（state/logs）。 */
function mockApiByOp() {
  mockApi.mockImplementation((async (op: string) => {
    if (op === 'state') return mockState
    if (op === 'logs') return { logs: mockLogs, page: 1, total: mockLogs.length }
    if (op === 'log') return mockLogDetail
    if (op === 'user') return mockUser
    if (op === 'appeals') return { appeals: mockAppeals, page: 1, total: mockAppeals.length }
    if (op === 'appeal') return mockAppealDetail
    return { ok: true }
  }) as never)
}

function makeBridge() {
  const listeners = new Set<(theme: TelegramTheme) => void>()
  const bridge: TelegramBridge = {
    available: true,
    initData: 'test-init-data',
    botId: '0',
    getTheme: () => ({ colorScheme: 'light', themeParams: {} }),
    onThemeChanged: (cb) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    backButton: { show: () => {}, hide: () => {}, onClick: () => () => {} },
    openLink: () => {},
  }
  return { bridge, emitTheme: (theme: TelegramTheme) => listeners.forEach((cb) => cb(theme)) }
}

beforeEach(() => {
  mockInit.mockReset()
  mockApi.mockReset()
})

describe('App', () => {
  it('SDK 不可用：渲染引导页且不发请求', async () => {
    mockInit.mockResolvedValue({ ...makeBridge().bridge, available: false, initData: '' })
    render(<App />)

    expect(
      await screen.findByText('请通过 Telegram 里的菜单按钮「配置」打开本页。'),
    ).toBeInTheDocument()
    expect(screen.getByText(/请关闭后重新打开本页/)).toBeInTheDocument()
    expect(mockApi).not.toHaveBeenCalled()
  })

  it('initTelegram 异常时进入引导页，不停留在骨架屏', async () => {
    mockInit.mockRejectedValue(new Error('注入实现异常'))
    render(<App />)
    expect(await screen.findByTestId('unavailable-guide')).toBeInTheDocument()
    expect(screen.queryByTestId('boot-skeleton')).not.toBeInTheDocument()
    expect(mockApi).not.toHaveBeenCalled()
  })

  it('telegram 就绪前不发请求；就绪后渲染标题、身份与 TabBar', async () => {
    const { bridge } = makeBridge()
    let resolveInit!: (bridge: TelegramBridge) => void
    mockInit.mockReturnValue(
      new Promise<TelegramBridge>((resolve) => {
        resolveInit = resolve
      }),
    )
    mockApiByOp()

    render(<App />)
    // 初始化中：整页骨架；契约要求此时不得用空 initData 发请求
    expect(screen.getByTestId('boot-skeleton')).toBeInTheDocument()
    await Promise.resolve()
    expect(mockApi).not.toHaveBeenCalled()

    await act(async () => {
      resolveInit(bridge)
    })

    expect(await screen.findByText('门神')).toBeInTheDocument()
    expect(screen.getByText('主管理员 · @menshen_admin')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '概览' })).toBeInTheDocument()
    expect(mockApi).toHaveBeenCalledWith('state', undefined, expect.anything())
  })

  it('state 返回 401：渲染 Telegram 引导文案，重试后恢复外壳', async () => {
    const { bridge } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApi.mockRejectedValueOnce(new ApiError(401, '身份失效'))
    mockApiByOp()

    render(<App />)
    expect(await screen.findByText('请通过 Telegram 菜单按钮打开')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    // 第二次仍必须是 state 请求（不能因重试而漏掉鉴权前的数据加载）
    await waitFor(() => expect(mockApi.mock.calls.length).toBeGreaterThanOrEqual(2))
    expect(mockApi.mock.calls[1][0]).toBe('state')
    expect(await screen.findByText('主管理员 · @menshen_admin')).toBeInTheDocument()
  })

  it('themeChanged 后 ThemeProvider 用新 themeParams 实时重建（CssBaseline 生效）', async () => {
    const { bridge, emitTheme } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApiByOp()

    render(<App />)
    await screen.findByText('门神')

    act(() => {
      emitTheme({ colorScheme: 'dark', themeParams: { bg_color: '#101010' } })
    })
    await waitFor(() => {
      expect(window.getComputedStyle(document.body).backgroundColor).toBe('rgb(16, 16, 16)')
    })
    // 主题变化后外壳仍在
    expect(screen.getByText('门神')).toBeInTheDocument()
  })

  it('Tab 映射：机器人列表可进详情、返回后仍在列表；群组 Tab 渲染真实页面', async () => {
    const { bridge } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApiByOp()

    render(<App />)
    await screen.findByText('门神')

    // 概览是真实页面：待办卡可见
    expect(await screen.findByText('未结申诉')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '机器人' }))
    expect(await screen.findByText('演示机器人')).toBeInTheDocument()

    fireEvent.click(screen.getByText('门神小助手'))
    expect(await screen.findByText('管理其群组')).toBeInTheDocument()
    expect(screen.getByText('主 bot 的归属由配置文件决定，不能改派')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    expect(await screen.findByText('演示机器人')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '群组' }))
    expect(await screen.findByText('测试群')).toBeInTheDocument()

    // 搜索词在进入详情再返回后不丢（计划 2.2：页面内存状态导航往返保留）
    fireEvent.change(screen.getByLabelText('搜索群组'), { target: { value: '第二个' } })
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('第二个群'))
    expect(await screen.findByText('实际执行')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    expect(await screen.findByText('第二个群')).toBeInTheDocument()
    expect(screen.queryByText('测试群')).not.toBeInTheDocument()
    expect(screen.getByLabelText('搜索群组')).toHaveValue('第二个')
  })

  it('记录 Tab：判定/申诉分段、记录详情与用户页的动态标题', async () => {
    const { bridge } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApiByOp()

    render(<App />)
    await screen.findByText('门神')

    fireEvent.click(screen.getByRole('button', { name: '记录' }))
    // 记录分段默认「已删除」
    expect(await screen.findByText('已删除')).toBeInTheDocument()

    fireEvent.click(await screen.findByRole('button', { name: /#9812/ }))
    expect(await screen.findByText('记录 #9812')).toBeInTheDocument()

    // 详情里的 uid 链接进用户页
    fireEvent.click(screen.getByText('uid 555（资料）'))
    expect(await screen.findByText('用户 uid 555')).toBeInTheDocument()
    expect(await screen.findByText('演示用户')).toBeInTheDocument()

    // 返回记录页后切申诉分段，行进申诉详情
    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    fireEvent.click(await screen.findByRole('button', { name: /申诉/ }))
    fireEvent.click(await screen.findByRole('button', { name: /#77/ }))
    expect(await screen.findByText('申诉 #77')).toBeInTheDocument()
  })

  it('我的 Tab：名单/上游/设置的页面栈接线', async () => {
    const { bridge } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApiByOp()

    render(<App />)
    await screen.findByText('门神')

    fireEvent.click(screen.getByRole('button', { name: '我的' }))
    expect(await screen.findByText('名单管理')).toBeInTheDocument()

    fireEvent.click(screen.getByText('上游渠道'))
    expect(await screen.findByText('demo')).toBeInTheDocument()
    expect(screen.getByText(/https:\/\/api\.example\.com/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    fireEvent.click(await screen.findByText('全局设置'))
    expect(await screen.findByText('总开关')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    fireEvent.click(await screen.findByText('名单管理'))
    expect(await screen.findByText('默认豁免（内置，无需配置）')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '资料放行' })).toBeInTheDocument()
  })

  it('次级管理员：我的 → 名单管理进入联封分段', async () => {
    const { bridge } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApi.mockImplementation((async (op: string) => {
      if (op === 'state') {
        return {
          ...structuredClone(mockState),
          me: { uid: 200, main: false },
          bots: mockState.bots.map((b) => ({ ...b, owner_id: 200 })),
        }
      }
      if (op === 'logs') return { logs: [], page: 1, total: 0 }
      return { ok: true }
    }) as never)

    render(<App />)
    await screen.findByText('门神')

    fireEvent.click(screen.getByRole('button', { name: '我的' }))
    expect(await screen.findByText('名单管理')).toBeInTheDocument()
    fireEvent.click(screen.getByText('名单管理'))

    // 次管 push 的是 {k:'lists', section:'gban'}：直接进联封，没有
    // 白名单/资料放行/次级管理员分段。
    expect(await screen.findByText('全局联合封禁组')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '白名单' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '资料放行' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '次级管理员' })).not.toBeInTheDocument()
  })
})

describe('App · 网页版', () => {
  const realMatchMedia = window.matchMedia

  beforeEach(() => {
    // jsdom 没有 matchMedia：桌面判定用 stub 固定为宽屏；路径切到 /admin。
    window.matchMedia = ((query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia
    window.history.pushState({}, '', '/admin/?bot=42')
  })

  afterEach(() => {
    window.history.pushState({}, '', '/')
    window.matchMedia = realMatchMedia
  })

  it('已登录（state 正常）：桌面外壳列出管理入口', async () => {
    mockApiByOp()
    render(<App />)

    // 侧栏：管理分区与直达入口
    expect(await screen.findByText('AI 必封规则')).toBeInTheDocument()
    expect(screen.getByText('上游渠道')).toBeInTheDocument()
    expect(screen.getByText('全局设置')).toBeInTheDocument()
    expect(screen.getByText('退出登录')).toBeInTheDocument()
    // 内容区渲染概览
    expect(await screen.findByText('近 24 小时')).toBeInTheDocument()
  })

  it('会话过期（state 401）：显示获取登录链接的指引', async () => {
    mockApi.mockImplementation((async (op: string) => {
      if (op === 'state') throw new ApiError(401, '网页版登录已过期')
      return { ok: true }
    }) as never)
    render(<App />)

    expect(await screen.findByText('需要登录')).toBeInTheDocument()
    expect(screen.getByText(/🖥 网页版（浏览器打开）/)).toBeInTheDocument()
  })

  it('侧栏点击管理入口：push 对应页面并可返回', async () => {
    mockApiByOp()
    render(<App />)

    fireEvent.click(await screen.findByText('全局设置'))
    expect(await screen.findByText('总开关')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '← 返回' }))
    // 管理入口挂在「我的」Tab 下，返回后回到该 Tab（侧栏也有同名入口）。
    expect((await screen.findAllByText('名单管理')).length).toBeGreaterThan(0)
  })
})
