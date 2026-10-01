import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { SwitchRow } from './SwitchRow'

describe('SwitchRow', () => {
  it('点行触发一次 onChange(!checked)', () => {
    const onChange = vi.fn()
    render(<SwitchRow primary="启用" secondary="关闭后不再判定" checked={false} onChange={onChange} />)
    fireEvent.click(screen.getByText('启用'))
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('点开关只触发一次（不冒泡到行导致连翻两次）', () => {
    const onChange = vi.fn()
    render(<SwitchRow primary="启用" checked onChange={onChange} />)
    fireEvent.click(screen.getByRole('switch'))
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith(false)
  })

  it('disabled 时行与开关都不触发', () => {
    const onChange = vi.fn()
    render(<SwitchRow primary="启用" checked={false} onChange={onChange} disabled />)
    fireEvent.click(screen.getByText('启用'))
    fireEvent.click(screen.getByRole('switch'))
    expect(onChange).not.toHaveBeenCalled()
  })
})
