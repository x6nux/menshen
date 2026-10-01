import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { NavProvider, useNav } from '../nav'
import { TabBar } from './TabBar'

function Probe() {
  const nav = useNav()
  return (
    <>
      <div data-testid="tab">{nav.tab}</div>
      <button onClick={() => nav.push({ k: 'bot', id: 1 })}>push</button>
    </>
  )
}

function setup() {
  return render(
    <NavProvider>
      <TabBar />
      <Probe />
    </NavProvider>,
  )
}

describe('TabBar', () => {
  it('渲染五个 Tab 并切换选中项', () => {
    setup()
    for (const label of ['概览', '机器人', '群组', '记录', '我的']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument()
    }
    expect(screen.getByTestId('tab')).toHaveTextContent('overview')

    fireEvent.click(screen.getByRole('button', { name: '群组' }))
    expect(screen.getByTestId('tab')).toHaveTextContent('chats')
  })

  it('有页面栈（详情页）时整条隐藏', () => {
    setup()
    expect(screen.getByRole('button', { name: '概览' })).toBeInTheDocument()

    fireEvent.click(screen.getByText('push'))
    expect(screen.queryByRole('button', { name: '概览' })).not.toBeInTheDocument()
  })
})
