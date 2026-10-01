import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ToastProvider, useToast } from './Toast'

function Probe() {
  const show = useToast()
  return (
    <>
      <button onClick={() => show('已保存')}>first</button>
      <button onClick={() => show('保存失败')}>second</button>
    </>
  )
}

function setup() {
  return render(
    <ToastProvider>
      <Probe />
    </ToastProvider>,
  )
}

describe('Toast', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('show 后可见，约 2.2s 自动消失', () => {
    vi.useFakeTimers()
    setup()
    fireEvent.click(screen.getByText('first'))
    expect(screen.getByTestId('toast')).toHaveTextContent('已保存')

    act(() => {
      vi.advanceTimersByTime(2300)
    })
    expect(screen.getByTestId('toast')).toBeEmptyDOMElement()
  })

  it('再次 show 替换文案并重置计时', () => {
    vi.useFakeTimers()
    setup()
    fireEvent.click(screen.getByText('first'))
    act(() => {
      vi.advanceTimersByTime(1500)
    })
    fireEvent.click(screen.getByText('second'))
    // 第二条立即可见，第一条不再显示
    expect(screen.getByTestId('toast')).toHaveTextContent('保存失败')
    expect(screen.getByTestId('toast')).not.toHaveTextContent('已保存')

    act(() => {
      vi.advanceTimersByTime(1500)
    })
    // 第二条 show 只过了 1.5s，还没到 2.2s，仍在显示
    expect(screen.getByTestId('toast')).toHaveTextContent('保存失败')

    act(() => {
      vi.advanceTimersByTime(800)
    })
    expect(screen.getByTestId('toast')).toBeEmptyDOMElement()
  })
})
