import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Badge } from './Badge'

describe('Badge', () => {
  it('渲染文案并带语义色调', () => {
    render(<Badge tone="ok">运行中</Badge>)
    expect(screen.getByText('运行中')).toHaveAttribute('data-tone', 'ok')
  })

  it('支持 no/warn/neutral 三种标记', () => {
    render(
      <>
        <Badge tone="no">未运行</Badge>
        <Badge tone="warn">演练</Badge>
        <Badge>主 bot</Badge>
      </>,
    )
    expect(screen.getByText('未运行')).toHaveAttribute('data-tone', 'no')
    expect(screen.getByText('演练')).toHaveAttribute('data-tone', 'warn')
    expect(screen.getByText('主 bot')).toHaveAttribute('data-tone', 'neutral')
  })
})
