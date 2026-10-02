// 上游渠道：列表、添加参数、详情（api_key 留空 = 不改、能力开关、改名、
// 启停乐观更新、删除确认、测试连通的成功/失败/禁用态）。
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
  it('列表渲染名称/类型/base_url/状态，点行进详情', async () => {
    renderPage(<UpstreamsPage />)

    expect(await screen.findByText('demo')).toBeInTheDocument()
    expect(screen.getByText(/OpenAI Completions · https:\/\/api\.example\.com/)).toBeInTheDocument()
    expect(screen.getByText('启用')).toBeInTheDocument()
  })

  it('添加渠道：提交名称/类型/base_url/api_key/能力/启用', async () => {
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
      kind: 'openai',
      base_url: 'https://api2.example.com',
      api_key: 'sk-2',
      supports_chat: true,
      supports_systemone: true,
      disabled: false,
    })
  })

  it('添加 Cloudflare 渠道：能力固定为主判定（chat 关、systemone 开）', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamsPage />)
    await screen.findByText('demo')

    fireEvent.click(screen.getByText('＋ 添加渠道'))
    fireEvent.change(screen.getByLabelText('名称（模型名前缀）'), { target: { value: 'cf' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Cloudflare' }))
    fireEvent.change(screen.getByLabelText('base_url'), {
      target: { value: 'https://api.cloudflare.com/client/v4/accounts/acc' },
    })
    fireEvent.change(screen.getByLabelText('api_key'), { target: { value: 'cf-token' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'add',
      name: 'cf',
      kind: 'cloudflare',
      base_url: 'https://api.cloudflare.com/client/v4/accounts/acc',
      api_key: 'cf-token',
      supports_chat: false,
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
      kind: 'openai',
      base_url: 'https://api.example.com',
      supports_chat: true,
      supports_systemone: false,
    })
    expect(bodies[0]).not.toHaveProperty('api_key')
  })

  it('渠道配置切到 Gemini：对话协议只能复判，提交 kind 与强制能力', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('upstream', bodies)
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByText('渠道配置'))
    fireEvent.click(await screen.findByRole('button', { name: 'Gemini' }))
    // Gemini 是 chat-only：能力开关消失，换成说明。
    expect(screen.queryByRole('switch', { name: '支持 systemone（初判）' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'update',
      id: 1,
      kind: 'gemini',
      base_url: 'https://api.example.com',
      supports_chat: true,
      supports_systemone: false,
    })
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

  it('测试连通成功：提交 action=test/id，行内展示延迟与模型', async () => {
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/upstream', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true, latency_ms: 123, model: 'demo/gpt-5-mini' })
      }),
    )
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByRole('button', { name: '测试连通' }))
    expect(await screen.findByText('✅ 连通 · 123ms · demo/gpt-5-mini')).toBeInTheDocument()
    expect(bodies[0]).toEqual({ action: 'test', id: 1 })
  })

  it('测试连通失败：HTTP 200 + ok:false 时行内展示错误原因', async () => {
    server.use(
      http.post('*/miniapp/api/upstream', () =>
        HttpResponse.json({ ok: false, error: '上游 demo 返回 500' }),
      ),
    )
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    fireEvent.click(screen.getByRole('button', { name: '测试连通' }))
    expect(await screen.findByText('❌ 上游 demo 返回 500')).toBeInTheDocument()
  })

  it('测试连通 pending：按钮禁用，不阻塞其他操作', async () => {
    let release = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.post('*/miniapp/api/upstream', async () => {
        await gate
        return HttpResponse.json({ ok: true, latency_ms: 5, model: 'demo/m' })
      }),
    )
    renderPage(<UpstreamDetailPage id={1} />)
    await screen.findByText('https://api.example.com')

    const btn = screen.getByRole('button', { name: '测试连通' })
    fireEvent.click(btn)
    await waitFor(() => expect(btn).toBeDisabled())
    // 连通测试只锁自己：同一时间启停等其它操作照常可用。
    expect(screen.getByRole('switch', { name: '启用' })).toBeEnabled()

    release()
    expect(await screen.findByText('✅ 连通 · 5ms · demo/m')).toBeInTheDocument()
  })
})
