import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { SearchField } from './SearchField'

function Wrapper() {
  const [value, setValue] = useState('')
  return <SearchField value={value} onChange={setValue} placeholder="搜索群组" />
}

describe('SearchField', () => {
  it('受控输入，可一键清空', async () => {
    const user = userEvent.setup()
    render(<Wrapper />)
    const input = screen.getByPlaceholderText('搜索群组')

    await user.type(input, 'abc')
    expect(input).toHaveValue('abc')

    await user.click(screen.getByRole('button', { name: '清空' }))
    expect(input).toHaveValue('')
    // 清空后按钮消失
    expect(screen.queryByRole('button', { name: '清空' })).not.toBeInTheDocument()
  })

  it('clearable=false 时不显示清空按钮', () => {
    render(<SearchField value="abc" onChange={() => {}} clearable={false} />)
    expect(screen.queryByRole('button', { name: '清空' })).not.toBeInTheDocument()
  })

  it('aria-label 默认取 placeholder，可被覆盖', () => {
    const first = render(<SearchField value="" onChange={() => {}} placeholder="搜索群组" />)
    expect(screen.getByRole('textbox', { name: '搜索群组' })).toBeInTheDocument()
    first.unmount()

    render(
      <SearchField value="" onChange={() => {}} placeholder="搜索群组" ariaLabel="按群号搜索" />,
    )
    expect(screen.getByRole('textbox', { name: '按群号搜索' })).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: '搜索群组' })).not.toBeInTheDocument()
  })
})
