// Turnstile 显式渲染模式的加载器：脚本只注入一次，render 返回的 widget
// id 供卸载时 remove。

export interface TurnstileApi {
  render: (
    el: HTMLElement,
    opts: {
      sitekey: string
      action?: string
      cdata?: string
      callback?: (token: string) => void
      'error-callback'?: () => void
      'expired-callback'?: () => void
    },
  ) => string
  remove: (id: string) => void
}

declare global {
  interface Window {
    turnstile?: TurnstileApi
  }
}

let loader: Promise<TurnstileApi | null> | null = null

/** loadTurnstile 注入官方脚本（render=explicit）；失败返回 null。 */
export function loadTurnstile(): Promise<TurnstileApi | null> {
  if (window.turnstile) return Promise.resolve(window.turnstile)
  if (loader) return loader
  loader = new Promise((resolve) => {
    const s = document.createElement('script')
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    s.async = true
    s.defer = true
    s.onload = () => resolve(window.turnstile ?? null)
    s.onerror = () => resolve(null)
    document.head.appendChild(s)
  })
  return loader
}
