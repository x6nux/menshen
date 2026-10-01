// 模型定价：搜索、列表摘要、添加（上游仅启用中 + 四个价格）、详情价格编辑、
// 启停与删除参数、无启用上游时的入口收敛。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { ModelDetailPage, ModelsPage } from './ModelsPage'

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

describe('ModelsPage', () => {
  it('搜索按名称过滤，行内展示上游与单价摘要', async () => {
    renderPage(<ModelsPage />)

    expect(await screen.findByText('demo/gpt-5-mini')).toBeInTheDocument()
    expect(screen.getByText(/输入 \$0.1\/M · 补全 \$0.2\/M/)).toBeInTheDocument()

    const input = screen.getByLabelText('搜索模型')
    input.focus()
    fireEvent.change(input, { target: { value: 'zzz' } })
    expect(screen.queryByText('demo/gpt-5-mini')).not.toBeInTheDocument()
    expect(screen.getByText('没有匹配的模型')).toBeInTheDocument()
    expect(input).toHaveFocus()
  })

  it('添加模型：上游（仅启用中）+ 模型 ID + 四价格', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('model', bodies)
    renderPage(<ModelsPage />)
    await screen.findByText('demo/gpt-5-mini')

    fireEvent.click(screen.getByText('＋ 添加模型'))
    expect(await screen.findByText('添加模型')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('模型 ID'), { target: { value: 'gpt-5' } })
    fireEvent.change(screen.getByLabelText('输入价 $/M'), { target: { value: '0.5' } })
    fireEvent.change(screen.getByLabelText('补全价 $/M'), { target: { value: '1.5' } })
    fireEvent.change(screen.getByLabelText('缓存读取价'), { target: { value: '0.05' } })
    fireEvent.change(screen.getByLabelText('缓存创建价'), { target: { value: '0.06' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'add',
      upstream: 'demo',
      model_id: 'gpt-5',
      prompt_price: '0.5',
      completion_price: '1.5',
      cache_read_price: '0.05',
      cache_write_price: '0.06',
    })
  })

  it('没有启用的上游：不出现添加入口，给出引导文案', async () => {
    useState({
      ...structuredClone(mockState),
      upstreams: mockState.upstreams?.map((u) => ({ ...u, status: false })),
    })
    renderPage(<ModelsPage />)
    await screen.findByText('demo/gpt-5-mini')

    expect(screen.queryByText('＋ 添加模型')).not.toBeInTheDocument()
    expect(screen.getByText(/还没有启用的上游渠道/)).toBeInTheDocument()
  })
})

describe('ModelDetailPage', () => {
  it('价格编辑：四价格一起提交', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('model', bodies)
    renderPage(<ModelDetailPage name="demo/gpt-5-mini" />)
    await screen.findByText('上游 demo ｜ 模型 ID gpt-5-mini')

    fireEvent.change(screen.getByLabelText('输入价'), { target: { value: '0.9' } })
    fireEvent.click(screen.getByRole('button', { name: '保存价格' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      action: 'update',
      name: 'demo/gpt-5-mini',
      prompt_price: '0.9',
      completion_price: '0.2',
      cache_read_price: '0.01',
      cache_write_price: '0.01',
    })
  })

  it('启停 Switch 乐观更新，提交 enabled=false', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('model', bodies)
    renderPage(<ModelDetailPage name="demo/gpt-5-mini" />)
    await screen.findByText('上游 demo ｜ 模型 ID gpt-5-mini')

    const sw = screen.getByRole('switch', { name: '启用' })
    expect(sw).toBeChecked()
    fireEvent.click(sw)
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'update', name: 'demo/gpt-5-mini', enabled: false })
  })

  it('删除模型：确认文案含模型名，确认后提交 remove', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('model', bodies)
    renderPage(<ModelDetailPage name="demo/gpt-5-mini" />)
    await screen.findByText('上游 demo ｜ 模型 ID gpt-5-mini')

    fireEvent.click(screen.getByText('删除该模型'))
    expect(await screen.findByText('删除该模型？')).toBeInTheDocument()
    expect(screen.getByText(/将删除「demo\/gpt-5-mini」/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '删除' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'remove', name: 'demo/gpt-5-mini' })
  })
})
