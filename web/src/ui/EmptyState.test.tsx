import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { EmptyState } from './EmptyState'

describe('EmptyState', () => {
  it('渲染图标、文案与可选按钮', () => {
    const onClick = vi.fn()
    render(
      <EmptyState
        title="还没有机器人"
        description="在 Telegram 私聊主 bot 发送 /start 接入"
        action={<button onClick={onClick}>去接入</button>}
      />,
    )
    expect(screen.getByText('还没有机器人')).toBeInTheDocument()
    expect(screen.getByText('在 Telegram 私聊主 bot 发送 /start 接入')).toBeInTheDocument()
    expect(screen.getByTestId('empty-state').querySelector('svg')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '去接入' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('不传 action 时没有按钮', () => {
    render(<EmptyState title="没有记录" />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
