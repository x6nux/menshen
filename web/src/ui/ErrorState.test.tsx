import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ErrorState } from './ErrorState'

describe('ErrorState', () => {
  it('401 显示「请通过 Telegram 菜单按钮打开」引导', () => {
    render(<ErrorState status={401} />)
    expect(screen.getByText('请通过 Telegram 菜单按钮打开')).toBeInTheDocument()
  })

  it('403 显示无权限文案', () => {
    render(<ErrorState status={403} />)
    expect(screen.getByText('没有权限')).toBeInTheDocument()
  })

  it('网络错误显示网络文案，重试触发回调', () => {
    const onRetry = vi.fn()
    render(<ErrorState status={0} onRetry={onRetry} />)
    expect(screen.getByText('网络异常')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('不传 onRetry 时没有重试按钮', () => {
    render(<ErrorState status={500} />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
