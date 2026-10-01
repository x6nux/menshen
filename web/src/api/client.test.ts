import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { ApiError, api, setApiAuth } from './client'

const server = setupServer()

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  setApiAuth({ initData: '', botId: '0' })
})
afterAll(() => server.close())

describe('api', () => {
  it('POST 到 /miniapp/api/{op}，带上 JSON 与鉴权头', async () => {
    let seen: { initData: string | null; botId: string | null; body: unknown } | null = null
    server.use(
      http.post('*/miniapp/api/state', async ({ request }) => {
        seen = {
          initData: request.headers.get('X-Tg-Init-Data'),
          botId: request.headers.get('X-Bot-Id'),
          body: await request.json(),
        }
        return HttpResponse.json({ me: { uid: 1, main: true } })
      }),
    )
    setApiAuth({ initData: 'query_id=abc', botId: '42' })
    const data = await api<{ me: { uid: number } }>('state', { page: 2 })
    expect(data.me.uid).toBe(1)
    expect(seen).toEqual({ initData: 'query_id=abc', botId: '42', body: { page: 2 } })
  })

  it('未就绪时头传空串与 0', async () => {
    let seen: { initData: string | null; botId: string | null } | null = null
    server.use(
      http.post('*/miniapp/api/state', ({ request }) => {
        seen = {
          initData: request.headers.get('X-Tg-Init-Data'),
          botId: request.headers.get('X-Bot-Id'),
        }
        return HttpResponse.json({})
      }),
    )
    await api('state')
    expect(seen).toEqual({ initData: '', botId: '0' })
  })

  it.each([
    [400, '取值非法：0-100 的整数'],
    [401, 'initData 已过期，请重新打开'],
    [403, '你不是本服务的管理员'],
  ])('HTTP %i 抛 ApiError 并带上服务端 error 文案', async (status, message) => {
    server.use(http.post('*/miniapp/api/set', () => HttpResponse.json({ error: message }, { status })))
    const err = await api('set', { key: 'x' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(status)
    expect((err as ApiError).message).toBe(message)
  })

  it('非 JSON 错误响应回退 HTTP 状态文案', async () => {
    server.use(http.post('*/miniapp/api/log', () => new HttpResponse('boom', { status: 500 })))
    const err = (await api('log', { id: 1 }).catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(500)
    expect(err.message).toBe('HTTP 500')
  })

  it('网络失败抛 ApiError{status:0}', async () => {
    server.use(http.post('*/miniapp/api/state', () => HttpResponse.error()))
    const err = (await api('state').catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
  })

  it('AbortSignal 透传：中断后以 ApiError{status:0} 冒泡', async () => {
    server.use(
      http.post('*/miniapp/api/state', async () => {
        await new Promise((resolve) => setTimeout(resolve, 30))
        return HttpResponse.json({})
      }),
    )
    const controller = new AbortController()
    const pending = api('state', undefined, controller.signal)
    controller.abort()
    const err = (await pending.catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
  })

  it('2xx 但响应不是 JSON 抛「响应格式错误」', async () => {
    server.use(http.post('*/miniapp/api/state', () => new HttpResponse('not json', { status: 200 })))
    const err = (await api('state').catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(200)
    expect(err.message).toBe('响应格式错误')
  })
})
