// App 外壳测试：三条主路径（不可用引导 / 就绪外壳 / 错误分流）+ 主题实时性。
// telegram 与 api/client 只替换必要的导出（api 换成 spy，ApiError/setApiBridge 保真）。
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { ApiError, api } from './api/client'
import { mockState } from './mocks/fixtures'
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

  it('telegram 就绪前不发请求；就绪后渲染标题、身份与 TabBar', async () => {
    const { bridge } = makeBridge()
    let resolveInit!: (bridge: TelegramBridge) => void
    mockInit.mockReturnValue(
      new Promise<TelegramBridge>((resolve) => {
        resolveInit = resolve
      }),
    )
    mockApi.mockResolvedValue(mockState as never)

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
    mockApi.mockRejectedValueOnce(new ApiError(401, '身份失效')).mockResolvedValue(mockState as never)

    render(<App />)
    expect(await screen.findByText('请通过 Telegram 菜单按钮打开')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(mockApi).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('主管理员 · uid 100')).toBeInTheDocument()
  })

  it('themeChanged 后 ThemeProvider 用新 themeParams 实时重建（CssBaseline 生效）', async () => {
    const { bridge, emitTheme } = makeBridge()
    mockInit.mockResolvedValue(bridge)
    mockApi.mockResolvedValue(mockState as never)

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
})
