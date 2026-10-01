// msw handlers：读接口返回 fixtures，写接口统一回成功。
// 开发（VITE_MOCK=1）与测试可复用；真实校验逻辑属于后端测试范围。
import { HttpResponse, http } from 'msw'
import type { LogRow } from '../api/types'
import {
  mockAppealDetail,
  mockAppeals,
  mockLogDetail,
  mockLogs,
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
  http.post('*/miniapp/api/user', () => HttpResponse.json(mockUser)),
  http.post('*/miniapp/api/appeals', () => HttpResponse.json({ appeals: mockAppeals, page: 1, total: mockAppeals.length })),
  http.post('*/miniapp/api/appeal', () => HttpResponse.json(mockAppealDetail)),
  // 其余写操作（set/bot/chat/upstream/model/admin/gban/gbanown/whitelist/digest/）
  // logact/appealact）统一返回成功；note 与真实服务端文案格式一致。
  http.post('*/miniapp/api/:op', () => HttpResponse.json({ ok: true, note: '（mock）已保存' })),
]
