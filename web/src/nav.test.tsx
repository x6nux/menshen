import { act, fireEvent, render, screen } from '@testing-library/react'
import { StrictMode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { NavProvider, useNav } from './nav'
import type { TelegramBackButton } from './telegram'

function mockBackButton() {
  const handlers: (() => void)[] = []
  const show = vi.fn()
  const hide = vi.fn()
  const onClick = vi.fn<(cb: () => void) => () => void>((cb) => {
    handlers.push(cb)
    return () => {
      const i = handlers.indexOf(cb)
      if (i >= 0) handlers.splice(i, 1)
    }
  })
  const button: TelegramBackButton = { show, hide, onClick }
  return { button, handlers, show, hide, onClick }
}

function Probe() {
  const nav = useNav()
  return (
    <>
      <div data-testid="tab">{nav.tab}</div>
      <div data-testid="intent">{nav.intent ?? ''}</div>
      <div data-testid="stack">{nav.stack.map((p) => p.k).join(',')}</div>
      <button onClick={() => nav.push({ k: 'bot', id: 1 })}>push-bot</button>
      <button onClick={() => nav.push({ k: 'log', id: 9 })}>push-log</button>
      <button onClick={() => nav.pop()}>pop</button>
      <button onClick={() => nav.switchTab('chats')}>tab-chats</button>
      <button onClick={() => nav.switchTab('chats', 'bot:1')}>tab-chats-bot</button>
      <button onClick={() => nav.switchTab('records', 'appeals')}>tab-records-appeals</button>
    </>
  )
}

function renderNav() {
  const back = mockBackButton()
  const view = render(
    <NavProvider backButton={back.button}>
      <Probe />
    </NavProvider>,
  )
  return { ...back, unmount: view.unmount }
}

describe('NavProvider', () => {
  it('push/pop 维护栈，并与 BackButton 显隐同步', () => {
    const back = renderNav()
    // 顶层：隐藏返回按钮
    expect(back.hide).toHaveBeenCalledTimes(1)
    expect(back.show).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('push-bot'))
    expect(screen.getByTestId('stack').textContent).toBe('bot')
    expect(back.show).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByText('push-log'))
    expect(screen.getByTestId('stack').textContent).toBe('bot,log')

    // Telegram 返回按钮点击 = pop
    act(() => {
      back.handlers.forEach((h) => h())
    })
    expect(screen.getByTestId('stack').textContent).toBe('bot')

    fireEvent.click(screen.getByText('pop'))
    expect(screen.getByTestId('stack').textContent).toBe('')
    expect(back.hide).toHaveBeenCalledTimes(2)
  })

  it('顶层 pop 不越界，切 Tab 清空页面栈', () => {
    const back = renderNav()
    fireEvent.click(screen.getByText('pop'))
    expect(screen.getByTestId('stack').textContent).toBe('')

    fireEvent.click(screen.getByText('push-bot'))
    fireEvent.click(screen.getByText('tab-chats'))
    expect(screen.getByTestId('tab').textContent).toBe('chats')
    expect(screen.getByTestId('stack').textContent).toBe('')
    expect(back.hide).toHaveBeenCalledTimes(2)
    expect(back.show).toHaveBeenCalledTimes(1)
  })

  it('同页重复 push 不叠栈，不同页照常入栈', () => {
    renderNav()
    fireEvent.click(screen.getByText('push-bot'))
    fireEvent.click(screen.getByText('push-bot'))
    fireEvent.click(screen.getByText('push-bot'))
    expect(screen.getByTestId('stack').textContent).toBe('bot')

    fireEvent.click(screen.getByText('push-log'))
    expect(screen.getByTestId('stack').textContent).toBe('bot,log')
    // 栈顶不同，即便反复按同一页也只在每次切换后入栈一次
    fireEvent.click(screen.getByText('push-log'))
    expect(screen.getByTestId('stack').textContent).toBe('bot,log')
  })

  it('StrictMode 下订阅不重复，显隐与栈状态一致', () => {
    const back = mockBackButton()
    render(
      <StrictMode>
        <NavProvider backButton={back.button}>
          <Probe />
        </NavProvider>
      </StrictMode>,
    )
    // StrictMode 会双跑 effect，但订阅（以及退订）配平，只剩一份
    expect(back.handlers).toHaveLength(1)
    expect(back.hide).toHaveBeenCalled()
    expect(back.show).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('push-bot'))
    expect(screen.getByTestId('stack').textContent).toBe('bot')
    expect(back.show).toHaveBeenCalled()

    // 通过 BackButton 回退也只弹一层
    act(() => {
      back.handlers.forEach((h) => h())
    })
    expect(screen.getByTestId('stack').textContent).toBe('')
    expect(back.hide).toHaveBeenCalled()
  })

  it('卸载时取消 BackButton 订阅', () => {
    const back = renderNav()
    expect(back.handlers).toHaveLength(1)
    back.unmount()
    expect(back.handlers).toHaveLength(0)
  })

  it('switchTab 带 intent：可读、再次切 tab 清空、push 也清空', () => {
    renderNav()
    expect(screen.getByTestId('intent').textContent).toBe('')

    fireEvent.click(screen.getByText('tab-chats-bot'))
    expect(screen.getByTestId('tab').textContent).toBe('chats')
    expect(screen.getByTestId('intent').textContent).toBe('bot:1')

    // push 进入二级页时清空意图（返回列表不再残留过滤）
    fireEvent.click(screen.getByText('push-bot'))
    expect(screen.getByTestId('intent').textContent).toBe('')

    // 再次带意图切换，随后不带意图切 tab 也要清空
    fireEvent.click(screen.getByText('tab-records-appeals'))
    expect(screen.getByTestId('intent').textContent).toBe('appeals')
    fireEvent.click(screen.getByText('tab-chats'))
    expect(screen.getByTestId('intent').textContent).toBe('')
  })
})
