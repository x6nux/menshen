// App 外壳测试：三条主路径（不可用引导 / 就绪外壳 / 错误分流）+ 主题实时性。
// telegram 与 api/client 只替换必要的导出（api 换成 spy，ApiError/setApiBridge 保真）。
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { ApiError, api } from './api/client'
import { mockLogs, mockState } from './mocks/fixtures'
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
    expect(screen.getByText(/telegram\.org 在部分网络下不可达/)).toBeInTheDocument()
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
    expect(screen.getByText('主管理员 · uid 100')).toBeInTheDocument()
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
    expect(await screen.findByText('主管理员 · uid 100')).toBeInTheDocument()
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
})
