// AI 必封规则：Agent 启停/步骤/完成后刷新列表、列表徽标与 TP/FP 摘要、
// 详情全库误封测试与强制防误封门、手动新增参数与 400 保留输入、
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
function captureRules(handler: (body: RulesBody, n: number) => Response) {
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
})
