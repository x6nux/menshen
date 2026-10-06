// 导航栈（计划 2.2）：tab + 页面栈，二级页与 Telegram BackButton 双绑。
//
// URL 是导航状态的唯一镜像：位置（tab + 栈）编进路径，页面内的筛选/搜索/分段
// 通过 usePageParam 写进查询串。刷新、深链、浏览器前进后退都据此还原。
//
//   - push/switchTab 改路径 → history.pushState（浏览器后退可回到上一处）；
//   - pop（含 Telegram BackButton）与筛选变化 → history.replaceState，不新增历史；
//   - popstate 时反过来从 URL 解析导航状态。
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { pathOf, parseRoute, routeSlug } from './route'
import type { TelegramBackButton } from './telegram'

export type TabKey = 'overview' | 'bots' | 'chats' | 'records' | 'mine'

export type Page =
  | { k: 'bot'; id: number }
  | { k: 'chat'; botId: number; chatId: number }
  | { k: 'log'; id: number }
  | { k: 'user'; id: number }
  | { k: 'appeal'; id: number }
  | { k: 'lists'; section?: string }
  | { k: 'upstreams' }
  | { k: 'upstream'; id: number }
  | { k: 'models' }
  | { k: 'model'; name: string }
  | { k: 'rules' }
  | { k: 'rule'; id: number }
  | { k: 'settings' }
  | { k: 'syslog' }

export interface NavValue {
  tab: TabKey
  stack: Page[]
  /**
   * 跨页传参：switchTab 时可带一个意图串（如 'appeals'、'dryrun'、'bot:12'），
   * 目标页启动时读取一次做预置筛选/定位；push 二级页或再次 switchTab 时清空。
   * 随 URL 的 ?intent= 往返，刷新后仍在，直到被消费或覆盖。
   */
  intent: string | null
  /** push 进入一个二级页（列表 → 详情）；与栈顶同页时忽略，避免双击叠栈。 */
  push(page: Page): void
  /** pop 返回上一层；已在顶层时不动作。 */
  pop(): void
  /** switchTab 切一级 Tab，并清空页面栈；intent 缺省即清空。 */
  switchTab(tab: TabKey, intent?: string): void
  /** clearIntent 消费掉当前意图（目标页读取后调用），避免导航往返重复应用。 */
  clearIntent(): void
  /** setPageParam 由 usePageParam 调用：登记当前页面的一项查询参数并同步到 URL。 */
  setPageParam(scope: string, key: string, value: string): void
}

/**
 * samePage 判断两个页面是否指向同一处：有标识的页按 kind + 标识比较，
 * 无标识的页（upstreams/models/settings）同 kind 即同页，lists 再看 section。
 */
function samePage(a: Page, b: Page): boolean {
  if (a.k !== b.k) return false
  switch (a.k) {
    case 'bot':
      return a.id === (b as Extract<Page, { k: 'bot' }>).id
    case 'chat':
      return (
        a.botId === (b as Extract<Page, { k: 'chat' }>).botId &&
        a.chatId === (b as Extract<Page, { k: 'chat' }>).chatId
      )
    case 'log':
      return a.id === (b as Extract<Page, { k: 'log' }>).id
    case 'user':
      return a.id === (b as Extract<Page, { k: 'user' }>).id
    case 'appeal':
      return a.id === (b as Extract<Page, { k: 'appeal' }>).id
    case 'upstream':
      return a.id === (b as Extract<Page, { k: 'upstream' }>).id
    case 'model':
      return a.name === (b as Extract<Page, { k: 'model' }>).name
    case 'rule':
      return a.id === (b as Extract<Page, { k: 'rule' }>).id
    case 'lists':
      return (a.section ?? '') === ((b as Extract<Page, { k: 'lists' }>).section ?? '')
    default:
      // upstreams / models / rules / settings 没有参数，kind 相同就是同一页。
      return true
  }
}

const NavContext = createContext<NavValue | null>(null)

interface ScopeValue {
  scope: string
  active: boolean
  initial: URLSearchParams
}
const ScopeContext = createContext<ScopeValue | null>(null)

export interface PageScopeProps {
  /** scope 标识当前页面（用 routeSlug），参数按它分组，避免隐藏页互相覆盖。 */
  scope: string
  /** active=false 时（被二级页盖住的 tab 根页）只保留内存状态，不写 URL。 */
  active: boolean
  children: ReactNode
}

/**
 * PageScope 圈出「当前可见页面」。只有 active 的页面会把自己的查询参数写进 URL，
 * 被二级页盖住、仍在挂载的 tab 根页只保住内存状态，返回时再写回去。
 * 调用方要用 routeSlug 作 key，使换页时重新取一次 URL 上的初始值。
 */
export function PageScope({ scope, active, children }: PageScopeProps) {
  const [initial] = useState(() =>
    new URLSearchParams(typeof location !== 'undefined' ? location.search : ''),
  )
  const value = useMemo<ScopeValue>(() => ({ scope, active, initial }), [scope, active, initial])
  return <ScopeContext.Provider value={value}>{children}</ScopeContext.Provider>
}

/**
 * usePageParam 把一个页面级状态（搜索词、筛选、分段）绑定到查询参数：
 * 初值来自 URL（缺省 fallback），变化时同步回去。valid 给定时，URL 上的非法值
 * 一律退回 fallback，避免手改地址栏弄出非法筛选。
 * 没有 PageScope（单元测试直接渲染页面）时退化成普通 useState。
 */
// oxlint-disable-next-line react/only-export-components -- 与 NavProvider 共享 context，拆文件反而绕。
export function usePageParam<T extends string = string>(
  key: string,
  fallback: string = '',
  valid?: readonly string[],
): [T, (next: T) => void] {
  const nav = useContext(NavContext)
  const scope = useContext(ScopeContext)
  const [value, setValue] = useState<T>(() => {
    if (!scope || !scope.active) return fallback as T
    const raw = scope.initial.get(key)
    if (raw === null) return fallback as T
    if (valid && !valid.includes(raw)) return fallback as T
    return raw as T
  })
  const setPageParam = nav?.setPageParam
  useEffect(() => {
    if (!setPageParam || !scope || !scope.active) return
    setPageParam(scope.scope, key, value === fallback ? '' : value)
  }, [setPageParam, scope, key, value, fallback])
  return [value, setValue]
}

// urlBot 读网页版登录链接带进来的目标 bot（/admin/?bot=123）。它不属于导航状态，
// 但一旦丢失刷新就会退回主 bot；我们写 URL 时始终带上，因此从当前地址现读即可。
function urlBot(): string | null {
  if (typeof location === 'undefined') return null
  return new URLSearchParams(location.search).get('bot')
}

/**
 * buildUrl 组装「路径 + 查询串」。
 *
 * 页面参数按 scope 分组，只有当前路由那个 scope 的值会进 URL；其余 scope 的键
 * （上一个页面留下的）一律清掉。既不属于任何已登记页面的键原样保留——刷新或
 * 深链时页面可能还没挂载（外壳先渲染骨架），这时不能把它的参数提前抹掉。
 */
function buildUrl(
  tab: TabKey,
  stack: Page[],
  params: Map<string, Record<string, string>>,
  intent: string | null,
): string {
  const owned = new Set<string>()
  for (const bag of params.values()) {
    for (const key of Object.keys(bag)) owned.add(key)
  }

  const q = new URLSearchParams()
  const bot = urlBot()
  if (bot) q.set('bot', bot)
  if (intent) q.set('intent', intent)
  if (typeof location !== 'undefined') {
    for (const [key, value] of new URLSearchParams(location.search)) {
      if (key === 'bot' || key === 'intent' || owned.has(key)) continue
      q.set(key, value)
    }
  }

  const bag = params.get(routeSlug(tab, stack)) ?? {}
  // 按 key 排序，让相同状态得到相同 URL（避免无谓的历史写入）。
  for (const key of Object.keys(bag).sort()) {
    if (bag[key] !== '') q.set(key, bag[key])
  }
  const qs = q.toString()
  return pathOf(tab, stack) + (qs ? `?${qs}` : '')
}

export interface NavProviderProps {
  children: ReactNode
  /** Telegram BackButton 桥；缺省（无 SDK/测试）时只维护栈，不碰按钮。 */
  backButton?: TelegramBackButton
}

/**
 * NavState 把导航位置、当前页面的查询参数与「这次变化该怎么写历史」放在
 * 同一个 state 里：单一 effect 负责拼 URL，就不必在渲染期读写 ref 来同步。
 */
interface NavState {
  tab: TabKey
  stack: Page[]
  intent: string | null
  /** scope（routeSlug）→ 该页的查询参数。只有当前 scope 会被写进 URL。 */
  params: Map<string, Record<string, string>>
  /** 位置变了用 push（浏览器可后退），筛选变了用 replace。 */
  mode: 'push' | 'replace'
}

export function NavProvider({ children, backButton }: NavProviderProps) {
  // 初始位置来自 URL：刷新与深链都从这里还原。
  const [nav, setNav] = useState<NavState>(() => ({
    ...parseRoute(typeof location !== 'undefined' ? location.pathname : '/'),
    intent:
      typeof location !== 'undefined' ? new URLSearchParams(location.search).get('intent') : null,
    params: new Map(),
    mode: 'replace',
  }))
  const { tab, stack, intent } = nav

  // 唯一的写 URL 处：导航位置与页面参数都从这里落到地址栏。
  useEffect(() => {
    if (typeof window === 'undefined') return
    const url = buildUrl(nav.tab, nav.stack, nav.params, nav.intent)
    const current = location.pathname + location.search
    if (url === current) return
    if (nav.mode === 'push') history.pushState(null, '', url)
    else history.replaceState(null, '', url)
  }, [nav])

  const push = useCallback((page: Page) => {
    setNav((prev) => {
      const top = prev.stack[prev.stack.length - 1]
      // 与栈顶同页时忽略，避免双击叠栈；进入二级页后面向列表的意图不再适用。
      if (top && samePage(top, page)) {
        return prev.intent === null ? prev : { ...prev, intent: null, mode: 'replace' }
      }
      return { ...prev, stack: [...prev.stack, page], intent: null, mode: 'push' }
    })
  }, [])

  const pop = useCallback(() => {
    setNav((prev) =>
      prev.stack.length === 0
        ? prev
        : // 用 replace 而不是 back()：深链进入时没有上一页可退，返回按钮必须仍可用。
          { ...prev, stack: prev.stack.slice(0, -1), mode: 'replace' },
    )
  }, [])

  const switchTab = useCallback((next: TabKey, nextIntent?: string) => {
    setNav((prev) => ({ ...prev, tab: next, stack: [], intent: nextIntent ?? null, mode: 'push' }))
  }, [])

  const clearIntent = useCallback(() => {
    setNav((prev) => (prev.intent === null ? prev : { ...prev, intent: null, mode: 'replace' }))
  }, [])

  const setPageParam = useCallback((scope: string, key: string, value: string) => {
    setNav((prev) => {
      const bag = prev.params.get(scope) ?? {}
      if (bag[key] === value) return prev
      const params = new Map(prev.params)
      params.set(scope, { ...bag, [key]: value })
      // 页面参数变化不新增历史项：同一处筛选不同，不应占一条后退记录。
      return { ...prev, params, mode: 'replace' }
    })
  }, [])

  // 浏览器前进/后退：从 URL 反解导航状态。页面参数由 PageScope 在换页时重新读取。
  useEffect(() => {
    const onPop = () => {
      const route = parseRoute(location.pathname)
      const nextIntent = new URLSearchParams(location.search).get('intent')
      setNav((prev) => ({
        ...prev,
        tab: route.tab,
        stack: route.stack,
        intent: nextIntent,
        mode: 'replace',
      }))
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // BackButton 的点击回调只注册一次，通过 ref 读最新的 pop。
  const popRef = useRef(pop)
  useEffect(() => {
    popRef.current = pop
  }, [pop])

  useEffect(() => {
    if (!backButton) return
    if (stack.length > 0) backButton.show()
    else backButton.hide()
  }, [backButton, stack.length])

  useEffect(() => {
    if (!backButton) return
    return backButton.onClick(() => popRef.current())
  }, [backButton])

  const value = useMemo<NavValue>(
    () => ({ tab, stack, intent, push, pop, switchTab, clearIntent, setPageParam }),
    [tab, stack, intent, push, pop, switchTab, clearIntent, setPageParam],
  )
  return <NavContext.Provider value={value}>{children}</NavContext.Provider>
}

/** useNav 必须在 NavProvider 内使用。 */
// oxlint-disable-next-line react/only-export-components -- Provider 与 hook 必须共享同一个 context，拆分反而绕。
export function useNav(): NavValue {
  const value = useContext(NavContext)
  if (!value) throw new Error('useNav 必须在 NavProvider 内使用')
  return value
}
