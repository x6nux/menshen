import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { Segmented } from './Segmented'

const OPTIONS = [
  { value: 'open', label: '未结' },
  { value: 'all', label: '全部' },
]

function Wrapper() {
  const [value, setValue] = useState('open')
  return (
    <>
      <Segmented value={value} onChange={setValue} options={OPTIONS} ariaLabel="申诉筛选" />
      <div data-testid="value">{value}</div>
    </>
  )
}

describe('Segmented', () => {
  it('点击切换选中值，再点当前项不会清空选择', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    const all = screen.getByRole('button', { name: '全部' })

    await user.click(all)
    expect(screen.getByTestId('value')).toHaveTextContent('all')
    expect(all).toHaveAttribute('aria-pressed', 'true')

    await user.click(all)
    expect(screen.getByTestId('value')).toHaveTextContent('all')
    expect(all).toHaveAttribute('aria-pressed', 'true')
  })
})
