import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Badge } from './Badge'
import { ListRow } from './ListRow'

describe('ListRow', () => {
  it('渲染主副文案、徽标与右侧值，点击整行触发 onClick', () => {
    const onClick = vi.fn()
    render(
      <ListRow
        primary="测试群"
        secondary="-100123456"
        secondaryMono
        badge={<Badge tone="ok">运行中</Badge>}
        value="2 天"
        chevron
        onClick={onClick}
      />,
    )
    expect(screen.getByText('测试群')).toBeInTheDocument()
    expect(screen.getByText('-100123456')).toBeInTheDocument()
    expect(screen.getByText('运行中')).toBeInTheDocument()
    expect(screen.getByText('2 天')).toBeInTheDocument()

    fireEvent.click(screen.getByText('测试群'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('没有 onClick 时整行不可点', () => {
    render(<ListRow primary="只读行" />)
    expect(screen.getByText('只读行')).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('渲染自定义 trailing（如开关）', () => {
    render(<ListRow primary="启用" trailing={<span data-testid="trailing">SW</span>} />)
    expect(screen.getByTestId('trailing')).toBeInTheDocument()
  })
})
