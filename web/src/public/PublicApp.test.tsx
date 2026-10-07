// 公开网页（_w）行为测试：申诉验证页渲染处罚依据与失效态；原文查看页
// 门槛凭据 → 内容；申诉详情页资料渲染。
import { fireEvent, render, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { afterEach, describe, expect, it } from 'vitest'
import { server, startTestServer } from '../test/server'
import type { AppealData, AppealDossierView, JoinVerifyData, LogRecordView } from './api'
import { PublicApp } from './PublicApp'

startTestServer()

/** at 设定当前路径：PublicApp 挂载时按 location.pathname 解析路由。 */
function at(path: string) {
  window.history.pushState({}, '', path)
}

const appealData: AppealData = {
  appeal_id: 7,
  uid: 555,
  sitekey: 'site-key',
  cdata: '7',
  days: 30,
  account: { uid: 555, name: '张三 (@zs)' },
  limits: [
    {
      type: 'message',
      chat_id: -100,
      time: '2026-10-03 20:00',
      reason: '昵称里写着日入5000',
      text: '加微信买号',
    },
  ],
  messages: [
    { chat_id: -100, title: '测试群', text: '加微信买号', time: '10-03 19:58' },
    { chat_id: -100, title: '测试群', text: '日入过万 <b>bold</b>', time: '10-03 19:59' },
  ],
  ai: {
    result: 'uphold',
    label: 'AI 复核后维持原判',
    conf: 0.92,
    model: 'm1',
    reason: '资料仍写着推广',
    statement: '我改资料了',
  },
}

const logView: LogRecordView = {
  record: {
    id: 9,
    user_id: 555,
    user_name: '昵称',
    chat_id: -100,
    chat: '测试群（-100）',
    message_id: 900,
    text: '买号 <script>alert(1)</script>',
    verdict: 'ad',
    confidence: 0.9,
    decider: 'llm',
    kind: 'scam',
    action: 'deleted_muted',
    action_label: '删除+禁言',
    reason: '判定理由',
    created: '2026-10-03 20:00',
  },
  member: { joined: '2026-09-01 00:00', msgs: 3, hits: 1 },
  history: [{ message_id: 900, text: '买号', at: 1, time: '10-03 20:00', blocked: true }],
  logs: [
    {
      id: 9,
      verdict: 'ad',
      confidence: 0.9,
      action: 'deleted_muted',
      action_label: '删除+禁言',
      at: 1,
      time: '10-03 20:00',
    },
  ],
}

const appealView: AppealDossierView = {
  id: 7,
  uid: 555,
  status: '已发解禁码',
  statement: '我是清白的',
  ai_result: '维持原判',
  ai_conf: 95,
  ai_reason: '资料里有推广话术',
  ai_model: 'test/model',
  web_attempts: 1,
  code: 'MSU-AAAA',
  code_expires: '2026-10-04 20:00',
  created: '2026-10-03 20:00',
  u_name: '广告昵称',
  bot: '测试 bot',
  first_seen: '2026-10-03 19:00',
  joined: '2026-10-03 19:00',
  last_msg: '2026-10-03 19:59',
  msgs: 3,
  hits: 1,
  chats: 1,
  gban: '',
  limits: [
    {
      type: 'join_profile',
      label: '进群资料审核限制',
      chat_id: -100,
      chat: '测试群',
      text: '',
      reason: '资料里写着引流',
      at: 1,
      time: '10-03 20:00',
    },
  ],
  penalties: [],
  history: [
    {
      chat: '测试群',
      text: '加微信 日入5000',
      at: 1,
      time: '10-03 20:00',
      mark: '被拦',
      blocked: true,
    },
  ],
  history_more: [],
  history_count: 1,
  logs: [
    {
      id: 9,
      chat_id: -100,
      chat: '测试群',
      verdict: 'ad',
      confidence: 0.99,
      action: 'deleted_muted',
      action_label: '删除+禁言',
      reason: '账号资料与消息都像广告',
      at: 1,
      time: '10-03 20:00',
    },
  ],
  checks: [
    {
      result: 'pass',
      flags: '屏幕尺寸为 0',
      ip: '1.2.3.4',
      fp: 'fphash',
      ua: 'UA/1.0',
      at: 1,
      time: '10-03 20:00',
    },
  ],
  strong: [],
  weak: [],
}

describe('PublicApp', () => {
  it('申诉验证页渲染处罚依据与账号信息', async () => {
    at('/_w/ap/7/sig')
    server.use(http.get('*/_w/ap/7/sig', () => HttpResponse.json(appealData)))
    render(<PublicApp />)

    expect(await screen.findByText('为什么被限制')).toBeInTheDocument()
    expect(screen.getByText(/消息判定处置/)).toBeInTheDocument()
    expect(screen.getByText(/昵称里写着日入5000/)).toBeInTheDocument()
    expect(screen.getByText(/张三 \(@zs\)/)).toBeInTheDocument()
    expect(screen.getByText('AI 复核后维持原判', { exact: false })).toBeInTheDocument()
    expect(screen.getByText(/资料仍写着推广/)).toBeInTheDocument()
    // 人机验证区块挂共享 Turnstile 组件容器。
    expect(screen.getByTestId('turnstile')).toBeInTheDocument()
    // 留底里的 HTML 按文本渲染，不注入 DOM。
    expect(screen.getByText(/<b>bold<\/b>/)).toBeInTheDocument()
  })

  it('过期的申诉链接显示失效文案', async () => {
    at('/_w/ap/7/sig')
    server.use(
      http.get('*/_w/ap/7/sig', () =>
        HttpResponse.json({ error: '链接已失效，请回到 bot 重新申诉。' }, { status: 410 }),
      ),
    )
    render(<PublicApp />)

    expect(await screen.findByText(/链接已失效/)).toBeInTheDocument()
  })

  it('原文查看页：门槛凭据 → POST 后展示内容', async () => {
    at('/_w/v/9/sig')
    const posted: unknown[] = []
    server.use(
      http.get('*/_w/v/9/sig', () =>
        HttpResponse.json({
          gate: { title: '原文与理由', warn: '敏感内容，请勿转发', exp: 123, k: 'kk', ttl_seconds: 300 },
        }),
      ),
      http.post('*/_w/v/9/sig', async ({ request }) => {
        posted.push(await request.json())
        return HttpResponse.json({ view: logView })
      }),
    )
    render(<PublicApp />)

    fireEvent.click(await screen.findByRole('button', { name: '查看内容' }))
    expect((await screen.findAllByText(/买号/)).length).toBeGreaterThan(0)
    expect(posted).toEqual([{ e: 123, k: 'kk' }])
    // 判定详情与留底表格
    expect(screen.getByText('被拦原文')).toBeInTheDocument()
    expect(screen.getByText('该群最近判定记录（1 条）')).toBeInTheDocument()
  })

  it('申诉详情页展示资料栏与解禁码', async () => {
    at('/_w/apv/7/sig')
    server.use(
      http.get('*/_w/apv/7/sig', () =>
        HttpResponse.json({
          gate: { title: '申诉详情', warn: '敏感内容', exp: 123, k: 'kk', ttl_seconds: 300 },
        }),
      ),
      http.post('*/_w/apv/7/sig', () => HttpResponse.json({ view: appealView })),
    )
    render(<PublicApp />)

    fireEvent.click(await screen.findByRole('button', { name: '查看内容' }))
    expect(await screen.findByText('账号信息')).toBeInTheDocument()
    expect(screen.getAllByText(/广告昵称/).length).toBeGreaterThan(0)
    expect(screen.getByText('MSU-AAAA')).toBeInTheDocument()
    expect(screen.getByText(/资料里写着引流/)).toBeInTheDocument()
    expect(screen.getByText(/1\.2\.3\.4/)).toBeInTheDocument()
  })

  it('申诉详情数组字段为 null 时也正常渲染（不再整页白屏）', async () => {
    at('/_w/apv/7/sig')
    // 模拟服务端旧版本/异常返回 null：前端必须自己兜底，否则 React 卸载整页。
    const nullArrays = {
      ...appealView,
      limits: null,
      penalties: null,
      history: null,
      history_more: null,
      logs: null,
      checks: null,
      strong: null,
      weak: null,
    } as unknown as AppealDossierView
    server.use(
      http.get('*/_w/apv/7/sig', () =>
        HttpResponse.json({
          gate: { title: '申诉详情', warn: '敏感内容', exp: 123, k: 'kk', ttl_seconds: 300 },
        }),
      ),
      http.post('*/_w/apv/7/sig', () => HttpResponse.json({ view: nullArrays })),
    )
    render(<PublicApp />)

    fireEvent.click(await screen.findByRole('button', { name: '查看内容' }))
    expect(await screen.findByText('账号信息')).toBeInTheDocument()
    expect(screen.getByText(/生效中限制：0 条/)).toBeInTheDocument()
    expect(screen.queryByTestId('error-boundary')).not.toBeInTheDocument()
  })

  it('路径不是公开网页时给出无效链接提示', async () => {
    at('/other/path')
    render(<PublicApp />)
    expect(await screen.findByText('链接无效或已被替换。')).toBeInTheDocument()
  })
})

const joinVerify: JoinVerifyData = {
  jv_id: 3,
  uid: 555,
  chat_id: -100,
  chat: '测试群',
  provider: 'turnstile',
  sitekey: 'site-key',
  status: 'pending',
  minutes: 10,
  expires: 1893456000,
}

describe('PublicApp · 入群验证页', () => {
  it('展示验证说明与组件容器', async () => {
    at('/_w/jv/3/sig')
    server.use(http.get('*/_w/jv/3/sig', () => HttpResponse.json(joinVerify)))
    render(<PublicApp />)

    expect(await screen.findByText('加入「测试群」')).toBeInTheDocument()
    expect(screen.getByText(/10 分钟内完成/)).toBeInTheDocument()
    expect(screen.getByText(/Cloudflare Turnstile/)).toBeInTheDocument()
    expect(screen.getByTestId('captcha')).toBeInTheDocument()
  })

  it('已完成的验证单显示失效文案', async () => {
    at('/_w/jv/3/sig')
    server.use(
      http.get('*/_w/jv/3/sig', () => HttpResponse.json({ ...joinVerify, status: 'verified' })),
    )
    render(<PublicApp />)
    expect(await screen.findByText(/已经完成或已失效/)).toBeInTheDocument()
  })

  it('未配置提供方时提示联系管理员', async () => {
    at('/_w/jv/3/sig')
    server.use(
      http.get('*/_w/jv/3/sig', () =>
        HttpResponse.json({ ...joinVerify, provider: '', sitekey: '' }),
      ),
    )
    render(<PublicApp />)
    expect(await screen.findByText(/未配置人机验证/)).toBeInTheDocument()
  })
})

describe('PublicApp · 人机验证测试台', () => {
  it('列出已配置的验证方式并提供组件容器', async () => {
    at('/_w/demo/1/x')
    server.use(
      http.get('*/_w/demo/1/x', () =>
        HttpResponse.json({
          providers: [
            { provider: 'hcaptcha', sitekey: 'hsite' },
            { provider: 'cap', sitekey: 'menshen', endpoint: '/cap/menshen/' },
          ],
        }),
      ),
    )
    render(<PublicApp />)

    expect(await screen.findByText('人机验证测试台')).toBeInTheDocument()
    expect(screen.getByText(/hCaptcha/)).toBeInTheDocument()
    expect(screen.getByText(/Cap（内置）/)).toBeInTheDocument()
    expect(screen.getByTestId('captcha-hcaptcha')).toBeInTheDocument()
    expect(screen.getByTestId('captcha-cap')).toBeInTheDocument()
  })

  it('没有配置任何验证方式时给出提示', async () => {
    at('/_w/demo/1/x')
    server.use(http.get('*/_w/demo/1/x', () => HttpResponse.json({ providers: [] })))
    render(<PublicApp />)
    expect(await screen.findByText(/未配置任何验证方式/)).toBeInTheDocument()
  })
})

describe('PublicApp · 申诉验证测试台', () => {
  it('渲染 Turnstile 组件容器并显示 site key 与 cdata', async () => {
    at('/_w/apdemo/1/x')
    server.use(
      http.get('*/_w/apdemo/1/x', () =>
        HttpResponse.json({ sitekey: 'asite', cdata: '1' }),
      ),
    )
    render(<PublicApp />)

    expect(await screen.findByText('申诉验证测试台')).toBeInTheDocument()
    expect(screen.getByTestId('turnstile')).toBeInTheDocument()
    expect(screen.getByText(/site key：asite/)).toBeInTheDocument()
  })

  it('未配置 Turnstile 密钥时给出提示', async () => {
    at('/_w/apdemo/1/x')
    server.use(
      http.get('*/_w/apdemo/1/x', () => HttpResponse.json({ sitekey: '', cdata: '1' })),
    )
    render(<PublicApp />)
    expect(await screen.findByText(/未配置申诉验证的 Turnstile 密钥/)).toBeInTheDocument()
  })
})

// stubMatchMedia 让 jsdom 里的 useMediaQuery 按查询返回指定匹配。
function stubMatchMedia(matches: (q: string) => boolean) {
  window.matchMedia = ((q: string) => ({
    matches: matches(q),
    media: q,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

/** mockAPV 挂上申诉详情的门槛与内容接口。 */
function mockAPV(view: unknown) {
  server.use(
    http.get('*/_w/apv/7/sig', () =>
      HttpResponse.json({
        gate: { title: '申诉详情', warn: '敏感内容', exp: 123, k: 'kk', ttl_seconds: 300 },
      }),
    ),
    http.post('*/_w/apv/7/sig', () => HttpResponse.json({ view })),
  )
}

describe('PublicApp · 申诉详情响应式列数', () => {
  const realMatchMedia = window.matchMedia
  afterEach(() => {
    window.matchMedia = realMatchMedia
  })

  it('≥1400px 三列并排', async () => {
    stubMatchMedia((q) => q.includes('1400') || q.includes('900'))
    at('/_w/apv/7/sig')
    mockAPV(appealView)
    render(<PublicApp />)
    fireEvent.click(await screen.findByRole('button', { name: '查看内容' }))
    expect(await screen.findByTestId('appeal-grid-3')).toBeInTheDocument()
    expect(screen.queryByTestId('appeal-grid-2')).not.toBeInTheDocument()
  })

  it('900–1400px 两列（资料与活动同栏）', async () => {
    stubMatchMedia((q) => q.includes('900') && !q.includes('1400'))
    at('/_w/apv/7/sig')
    mockAPV(appealView)
    render(<PublicApp />)
    fireEvent.click(await screen.findByRole('button', { name: '查看内容' }))
    expect(await screen.findByTestId('appeal-grid-2')).toBeInTheDocument()
    expect(screen.getByTestId('appeal-two-wrap')).toBeInTheDocument()
    expect(screen.queryByTestId('appeal-grid-3')).not.toBeInTheDocument()
  })
})
