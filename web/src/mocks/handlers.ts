// msw handlers：读接口返回 fixtures，写接口统一回成功。
// 开发（VITE_MOCK=1）与测试可复用；真实校验逻辑属于后端测试范围。
import { HttpResponse, http } from 'msw'
import type { LogRow } from '../api/types'
import {
  mockAppealDetail,
  mockAppeals,
  mockLogDetail,
  mockLogs,
  mockRuleAgent,
  mockRuleTest,
  mockRules,
  mockState,
  mockUser,
} from './fixtures'

async function bodyOf(request: Request): Promise<Record<string, unknown>> {
  try {
    return (await request.json()) as Record<string, unknown>
  } catch {
    return {}
  }
}

function filterLogs(verdict: string): LogRow[] {
  switch (verdict) {
    case 'deleted':
      return mockLogs.filter((l) => l.action.startsWith('deleted'))
    case 'ad':
      return mockLogs.filter((l) => l.verdict === 'ad')
    case 'clean':
      return mockLogs.filter((l) => l.verdict === 'clean' || l.verdict === 'none')
    case 'skipped':
      return mockLogs.filter((l) => l.verdict === 'skipped')
    default:
      return mockLogs
  }
}

/** 与后端一致的搜索口径：原文/理由/uid/群号。 */
function searchLogs(rows: LogRow[], q: string): LogRow[] {
  const query = q.trim().toLowerCase()
  if (!query) return rows
  return rows.filter((l) =>
    `${l.text} ${l.reason} ${l.user_id} ${l.chat_id}`.toLowerCase().includes(query),
  )
}

const LOG_PAGE_SIZE = 20

/** 未结申诉状态（与后端 store.AppealOpenStatusesSQL 一致），mock 列表筛选用。 */
const APPEAL_OPEN_STATUSES = ['statement', 'ai', 'web', 'noweb', 'code']

export const handlers = [
  http.post('*/miniapp/api/state', () => HttpResponse.json(mockState)),
  http.post('*/miniapp/api/logs', async ({ request }) => {
    const body = await bodyOf(request)
    const filtered = searchLogs(filterLogs(String(body.verdict ?? '')), String(body.q ?? ''))
    const page = Math.max(1, Number(body.page ?? 1) || 1)
    const start = (page - 1) * LOG_PAGE_SIZE
    // total 是筛选后的总数，logs 是当前页切片——与后端分页行为一致。
    return HttpResponse.json({
      logs: filtered.slice(start, start + LOG_PAGE_SIZE),
      page,
      total: filtered.length,
    })
  }),
  http.post('*/miniapp/api/log', () => HttpResponse.json(mockLogDetail)),
  http.post('*/miniapp/api/user', async ({ request }) => {
    const body = await bodyOf(request)
    const all = body.filter === 'all'
    // 「被处置过」= 有处置动作（与后端 ProcessedCond 的口径一致，mock 用 action!='none' 近似）。
    const filtered = all ? mockUser.logs : mockUser.logs.filter((l) => l.action !== 'none')
    const page = Math.max(1, Number(body.page ?? 1) || 1)
    const start = (page - 1) * LOG_PAGE_SIZE
    return HttpResponse.json({
      ...mockUser,
      filter: all ? 'all' : 'act',
      logs: filtered.slice(start, start + LOG_PAGE_SIZE),
      shown: filtered.length,
      page,
    })
  }),
  http.post('*/miniapp/api/appeals', async ({ request }) => {
    const body = await bodyOf(request)
    const filtered =
      body.filter === 'open'
        ? mockAppeals.filter((a) => APPEAL_OPEN_STATUSES.includes(a.status))
        : mockAppeals
    const page = Math.max(1, Number(body.page ?? 1) || 1)
    const start = (page - 1) * LOG_PAGE_SIZE
    return HttpResponse.json({
      appeals: filtered.slice(start, start + LOG_PAGE_SIZE),
      page,
      total: filtered.length,
    })
  }),
  http.post('*/miniapp/api/appeal', () => HttpResponse.json(mockAppealDetail)),
  // AI 必封规则（主管理员专属）：列表/状态读 fixtures，写操作只在 mock 里回
  // 成功形状，真实校验（空文本正则、防误封门）以后端测试为准。
  http.post('*/miniapp/api/rules', async ({ request }) => {
    const body = await bodyOf(request)
    switch (String(body.action ?? '')) {
      case 'list':
        return HttpResponse.json({ rules: mockRules })
      case 'agent_status':
        return HttpResponse.json({ agent: mockRuleAgent })
      case 'agent_start':
        return HttpResponse.json({
          ok: true,
          agent: { ...mockRuleAgent, running: true, started_at: 1700000600 },
        })
      case 'agent_stop':
        return HttpResponse.json({ ok: true, stopped: true })
      case 'test':
        return HttpResponse.json({ test: mockRuleTest })
      case 'save':
        return HttpResponse.json({ ok: true, id: 3, test: mockRuleTest })
      default:
        return HttpResponse.json({ ok: true, note: '（mock）已保存' })
    }
  }),
  // 其余写操作（set/bot/chat/upstream/model/admin/gban/gbanown/whitelist/digest/
  // logact/appealact）统一返回成功；note 与真实服务端文案格式一致。
  http.post('*/miniapp/api/:op', () => HttpResponse.json({ ok: true, note: '（mock）已保存' })),
]
