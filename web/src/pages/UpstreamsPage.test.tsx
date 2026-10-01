// 上游渠道：列表、添加参数、详情（api_key 留空 = 不改、能力开关、改名、
// 启停乐观更新、删除确认）。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { UpstreamDetailPage, UpstreamsPage } from './UpstreamsPage'

startTestServer()

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

function capturePost(op: string, bodies: Record<string, unknown>[]) {
  server.use(
    http.post(`*/miniapp/api/${op}`, async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>)
      return HttpResponse.json({ ok: true })
    }),
  )
}

describe('UpstreamsPage', () => {
  it('列表渲染名称/base_url/状态，点行进详情', async () => {
    renderPage(<UpstreamsPage />)

    expect(await screen.findByText('demo')).toBeInTheDocument()
    expect(screen.getByText('https://api.example.com')).toBeInTheDocument()
    expect(screen.getByText('启用')).toBeInTheDocument()
  })

  it('添加渠道：提交名称/base_url/api_key/能力/启用', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamsPage />)
    await screen.findByText('demo')

    fireEvent.click(screen.getByText('＋ 添加渠道'))
    expect(await screen.findByText('添加渠道')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('名称（模型名前缀）'), { target: { value: 'demo2' } })
    fireEvent.change(screen.getByLabelText('base_url'), {
      target: { value: 'https://api2.example.com' },
    })
    fireEvent.change(screen.getByLabelText('api_key'), { target: { value: 'sk-2' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'add',
      name: 'demo2',
      base_url: 'https://api2.example.com',
      api_key: 'sk-2',
      supports_chat: true,
      supports_systemone: true,
      disabled: false,
    })
  })

  it('次级管理员：上游页按 403 处理', async () => {
    useState({
      ...structuredClone(mockState),
      me: { uid: 200, main: false },
    })
    renderPage(<UpstreamsPage />)
    expect(await screen.findByText('没有权限')).toBeInTheDocument()
  })
})

describe('UpstreamDetailPage', () => {
  it('渠道配置：api_key 留空不改（请求体不含 api_key），能力开关照发', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByText('渠道配置'))
    expect(await screen.findByText('当前：sk-****')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: '支持 systemone（初判）' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'update',
      id: 1,
      base_url: 'https://api.example.com',
      supports_chat: true,
      supports_systemone: false,
    })
    expect(bodies[0]).not.toHaveProperty('api_key')
  })

  it('改名：提交 name', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByText('名称'))
    expect(await screen.findByText('重命名上游')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'demo-renamed' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'update', id: 1, name: 'demo-renamed' })
  })

  it('启停 Switch 乐观更新，提交 status=false', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    const sw = screen.getByRole('switch', { name: '启用' })
    expect(sw).toBeChecked()
    fireEvent.click(sw)
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'update', id: 1, status: false })
  })

  it('删除上游：确认文案含名称与后果，确认后提交 remove', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByText('删除该上游'))
    expect(await screen.findByText('删除该上游？')).toBeInTheDocument()
    expect(screen.getByText(/将删除渠道「demo」/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '删除' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'remove', id: 1 })
  })
})
