import { act, fireEvent, render, screen } from '@testing-library/react'
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
      <div data-testid="stack">{nav.stack.map((p) => p.k).join(',')}</div>
      <button onClick={() => nav.push({ k: 'bot', id: 1 })}>push-bot</button>
      <button onClick={() => nav.push({ k: 'log', id: 9 })}>push-log</button>
      <button onClick={() => nav.pop()}>pop</button>
      <button onClick={() => nav.switchTab('chats')}>tab-chats</button>
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

  it('卸载时取消 BackButton 订阅', () => {
    const back = renderNav()
    expect(back.handlers).toHaveLength(1)
    back.unmount()
    expect(back.handlers).toHaveLength(0)
  })
})
