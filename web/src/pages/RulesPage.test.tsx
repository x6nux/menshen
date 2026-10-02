// AI 必封规则：Agent 启停/步骤/完成后刷新列表、启动竞态、约 2 秒自动轮询
// 与卸载停止、状态读取失败重试、列表徽标与 TP/FP 摘要、详情全库误封测试与
// 强制防误封门、开关乐观更新与失败回滚、400 toast、手动新增参数与保留输入、
// 删除确认、次管 403 且不发 rules 请求。
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { miniQueryKeys } from '../api/hooks'
import type { Rule, RuleAgent, RuleSample, RuleTest, State } from '../api/types'
import { mockRuleAgent, mockRules, mockState } from '../mocks/fixtures'
import { NavProbe, renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { RuleDetailPage, RulesPage } from './RulesPage'

startTestServer()

type RulesBody = Record<string, unknown>

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

/** captureRules 安装一个可编排的 rules handler，返回收到的请求体数组。 */
function captureRules(handler: (body: RulesBody, n: number) => Response | Promise<Response>) {
  const bodies: RulesBody[] = []
  server.use(
    http.post('*/miniapp/api/rules', async ({ request }) => {
      const body = (await request.json()) as RulesBody
      bodies.push(body)
      return handler(body, bodies.length)
    }),
  )
  return bodies
}

const ok = (extra: Record<string, unknown> = {}) => HttpResponse.json({ ok: true, ...extra })

const fpSample: RuleSample = {
  id: 9810,
  verdict: 'clean',
  action: 'none',
  kind: 'none',
  user_id: 556,
  chat_id: -1001234567890,
  created_at: 1699998000,
  text: '（mock）正常消息被误匹配。',
}

const undoneSample: RuleSample = {
  id: 9809,
  verdict: 'ad',
  action: 'undone',
  kind: 'promo',
  user_id: 557,
  chat_id: -1001111111111,
  created_at: 1699997000,
  text: '（mock）已撤销的处罚样本。',
}

describe('RulesPage · Agent', () => {
  it('空态 → 开始发现 → 运行中步骤（模型/工具）→ 完成并刷新列表', async () => {
    let agent: RuleAgent = { ...mockRuleAgent }
    let listCalls = 0
    const bodies = captureRules((body) => {
      switch (body.action) {
        case 'list':
          listCalls++
          return HttpResponse.json({ rules: [] })
        case 'agent_status':
          return HttpResponse.json({ agent })
        case 'agent_start':
          agent = { ...agent, running: true, started_at: 1700000600 }
          return ok({ agent })
        case 'agent_stop':
          return HttpResponse.json({ ok: true, stopped: true })
        default:
          return ok()
      }
    })
    const { client } = renderPage(<RulesPage />)

    expect(await screen.findByText('还没有规则')).toBeInTheDocument()
    expect(screen.getByText('空闲')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '开始发现' }))
    await waitFor(() => expect(bodies.some((b) => b.action === 'agent_start')).toBe(true))
    // 等 mutation 结算：onSuccess 会把初始运行态写进缓存，抢跑会被它盖掉。
    expect(await screen.findByText('已开始规则发现，完成后会给出候选规则')).toBeInTheDocument()

    // 轮询返回运行中 + 两步（模型轮次/工具调用）。
    agent = {
      ...agent,
      running: true,
      steps: [
        { n: 1, at: 1700000601, kind: 'model', name: '选样本', summary: '读取 20 条历史封禁' },
        { n: 2, at: 1700000602, kind: 'tool', name: 'test_rule', summary: 'pattern 命中 5 条' },
      ],
    }
    await act(async () => {
      await client.refetchQueries({ queryKey: miniQueryKeys.rulesAgent })
    })
    expect(screen.getByText('运行中')).toBeInTheDocument()
    expect(await screen.findByText('模型', undefined, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.getByText('工具')).toBeInTheDocument()
    expect(screen.getByText('选样本')).toBeInTheDocument()
    expect(screen.getByText('pattern 命中 5 条')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '停止' }))
    await waitFor(() => expect(bodies.some((b) => b.action === 'agent_stop')).toBe(true))

    // 结束：显示结果与创建的规则 id，并触发规则列表刷新。
    const before = listCalls
    agent = {
      ...agent,
      running: false,
      finished_at: 1700000603,
      result: '发现 1 条候选规则',
      created_rule_id: 7,
    }
    await act(async () => {
      await client.refetchQueries({ queryKey: miniQueryKeys.rulesAgent })
    })
    expect(await screen.findByText('发现 1 条候选规则')).toBeInTheDocument()
    expect(screen.getByText(/已创建规则 #7/)).toBeInTheDocument()
    expect(screen.getByText('已完成')).toBeInTheDocument()
    await waitFor(() => expect(listCalls).toBeGreaterThan(before))
  })

  it('竞态：迟到的挂载期 agent_status 不覆盖启动态，轮询继续', async () => {
    let releaseSlow!: () => void
    const slowGate = new Promise<void>((resolve) => {
      releaseSlow = resolve
    })
    let statusCalls = 0
    let agent: RuleAgent = { ...mockRuleAgent }
    captureRules((body) => {
      switch (body.action) {
        case 'list':
          return HttpResponse.json({ rules: [] })
        case 'agent_status':
          statusCalls++
          if (statusCalls === 1) {
            // 挂载期第一次状态请求卡住，模拟它在「开始发现」之后才带着空闲
            // 旧数据返回——没有竞态防护时会把 running 改回 false。
            return slowGate.then(() => HttpResponse.json({ agent: { ...mockRuleAgent } }))
          }
          return HttpResponse.json({ agent })
        case 'agent_start':
          agent = { ...agent, running: true, started_at: 1700000600 }
          return ok({ agent })
        default:
          return ok()
      }
    })
    renderPage(<RulesPage />)
    await screen.findByText('还没有规则')
    // 确保慢请求已经在途，再点开始。
    await waitFor(() => expect(statusCalls).toBe(1))

    fireEvent.click(screen.getByRole('button', { name: '开始发现' }))
    expect(await screen.findByText('已开始规则发现，完成后会给出候选规则')).toBeInTheDocument()

    // 迟到的空闲响应现在才 resolve：卡片必须保持「运行中」。
    releaseSlow()
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(screen.getByText('运行中')).toBeInTheDocument()

    // 且轮询没有被打断：下一次 status（约 2 秒后）仍会发生。
    await waitFor(() => expect(statusCalls).toBeGreaterThanOrEqual(3), { timeout: 6000 })
    expect(screen.getByText('运行中')).toBeInTheDocument()
  }, 15000)

  it('运行中约每 2 秒自动轮询，卸载后停止', async () => {
    let statusCalls = 0
    const runningAgent: RuleAgent = {
      ...mockRuleAgent,
      running: true,
      started_at: 1700000600,
      steps: [
        { n: 1, at: 1700000601, kind: 'model', name: '选样本', summary: '读取 20 条历史封禁' },
      ],
    }
    captureRules((body) => {
      switch (body.action) {
        case 'list':
          return HttpResponse.json({ rules: [] })
        case 'agent_status':
          statusCalls++
          return HttpResponse.json({ agent: runningAgent })
        default:
          return ok()
      }
    })
    const { unmount } = renderPage(<RulesPage />)

    // 初始一次 + 约 2 秒后的轮询：不手动 refetch，真等定时器。
    await waitFor(() => expect(statusCalls).toBeGreaterThanOrEqual(2), { timeout: 6000 })
    expect(await screen.findByText('运行中')).toBeInTheDocument()

    unmount()
    const atUnmount = statusCalls
    await new Promise((resolve) => setTimeout(resolve, 2400))
    expect(statusCalls).toBe(atUnmount)
  }, 15000)

  it('状态读取失败：行内提示与重试，重试后恢复', async () => {
    let fail = true
    const agent: RuleAgent = { ...mockRuleAgent }
    captureRules((body) => {
      switch (body.action) {
        case 'list':
          return HttpResponse.json({ rules: [] })
        case 'agent_status':
          if (fail) return HttpResponse.json({ error: '状态读取失败' }, { status: 500 })
          return HttpResponse.json({ agent })
        default:
          return ok()
      }
    })
    renderPage(<RulesPage />)
    await screen.findByText('还没有规则')

    expect(await screen.findByText('状态读取失败')).toBeInTheDocument()
    expect(screen.getByText('失败')).toBeInTheDocument()

    fail = false
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText(/让 AI 扫描历史封禁生成候选/)).toBeInTheDocument()
    expect(screen.queryByText('状态读取失败')).not.toBeInTheDocument()
  }, 15000)
})

describe('RulesPage · 列表与新增', () => {
  it('渲染启用/候选与 TP/FP 摘要，点行进详情', async () => {
    captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: mockRules })
      if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
      return ok()
    })
    renderPage(
      <>
        <RulesPage />
        <NavProbe />
      </>,
    )

    expect(await screen.findByText('（mock）兼职押金话术')).toBeInTheDocument()
    expect(screen.getByText('（mock）联系方式引流')).toBeInTheDocument()
    expect(screen.getByText('启用')).toBeInTheDocument()
    expect(screen.getByText('候选')).toBeInTheDocument()
    expect(screen.getByTestId('rule-tpfp-1')).toHaveTextContent('TP 9 · FP 0')
    expect(screen.getByTestId('rule-tpfp-2')).toHaveTextContent('TP 4 · FP 2')

    fireEvent.click(screen.getByText('（mock）联系方式引流'))
    expect(screen.getByTestId('nav-top').textContent).toBe('rule')
  })

  it('手动新增：提交 save 参数正确；400 时保留输入并 toast', async () => {
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [] })
      if (body.action === 'agent_status') return HttpResponse.json({ agent: mockRuleAgent })
      if (body.action === 'save') {
        return HttpResponse.json(
          { error: '规则不能匹配空文本（会命中所有消息）' },
          { status: 400 },
        )
      }
      return ok()
    })
    renderPage(<RulesPage />)
    await screen.findByText('还没有规则')

    fireEvent.click(screen.getByText('＋ 手动新增'))
    expect(await screen.findByText('手动新增规则')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '押金话术' } })
    fireEvent.change(screen.getByLabelText('正则（RE2）'), { target: { value: '押金' } })
    fireEvent.change(screen.getByLabelText('分类（可选）'), { target: { value: 'scam' } })
    fireEvent.change(screen.getByLabelText('备注（可选）'), {
      target: { value: '来自历史封禁' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies.some((b) => b.action === 'save')).toBe(true))
    expect(bodies.find((b) => b.action === 'save')).toEqual({
      action: 'save',
      id: 0,
      name: '押金话术',
      pattern: '押金',
      category: 'scam',
      note: '来自历史封禁',
    })
    // 400：抽屉不关、输入保留、toast 服务端文案
    expect(await screen.findByText('规则不能匹配空文本（会命中所有消息）')).toBeInTheDocument()
    expect(screen.getByLabelText('名称')).toHaveValue('押金话术')
    expect(screen.getByText('手动新增规则')).toBeInTheDocument()
  })

  it('次管：403 且不发 rules 请求', async () => {
    useState({ ...structuredClone(mockState), me: { uid: 200, main: false } })
    const bodies = captureRules(() => ok())
    renderPage(<RulesPage />)

    expect(await screen.findByText('没有权限')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)
  })
})

describe('RuleDetailPage', () => {
  it('fp>0：强制开关禁用并说明原因；测试后误封样本红色可见', async () => {
    const rule: Rule = {
      ...mockRules[0],
      enabled: true,
      last_fp: 2,
      last_undone: 1,
      last_tested_at: 1700000500,
    }
    const testResp: RuleTest = {
      pattern: rule.pattern,
      scanned: 1200,
      matched: 15,
      tp: 12,
      fp: 3,
      undone: 1,
      neutral: 0,
      tp_samples: [
        {
          id: 9812,
          verdict: 'ad',
          action: 'deleted_muted',
          kind: 'scam',
          user_id: 555,
          chat_id: -1001234567890,
          created_at: 1700000000,
          text: '（mock）加我兼职日结，先交押金。',
        },
      ],
      fp_samples: [fpSample],
      undone_samples: [undoneSample],
    }
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [rule] })
      if (body.action === 'test') return HttpResponse.json({ test: testResp })
      return ok()
    })
    renderPage(<RuleDetailPage id={1} />)

    const enforceSwitch = await screen.findByRole('switch', { name: '强制' })
    expect(enforceSwitch).toBeDisabled()
    expect(screen.getByText(/先修净误封/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '重新跑全库测试' }))
    await waitFor(() => expect(bodies.some((b) => b.action === 'test')).toBe(true))

    expect(await screen.findByTestId('rule-test-result')).toBeInTheDocument()
    expect(screen.getByTestId('rule-test-fp')).toHaveTextContent('3')
    expect(screen.getByTestId('rule-test-undone')).toHaveTextContent('1')
    expect(screen.getByTestId('rule-sample-fp')).toBeInTheDocument()
    expect(screen.getByTestId('rule-sample-undone')).toBeInTheDocument()
    expect(screen.getByText('（mock）正常消息被误匹配。')).toBeInTheDocument()
    expect(screen.getByText('误封')).toBeInTheDocument()
    // 这一版规则仍有 fp：测试完仍不允许开启强制
    expect(screen.getByRole('switch', { name: '强制' })).toBeDisabled()
  })

  it('修正后（fp=0 且测试过）可开启强制并提交 enforce', async () => {
    let rule: Rule = {
      ...mockRules[0],
      enabled: true,
      enforce: false,
      last_fp: 0,
      last_undone: 0,
      last_tested_at: 1700000500,
    }
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [rule] })
      if (body.action === 'enforce') {
        rule = { ...rule, enforce: body.enforce === true }
        return ok()
      }
      return ok()
    })
    renderPage(<RuleDetailPage id={1} />)

    const sw = await screen.findByRole('switch', { name: '强制' })
    expect(sw).not.toBeDisabled()
    expect(screen.getByText('开启后命中即最高档处置（零 AI 成本）')).toBeInTheDocument()

    fireEvent.click(sw)
    await waitFor(() =>
      expect(bodies.some((b) => b.action === 'enforce' && b.enforce === true && b.id === 1)).toBe(
        true,
      ),
    )
    await waitFor(() => expect(sw).toBeChecked())
  })

  it('删除：确认文案含规则名，确认后提交 remove', async () => {
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [mockRules[0]] })
      return ok()
    })
    renderPage(<RuleDetailPage id={1} />)

    fireEvent.click(await screen.findByText('删除该规则'))
    expect(await screen.findByText('删除该规则？')).toBeInTheDocument()
    expect(screen.getByText(/将删除规则「（mock）兼职押金话术」/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '删除' }))

    await waitFor(() =>
      expect(bodies.some((b) => b.action === 'remove' && b.id === 1)).toBe(true),
    )
  })

  it('启用 Switch：乐观翻转，服务端确认后保持启用', async () => {
    let rule: Rule = { ...mockRules[1], enabled: false }
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [rule] })
      if (body.action === 'toggle') {
        return gate.then(() => {
          rule = { ...rule, enabled: body.enabled === true }
          return ok()
        })
      }
      return ok()
    })
    renderPage(<RuleDetailPage id={2} />)

    const sw = await screen.findByRole('switch', { name: '启用' })
    expect(sw).not.toBeChecked()
    fireEvent.click(sw)
    // 服务端响应被 gate 拦住时，UI 已经乐观翻转。
    await waitFor(() => expect(sw).toBeChecked())

    act(() => release())
    await waitFor(() =>
      expect(bodies.some((b) => b.action === 'toggle' && b.enabled === true && b.id === 2)).toBe(
        true,
      ),
    )
    // 服务端确认 + 列表刷新后仍是启用。
    await waitFor(() => expect(screen.getByRole('switch', { name: '启用' })).toBeChecked())
  })

  it('启用 Switch：服务端 500 时回滚并 toast 服务端文案', async () => {
    const rule: Rule = { ...mockRules[1], enabled: false }
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [rule] })
      if (body.action === 'toggle') {
        return gate.then(() => HttpResponse.json({ error: '保存失败：规则表被占用' }, { status: 500 }))
      }
      return ok()
    })
    renderPage(<RuleDetailPage id={2} />)

    const sw = await screen.findByRole('switch', { name: '启用' })
    fireEvent.click(sw)
    await waitFor(() => expect(sw).toBeChecked())

    act(() => release())
    await waitFor(() => expect(screen.getByRole('switch', { name: '启用' })).not.toBeChecked())
    expect(await screen.findByText('保存失败：规则表被占用')).toBeInTheDocument()
  })

  it('强制：服务端 400 时回滚并 toast；误封样本仍高亮可见', async () => {
    const rule: Rule = {
      ...mockRules[0],
      enabled: true,
      enforce: false,
      last_fp: 0,
      last_undone: 0,
      last_tested_at: 1700000500,
    }
    const testResp: RuleTest = {
      pattern: rule.pattern,
      scanned: 1200,
      matched: 4,
      tp: 1,
      fp: 3,
      undone: 0,
      neutral: 0,
      tp_samples: [],
      fp_samples: [fpSample],
      undone_samples: [],
    }
    let releaseEnforce!: () => void
    const enforceGate = new Promise<void>((resolve) => {
      releaseEnforce = resolve
    })
    const bodies = captureRules((body) => {
      if (body.action === 'list') return HttpResponse.json({ rules: [rule] })
      if (body.action === 'test') return HttpResponse.json({ test: testResp })
      if (body.action === 'enforce') {
        // 卡住响应先验证乐观翻转，再放行 400 观察回滚。
        return enforceGate.then(() =>
          HttpResponse.json(
            { error: '规则尚未通过全库测试（疑似误封 3 条），不能开启强制' },
            { status: 400 },
          ),
        )
      }
      return ok()
    })
    renderPage(<RuleDetailPage id={1} />)

    // 先跑一次测试：FP 样本红色可见。
    fireEvent.click(await screen.findByRole('button', { name: '重新跑全库测试' }))
    expect(await screen.findByTestId('rule-sample-fp')).toBeInTheDocument()

    const sw = screen.getByRole('switch', { name: '强制' })
    expect(sw).not.toBeDisabled()
    fireEvent.click(sw)
    await waitFor(() => expect(sw).toBeChecked())

    // 服务端 400：乐观翻转回滚，toast 服务端文案；样本仍在。
    act(() => releaseEnforce())
    await waitFor(() => expect(screen.getByRole('switch', { name: '强制' })).not.toBeChecked())
    expect(
      await screen.findByText('规则尚未通过全库测试（疑似误封 3 条），不能开启强制'),
    ).toBeInTheDocument()
    expect(screen.getByTestId('rule-sample-fp')).toBeInTheDocument()
    await waitFor(() => expect(bodies.some((b) => b.action === 'enforce')).toBe(true))
  })
})
