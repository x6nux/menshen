// Telegram WebApp 桥：复刻旧页面的 3 秒轮询（等 telegram-web-app.js 加载），
// 就绪后 ready/expand，并把 BackButton、themeChanged、openLink 收成一个对象，
// 供导航栈与主题重建使用。SDK 不可达时返回 available=false，不抛错。
import type { MiniThemeParams } from './theme'

/** Telegram WebApp 里用到的部分；只声明用得上的方法，便于测试注入。 */
export interface TelegramWebAppLike {
  initData?: string
  colorScheme?: string
  themeParams?: Record<string, string>
  ready(): void
  expand(): void
  onEvent?(event: string, cb: () => void): void
  offEvent?(event: string, cb: () => void): void
  openLink?(url: string): void
  BackButton?: {
    show(): void
    hide(): void
    onClick(cb: () => void): void
    offClick?(cb: () => void): void
  }
}

/** 主题的一次实时读数；themeChanged 事件后由 getTheme 重新读取。 */
export interface TelegramTheme {
  colorScheme: string
  themeParams: MiniThemeParams
}

/** BackButtonLike 与导航栈对接的最小接口（NavProvider 只依赖它）。 */
export interface TelegramBackButton {
  show(): void
  hide(): void
  /** 注册点击回调，返回取消订阅函数。 */
  onClick(cb: () => void): () => void
}

export interface TelegramBridge {
  /** SDK 是否就绪；false 时页面应渲染「请通过 Telegram 打开」的引导。 */
  available: boolean
  initData: string
  /** URL ?bot= 指定要管理的 bot，缺省 '0'（主 bot）。 */
  botId: string
  /**
   * getTheme 每次现读 SDK 的 colorScheme/themeParams（SDK 会先更新属性
   * 再派发 themeChanged），因此主题必须通过它取，不能缓存成静态快照。
   */
  getTheme(): TelegramTheme
  /** 订阅主题切换；回调参数是事件触发时的最新主题，返回取消订阅函数。 */
  onThemeChanged(cb: (theme: TelegramTheme) => void): () => void
  backButton: TelegramBackButton
  openLink(url: string): void
}

export interface InitTelegramOptions {
  /** 最长等待毫秒，默认 3000（旧页面为 30 次 ×100ms）。 */
  timeoutMs?: number
  /** 轮询间隔毫秒，默认 100。 */
  intervalMs?: number
  /** 注入等待实现；测试可传同步实现避免真等 3 秒。 */
  sleep?: (ms: number) => Promise<void>
  /** 读取 WebApp 对象；默认 window.Telegram?.WebApp。 */
  getWebApp?: () => TelegramWebAppLike | undefined
}

declare global {
  interface Window {
    Telegram?: { WebApp?: TelegramWebAppLike }
  }
}

function defaultGetWebApp(): TelegramWebAppLike | undefined {
  if (typeof window === 'undefined') return undefined
  return window.Telegram?.WebApp
}

/** botIdFromURL 从 ?bot= 读目标 bot；缺省 '0'。 */
export function botIdFromURL(search?: string): string {
  const qs = search ?? (typeof location !== 'undefined' ? location.search : '')
  return new URLSearchParams(qs).get('bot') || '0'
}

/** openLink 在页面里打开外部链接：优先 Telegram SDK（App 内打开），回退新窗口。 */
export function openLink(url: string): void {
  if (!url) return
  const app = tryGetWebApp(defaultGetWebApp)
  if (app?.openLink) app.openLink(url)
  else if (typeof window !== 'undefined') window.open(url, '_blank')
}

function tryGetWebApp(getter: () => TelegramWebAppLike | undefined): TelegramWebAppLike | undefined {
  try {
    return getter()
  } catch {
    // SDK 半加载状态下的访问异常不影响轮询与超时兜底。
    return undefined
  }
}

/** readTheme 从 SDK 对象现读主题；对象缺失时给出空主题兜底。 */
function readTheme(app: TelegramWebAppLike | undefined): TelegramTheme {
  return {
    colorScheme: app?.colorScheme ?? '',
    themeParams: { ...(app?.themeParams ?? {}) },
  }
}

function unavailableBridge(botId: string): TelegramBridge {
  const noop = () => {}
  return {
    available: false,
    initData: '',
    botId,
    getTheme: () => readTheme(undefined),
    onThemeChanged: () => noop,
    backButton: { show: noop, hide: noop, onClick: () => noop },
    openLink: (url) => {
      // 没有 SDK 也要能打开原文查看页。
      if (typeof window !== 'undefined') window.open(url, '_blank')
    },
  }
}

/** initTelegram 轮询等待 SDK 就绪；超时（默认 3 秒）返回不可用桥。 */
export async function initTelegram(options: InitTelegramOptions = {}): Promise<TelegramBridge> {
  const timeoutMs = options.timeoutMs ?? 3000
  const intervalMs = options.intervalMs ?? 100
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)))
  const getWebApp = options.getWebApp ?? defaultGetWebApp
  const maxTries = Math.max(1, Math.ceil(timeoutMs / intervalMs))

  let wa = tryGetWebApp(getWebApp)
  for (let i = 0; !wa && i < maxTries; i++) {
    await sleep(intervalMs)
    wa = tryGetWebApp(getWebApp)
  }

  const botId = botIdFromURL()
  if (!wa) return unavailableBridge(botId)
  const app = wa

  // ready/expand 分开兜底：个别旧客户端只会在其中一个上抛。
  try {
    app.ready()
  } catch {
    // 忽略：页面继续跑。
  }
  try {
    app.expand()
  } catch {
    // 忽略：页面继续跑。
  }

  return {
    available: true,
    initData: app.initData ?? '',
    botId,
    getTheme: () => readTheme(app),
    onThemeChanged: (cb) => {
      // 事件回调不直接透传：由桥在触发时现读主题，订阅者拿到的一定是最新值。
      const handler = () => cb(readTheme(app))
      app.onEvent?.('themeChanged', handler)
      return () => app.offEvent?.('themeChanged', handler)
    },
    backButton: {
      show: () => app.BackButton?.show(),
      hide: () => app.BackButton?.hide(),
      onClick: (cb) => {
        app.BackButton?.onClick(cb)
        return () => app.BackButton?.offClick?.(cb)
      },
    },
    openLink: (url) => {
      if (app.openLink) app.openLink(url)
      else window.open(url, '_blank')
    },
  }
}
