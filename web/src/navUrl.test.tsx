// URL 即导航状态：位置进路径、页面筛选进查询串。这些用例覆盖刷新不丢进度
// 依赖的四件事——写地址、读地址、浏览器前进后退、隐藏页不越权写参数。
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { NavProvider, PageScope, useNav, usePageParam } from './nav'

function NavProbe() {
  const nav = useNav()
  return (
    <div>
      <div data-testid="tab">{nav.tab}</div>
      <div data-testid="stack">{nav.stack.map((p) => p.k).join(',')}</div>
      <button onClick={() => nav.push({ k: 'bot', id: 1 })}>push-bot</button>
      <button onClick={() => nav.push({ k: 'log', id: 9 })}>push-log</button>
      <button onClick={() => nav.pop()}>pop</button>
      <button onClick={() => nav.switchTab('chats')}>tab-chats</button>
      <button onClick={() => nav.switchTab('records', 'appeals')}>tab-records-appeals</button>
    </div>
  )
}

function QueryBox({ param }: { param: string }) {
  const [value, setValue] = usePageParam(param)
  return (
    <input
      aria-label={param}
      value={value}
      onChange={(event) => setValue(event.target.value)}
    />
  )
}

function Page({ slug, active, children }: { slug: string; active: boolean; children: ReactNode }) {
  return (
    <PageScope scope={slug} active={active}>
      {children}
    </PageScope>
  )
}

function current(): string {
  return location.pathname + location.search
}

describe('URL 路由', () => {
  it('push 二级页写进路径；重新挂载（刷新）后栈还原', () => {
    const first = render(
      <NavProvider>
        <NavProbe />
      </NavProvider>,
    )
    fireEvent.click(screen.getByText('push-bot'))
    expect(current()).toBe('/miniapp/bots/1')
    first.unmount()

    render(
      <NavProvider>
        <NavProbe />
      </NavProvider>,
    )
    expect(screen.getByTestId('stack').textContent).toBe('bot')
  })

  it('switchTab 写路径并带上 intent；push 后 intent 清掉', () => {
    render(
      <NavProvider>
        <NavProbe />
      </NavProvider>,
    )
    fireEvent.click(screen.getByText('tab-records-appeals'))
    expect(current()).toBe('/miniapp/records?intent=appeals')

    fireEvent.click(screen.getByText('push-log'))
    expect(current()).toBe('/miniapp/logs/9')
  })

  it('pop 回到上一处并更新 URL', () => {
    render(
      <NavProvider>
        <NavProbe />
      </NavProvider>,
    )
    fireEvent.click(screen.getByText('push-bot'))
    fireEvent.click(screen.getByText('push-log'))
    expect(current()).toBe('/miniapp/logs/9')
    fireEvent.click(screen.getByText('pop'))
    expect(current()).toBe('/miniapp/bots/1')
    fireEvent.click(screen.getByText('pop'))
    expect(current()).toBe('/miniapp/')
  })

  it('浏览器后退（popstate）把导航状态恢复到 URL 所指处', () => {
    render(
      <NavProvider>
        <NavProbe />
      </NavProvider>,
    )
    fireEvent.click(screen.getByText('tab-chats'))
    fireEvent.click(screen.getByText('push-log'))
    expect(screen.getByTestId('tab').textContent).toBe('chats')

    act(() => {
      window.history.replaceState(null, '', '/miniapp/records')
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    expect(screen.getByTestId('tab').textContent).toBe('records')
    expect(screen.getByTestId('stack').textContent).toBe('')
  })

  it('页面参数初值来自 URL，改动写回查询串', async () => {
    window.history.replaceState(null, '', '/miniapp/bots?q=alice')
    render(
      <NavProvider>
        <Page slug="bots" active>
          <QueryBox param="q" />
        </Page>
      </NavProvider>,
    )
    const input = screen.getByLabelText('q')
    await waitFor(() => expect(input).toHaveValue('alice'))

    fireEvent.change(input, { target: { value: 'bob' } })
    await waitFor(() => expect(location.search).toBe('?q=bob'))
  })

  it('进入二级页时列表的筛选参数从 URL 上让位', async () => {
    window.history.replaceState(null, '', '/miniapp/bots?q=alice')
    render(
      <NavProvider>
        <Page slug="bots" active>
          <QueryBox param="q" />
        </Page>
        <NavProbe />
      </NavProvider>,
    )
    await waitFor(() => expect(location.search).toBe('?q=alice'))

    fireEvent.click(screen.getByText('push-bot'))
    expect(current()).toBe('/miniapp/bots/1')
  })

  it('页面还没挂载（壳先渲染骨架）时不抹掉 URL 上的参数', () => {
    // 刷新时 NavProvider 先于页面挂载：此刻没有任何参数被登记，地址栏必须原样保留，
    // 否则页面还没读到就被清掉，刷新就丢进度。
    window.history.replaceState(null, '', '/miniapp/records?seg=appeals')
    render(
      <NavProvider>
        <span>骨架</span>
      </NavProvider>,
    )
    expect(current()).toBe('/miniapp/records?seg=appeals')
  })

  it('非法筛选值退回默认，不落到页面状态里', async () => {
    window.history.replaceState(null, '', '/miniapp/bots?filter=bogus&q=kept')
    render(
      <NavProvider>
        <Page slug="bots" active>
          <FilterBox />
        </Page>
      </NavProvider>,
    )
    await waitFor(() => expect(screen.getByLabelText('filter')).toHaveValue('all'))
    // 非法值被丢弃后，URL 上只剩合法项。
    await waitFor(() => expect(location.search).toBe('?q=kept'))
  })
})

function FilterBox() {
  const [filter, setFilter] = usePageParam('filter', 'all', ['all', 'deleted'])
  return (
    <>
      <input aria-label="filter" value={filter} readOnly />
      <QueryBox param="q" />
      <button onClick={() => setFilter('deleted')}>to-deleted</button>
    </>
  )
}
