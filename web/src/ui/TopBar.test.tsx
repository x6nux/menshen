import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { NavProvider, useNav } from '../nav'
import { TopBar } from './TopBar'

function PushProbe() {
  const nav = useNav()
  return <button onClick={() => nav.push({ k: 'bot', id: 1 })}>push</button>
}

describe('TopBar', () => {
  it('无栈时不显示返回；有栈时自动显示返回并 pop', () => {
    render(
      <NavProvider>
        <TopBar title="门神" subtitle="主管理员 · uid 100" />
        <PushProbe />
      </NavProvider>,
    )
    expect(screen.getByText('门神')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '返回' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('push'))
    const back = screen.getByRole('button', { name: '返回' })
    fireEvent.click(back)
    // pop 之后栈空，返回按钮消失
    expect(screen.queryByRole('button', { name: '返回' })).not.toBeInTheDocument()
  })

  it('渲染副标题与右侧动作', () => {
    const onAction = vi.fn()
    render(
      <NavProvider>
        <TopBar title="机器人" subtitle="次级管理员 · uid 7" actions={<button onClick={onAction}>管理</button>} />
      </NavProvider>,
    )
    expect(screen.getByText('机器人')).toBeInTheDocument()
    expect(screen.getByText('次级管理员 · uid 7')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '管理' }))
    expect(onAction).toHaveBeenCalledTimes(1)
  })

  it('onBack 可覆盖默认 pop，且栈空时也显示返回', () => {
    const onBack = vi.fn()
    render(
      <NavProvider>
        <TopBar title="门神" onBack={onBack} />
      </NavProvider>,
    )
    fireEvent.click(screen.getByRole('button', { name: '返回' }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })
})
