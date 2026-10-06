// 桌面端管理面板的外壳测试：会话有效时渲染侧栏与内容，401 时给登录指引。
// api 换成 spy，其余（ApiError）保真。
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '../api/client'
import { mockLogs, mockState } from '../mocks/fixtures'
import { AdminApp } from './AdminApp'

vi.mock('../api/client', async (importOriginal) => {
  const mod = await importOriginal<typeof import('../api/client')>()
  return { ...mod, api: vi.fn() }
})

const mockApi = vi.mocked(api)

function mockApiByOp() {
  mockApi.mockImplementation((async (op: string) => {
    if (op === 'state') return mockState
    if (op === 'logs') return { logs: mockLogs, page: 1, total: mockLogs.length }
    return { ok: true }
  }) as never)
}

beforeEach(() => {
  mockApi.mockReset()
  window.history.replaceState(null, '', '/admin/?bot=42')
})

describe('AdminApp', () => {
  it('会话有效：侧栏列出入口，内容区渲染概览', async () => {
    mockApiByOp()
    render(<AdminApp />)

    expect(await screen.findByText('🛡 门神')).toBeInTheDocument()
    expect(screen.getAllByText('机器人').length).toBeGreaterThan(0)
    expect(screen.getAllByText('群组').length).toBeGreaterThan(0)
    expect(screen.getAllByText('记录').length).toBeGreaterThan(0)
    expect(screen.getByText('退出登录')).toBeInTheDocument()
    // 概览的指标卡。
    expect((await screen.findAllByText('近 24 小时')).length).toBeGreaterThan(0)
  })

  it('会话过期（state 401）：显示获取登录链接的指引', async () => {
    mockApi.mockImplementation((async (op: string) => {
      if (op === 'state') throw new ApiError(401, '网页版登录已过期')
      return { ok: true }
    }) as never)
    render(<AdminApp />)

    expect(await screen.findByText('需要登录')).toBeInTheDocument()
    expect(screen.getByText(/🖥 网页版（浏览器打开）/)).toBeInTheDocument()
  })
})
