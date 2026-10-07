// ErrorBoundary：渲染异常时给出原因与刷新入口。
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ErrorBoundary } from './ErrorBoundary'

function Boom(): never {
  throw new Error('boom-render')
}

describe('ErrorBoundary', () => {
  it('正常子元素直接渲染', () => {
    render(
      <ErrorBoundary>
        <div>内容正常</div>
      </ErrorBoundary>,
    )
    expect(screen.getByText('内容正常')).toBeInTheDocument()
  })

  it('渲染异常时显示错误与刷新按钮', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )
    expect(screen.getByTestId('error-boundary')).toBeInTheDocument()
    expect(screen.getByText('页面出错了')).toBeInTheDocument()
    expect(screen.getByText(/boom-render/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '刷新重试' })).toBeInTheDocument()
    spy.mockRestore()
  })
})
