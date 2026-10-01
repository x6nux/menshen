// 申诉详情行为测试：>300 字理由完整渲染、解禁码/兑换/网页验证记录可见、
// 状态→操作可见性矩阵、appealact 参数与操作后失效刷新。
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { AppealDetail } from '../api/types'
import { mockAppealDetail } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { AppealDetailPage } from './AppealDetailPage'

startTestServer()

function detail(overrides: Partial<AppealDetail> = {}): AppealDetail {
  return { ...structuredClone(mockAppealDetail), ...overrides }
}

/** 等确认面板退场动画结束：期间 MUI Modal 会把页面内容标记 aria-hidden。 */
async function waitSheetClosed() {
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
}

describe('AppealDetailPage', () => {
  it('详情单独请求；>300 字理由完整渲染；解禁码/兑换/网页验证记录可见', async () => {
    const longStatement = `申诉理由开头：${'这'.repeat(320)}——结尾标记END`
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/appeal', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(
          detail({
            status: 'noweb',
            statement: longStatement,
            code: 'CODE-abc123',
            has_code: true,
            code_expires: 1700100000,
            web_attempts: 3,
            web_checks: 2,
            web_passes: 1,
            redeems: [{ chat_id: -100123, by_uid: 200, at: 1700000200 }],
          }),
        )
      }),
    )
    renderPage(<AppealDetailPage id={77} />)

    // 数据源是 POST appeal {id}
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ id: 77 })

    // 完整理由（列表接口会截到 300 字，详情不能截）
    const statement = await screen.findByTestId('appeal-statement')
    expect(statement).toHaveTextContent(longStatement)
    expect(statement.textContent?.length).toBeGreaterThan(300)
    expect(statement.textContent).toContain('结尾标记END')

    // 解禁码与兑换、网页验证记录
    expect(screen.getByText('CODE-abc123')).toBeInTheDocument()
    expect(screen.getByText(/群 -100123 · uid 200/)).toBeInTheDocument()
    expect(screen.getByText(/验证记录 2 条（通过 1）/)).toBeInTheDocument()
    expect(screen.getByText('待人工处理')).toBeInTheDocument()
  })

  const MATRIX: [string, string[]][] = [
    ['noweb', ['人工解除', '驳回', '直接签发解禁码', '重跑 AI 复核']],
    ['web', ['人工解除', '驳回', '直接签发解禁码', '重跑 AI 复核']],
    ['ai', ['人工解除', '驳回', '重跑 AI 复核']],
    ['statement', ['人工解除', '驳回']],
    ['code', ['人工解除', '驳回']],
    ['lifted', []],
    ['rejected', []],
    ['expired', []],
  ]

  it.each(MATRIX)('状态 %s 的操作可见性', async (status, expected) => {
    server.use(
      http.post('*/miniapp/api/appeal', () => HttpResponse.json(detail({ status }))),
    )
    renderPage(<AppealDetailPage id={77} />)
    await screen.findByText('AI 复核')

    const all = ['人工解除', '驳回', '直接签发解禁码', '重跑 AI 复核']
    for (const name of all) {
      const found = screen.queryByRole('button', { name })
      if (expected.includes(name)) {
        expect(found).toBeInTheDocument()
      } else {
        expect(found).not.toBeInTheDocument()
      }
    }
    if (expected.length === 0) {
      expect(screen.getByText(/该申诉已结/)).toBeInTheDocument()
    }
  })

  it('人工解除/驳回需确认，appealact 参数正确且操作后失效刷新详情', async () => {
    const acts: Record<string, unknown>[] = []
    let appealCalls = 0
    server.use(
      http.post('*/miniapp/api/appeal', () => {
        appealCalls += 1
        return HttpResponse.json(detail({ status: 'noweb' }))
      }),
      http.post('*/miniapp/api/appealact', async ({ request }) => {
        acts.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true, note: '已解除（服务端）' })
      }),
    )
    renderPage(<AppealDetailPage id={77} />)
    await screen.findByRole('button', { name: '人工解除' })

    fireEvent.click(screen.getByRole('button', { name: '人工解除' }))
    expect(await screen.findByText('确认通过并解除该用户全部限制？')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '解除' }))
    await waitFor(() => expect(acts).toHaveLength(1))
    expect(acts[0]).toEqual({ id: 77, action: 'approve' })
    expect(await screen.findByText('已解除（服务端）')).toBeInTheDocument()
    // ['appeal'] 失效触发详情重新请求
    await waitFor(() => expect(appealCalls).toBeGreaterThanOrEqual(2))
    await waitSheetClosed()

    fireEvent.click(screen.getByRole('button', { name: '驳回' }))
    expect(await screen.findByText('确认驳回？限制保持原样。')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '确认驳回' }))
    await waitFor(() => expect(acts).toHaveLength(2))
    expect(acts[1]).toEqual({ id: 77, action: 'reject' })
  })

  it('签发解禁码与重跑 AI 复核直接走 appealact（web 状态两者都有）', async () => {
    const acts: Record<string, unknown>[] = []
    server.use(
      http.post('*/miniapp/api/appeal', () => HttpResponse.json(detail({ status: 'web' }))),
      http.post('*/miniapp/api/appealact', async ({ request }) => {
        acts.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true })
      }),
    )
    renderPage(<AppealDetailPage id={77} />)
    await screen.findByRole('button', { name: '直接签发解禁码' })

    fireEvent.click(screen.getByRole('button', { name: '直接签发解禁码' }))
    await waitFor(() => expect(acts).toHaveLength(1))
    expect(acts[0]).toEqual({ id: 77, action: 'issue_code' })

    fireEvent.click(screen.getByRole('button', { name: '重跑 AI 复核' }))
    await waitFor(() => expect(acts).toHaveLength(2))
    expect(acts[1]).toEqual({ id: 77, action: 'rerun' })
  })

  it('申诉人链接键盘可达：Enter 进用户页', async () => {
    server.use(
      http.post('*/miniapp/api/appeal', () => HttpResponse.json(detail({ status: 'noweb' }))),
    )
    renderPage(
      <>
        <AppealDetailPage id={77} />
        <NavProbe />
      </>,
    )

    const link = await screen.findByRole('link', { name: 'uid 555（资料）' })
    fireEvent.keyDown(link, { key: 'Enter' })
    expect(screen.getByTestId('nav-top').textContent).toBe('user')
  })

  it('底部操作栏预留高度暴露在 data-reserved（无 RO 用兜底 ≥170）', async () => {
    server.use(
      http.post('*/miniapp/api/appeal', () => HttpResponse.json(detail({ status: 'noweb' }))),
    )
    renderPage(<AppealDetailPage id={77} />)
    await screen.findByRole('button', { name: '人工解除' })

    const reserved = Number(
      screen.getByTestId('appeal-detail-page').getAttribute('data-reserved'),
    )
    expect(reserved).toBeGreaterThanOrEqual(170)
  })
})
