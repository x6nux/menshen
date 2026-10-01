import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Skeletons } from './Skeletons'

describe('Skeletons', () => {
  it('默认渲染 3 行骨架', () => {
    render(<Skeletons />)
    expect(screen.getAllByTestId('skeleton-row')).toHaveLength(3)
  })

  it('rows 可自定义', () => {
    render(<Skeletons rows={5} />)
    expect(screen.getAllByTestId('skeleton-row')).toHaveLength(5)
  })
})
