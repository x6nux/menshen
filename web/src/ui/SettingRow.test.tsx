import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { SettingRow } from './SettingRow'

describe('SettingRow', () => {
  it('渲染 label/hint/value 与 ›，点击整行触发 onClick', () => {
    const onClick = vi.fn()
    const { container } = render(
      <SettingRow label="上游渠道" hint="仅主管理员可见" value="2" onClick={onClick} />,
    )
    expect(screen.getByText('上游渠道')).toBeInTheDocument()
    expect(screen.getByText('仅主管理员可见')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
    // › 箭头（MUI 图标渲染成 svg）
    expect(container.querySelector('svg')).not.toBeNull()

    fireEvent.click(screen.getByText('上游渠道'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('disabled 时不触发 onClick', () => {
    const onClick = vi.fn()
    render(<SettingRow label="模型定价" onClick={onClick} disabled />)
    fireEvent.click(screen.getByText('模型定价'))
    expect(onClick).not.toHaveBeenCalled()
  })
})
