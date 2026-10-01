// 自研导航栈（计划 2.2）：tab + 页面栈，二级页与 Telegram BackButton 双绑。
// 不引路由库；页面状态（筛选、搜索词）留在各页面组件里，导航只记位置。
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
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
  | { k: 'settings' }

export interface NavValue {
  tab: TabKey
  stack: Page[]
  /** push 进入一个二级页（列表 → 详情）；与栈顶同页时忽略，避免双击叠栈。 */
  push(page: Page): void
  /** pop 返回上一层；已在顶层时不动作。 */
  pop(): void
  /** switchTab 切一级 Tab，并清空页面栈。 */
  switchTab(tab: TabKey): void
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
    case 'lists':
      return (a.section ?? '') === ((b as Extract<Page, { k: 'lists' }>).section ?? '')
    default:
      // upstreams / models / settings 没有参数，kind 相同就是同一页。
      return true
  }
}

const NavContext = createContext<NavValue | null>(null)

export interface NavProviderProps {
  children: ReactNode
  /** Telegram BackButton 桥；缺省（无 SDK/测试）时只维护栈，不碰按钮。 */
  backButton?: TelegramBackButton
}

export function NavProvider({ children, backButton }: NavProviderProps) {
  const [tab, setTab] = useState<TabKey>('overview')
  const [stack, setStack] = useState<Page[]>([])

  const push = useCallback(
    (page: Page) =>
      setStack((s) => (s.length > 0 && samePage(s[s.length - 1], page) ? s : [...s, page])),
    [],
  )
  const pop = useCallback(() => setStack((s) => (s.length ? s.slice(0, -1) : s)), [])
  const switchTab = useCallback((next: TabKey) => {
    setTab(next)
    setStack([])
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
    () => ({ tab, stack, push, pop, switchTab }),
    [tab, stack, push, pop, switchTab],
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
