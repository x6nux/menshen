// Turnstile 显式渲染模式的加载器：脚本只注入一次，render 返回的 widget
// id 供卸载时 remove。
//
// 参数名以官方 API 为准：显式渲染传的是 cData（驼峰），写成 cdata 会被
// 组件静默忽略——令牌里不带自定义数据，服务端按 cdata 核对时必然失败。

export interface TurnstileApi {
  render: (
    el: HTMLElement,
    opts: {
      sitekey: string
      action?: string
      /** 官方显式渲染参数是 cData；siteverify 响应里才叫 cdata。 */
      cData?: string
      callback?: (token: string) => void
      'error-callback'?: (code?: string) => void
      'expired-callback'?: () => void
      /** token 过期行为；manual = 等用户点组件上的刷新，不自动重发。 */
      'refresh-expired'?: 'auto' | 'manual' | 'never'
    },
  ) => string
  remove: (id: string) => void
  reset: (id?: string) => void
}

declare global {
  interface Window {
    turnstile?: TurnstileApi
  }
}

let loader: Promise<TurnstileApi | null> | null = null

/**
 * loadTurnstile 注入官方脚本（render=explicit）；失败返回 null 并清掉缓存
 * 的加载 promise，下次调用重新注入——否则一次网络抖动就把组件判死刑，
 * 只能整页刷新才恢复。
 */
export function loadTurnstile(): Promise<TurnstileApi | null> {
  if (window.turnstile) return Promise.resolve(window.turnstile)
  if (loader) return loader
  loader = new Promise((resolve) => {
    const s = document.createElement('script')
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    s.async = true
    s.defer = true
    s.onload = () => {
      if (!window.turnstile) loader = null
      resolve(window.turnstile ?? null)
    }
    s.onerror = () => {
      s.remove()
      loader = null
      resolve(null)
    }
    document.head.appendChild(s)
  })
  return loader
}
