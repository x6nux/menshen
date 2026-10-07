// 入群验证页的验证码组件加载与渲染。
//
// 支持三家：Cloudflare Turnstile / hCaptcha / Cap（内置 Go 实现，组件走
// jsdelivr，挑战走同源）。每家脚本只注入一次，render 后返回清理函数。
//
// 这里注入的脚本域必须与 Go 侧 panel.publicShellCSP 放行的来源一致：
// 少放行一个域时浏览器会静默拦下脚本，表现为组件不显示、验证永远不通过，
// 而服务端一行日志都没有。
import { loadTurnstile } from './turnstile'

export interface CaptchaOpts {
  provider: string
  sitekey: string
  /** Cap 专用：<base>/cap/<sitekey>/，由服务端下发。 */
  endpoint?: string
  onToken: (token: string) => void
}

interface HCaptchaApi {
  render: (
    el: HTMLElement,
    opts: { sitekey: string; callback: (token: string) => void },
  ) => string
  reset: (id?: string) => void
}

declare global {
  interface Window {
    hcaptcha?: HCaptchaApi
  }
}

/** mountCaptcha 在 el 内渲染验证码组件，返回卸载函数。 */
export function mountCaptcha(el: HTMLElement, o: CaptchaOpts): () => void {
  let stopped = false
  let teardown: (() => void) | null = null
  const done = (t: string) => {
    if (!stopped && t) o.onToken(t)
  }
  // 脚本是异步注入的：卸载发生在加载完成之前时，立刻执行清理而不是泄漏。
  const adopt = (fn: () => void) => {
    if (stopped) fn()
    else teardown = fn
  }

  switch (o.provider) {
    case 'turnstile':
      void loadTurnstile().then((ts) => {
        if (!ts || stopped) return
        const id = ts.render(el, { sitekey: o.sitekey, callback: done })
        adopt(() => window.turnstile?.remove(id))
      })
      break
    case 'hcaptcha':
      void loadHCaptcha().then((h) => {
        if (!h || stopped) return
        const id = h.render(el, { sitekey: o.sitekey, callback: done })
        adopt(() => h.reset(id))
      })
      break
    case 'cap': {
      // Cap 是原生 web component：建元素、加载模块脚本，等 solve 事件。
      const w = document.createElement('cap-widget')
      if (o.endpoint) w.setAttribute('data-cap-api-endpoint', o.endpoint)
      w.addEventListener('solve', (e) =>
        done((e as CustomEvent<{ token?: string }>).detail?.token ?? ''),
      )
      el.appendChild(w)
      void loadCap()
      adopt(() => w.remove())
      break
    }
  }
  return () => {
    stopped = true
    teardown?.()
  }
}

/** injectScript 注入一个脚本；成功 resolve(true)，失败 resolve(false)。 */
function injectScript(src: string, type?: string): Promise<boolean> {
  return new Promise((resolve) => {
    const s = document.createElement('script')
    if (type) s.type = type
    s.src = src
    s.async = true
    s.defer = true
    s.onload = () => resolve(true)
    s.onerror = () => {
      s.remove()
      resolve(false)
    }
    document.head.appendChild(s)
  })
}

let hcaptchaLoader: Promise<HCaptchaApi | null> | null = null

/**
 * loadHCaptcha 注入 hCaptcha（render=explicit）；失败返回 null 并清掉缓存
 * 的加载 promise，下次调用重新注入。成功路径只认官方 onload 回调：
 * 脚本文件加载完不等于 API 就绪，提前 resolve 会把未就绪当成失败。
 */
export function loadHCaptcha(): Promise<HCaptchaApi | null> {
  if (window.hcaptcha?.render) return Promise.resolve(window.hcaptcha)
  if (hcaptchaLoader) return hcaptchaLoader
  hcaptchaLoader = new Promise((resolve) => {
    ;(window as unknown as { msHCaptchaOnload?: () => void }).msHCaptchaOnload =
      () => resolve(window.hcaptcha ?? null)
    void injectScript(
      'https://js.hcaptcha.com/1/api.js?render=explicit&onload=msHCaptchaOnload',
    ).then((ok) => {
      if (!ok) {
        hcaptchaLoader = null
        resolve(null)
      }
    })
  })
  return hcaptchaLoader
}

let capLoader: Promise<void> | null = null

/** loadCap 注入 Cap 的 web component（模块脚本）；注册后自定义元素自动升级。 */
export function loadCap(): Promise<void> {
  if (capLoader) return capLoader
  capLoader = injectScript('https://cdn.jsdelivr.net/npm/cap-widget', 'module').then((ok) => {
    if (!ok) capLoader = null
  })
  return capLoader
}
