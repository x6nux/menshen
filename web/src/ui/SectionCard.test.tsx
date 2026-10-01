import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SectionCard } from './SectionCard'

describe('SectionCard', () => {
  it('渲染组标题、内容与 footer', () => {
    render(
      <SectionCard title="近 24 小时" footer={<button>添加</button>}>
        <div>指标内容</div>
      </SectionCard>,
    )
    expect(screen.getByText('近 24 小时')).toBeInTheDocument()
    expect(screen.getByText('指标内容')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '添加' })).toBeInTheDocument()
  })

  it('不传 title 时不渲染标题节点', () => {
    const { container } = render(<SectionCard>只有内容</SectionCard>)
    expect(screen.getByText('只有内容')).toBeInTheDocument()
    expect(container.querySelector('h2')).toBeNull()
  })
})
