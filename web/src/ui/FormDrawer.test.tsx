import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { FormDrawer } from './FormDrawer'

describe('FormDrawer', () => {
  it('提交触发 onSubmit，内容照常渲染', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn()
    render(
      <FormDrawer open onClose={onClose} title="添加群" onSubmit={onSubmit}>
        <input aria-label="chat_id" />
      </FormDrawer>,
    )
    expect(screen.getByText('添加群')).toBeInTheDocument()
    expect(screen.getByLabelText('chat_id')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '提交' }))
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('pending 时禁用提交与关闭，点遮罩不关闭；结束后可关闭', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn()
    const view = render(
      <FormDrawer open onClose={onClose} title="添加群" onSubmit={onSubmit}>
        <input aria-label="chat_id" />
      </FormDrawer>,
    )

    view.rerender(
      <FormDrawer open onClose={onClose} title="添加群" onSubmit={onSubmit} pending>
        <input aria-label="chat_id" />
      </FormDrawer>,
    )
    expect(screen.getByRole('button', { name: /提交/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: '关闭' })).toBeDisabled()

    fireEvent.click(document.querySelector('.MuiBackdrop-root') as HTMLElement)
    expect(onClose).not.toHaveBeenCalled()

    view.rerender(
      <FormDrawer open onClose={onClose} title="添加群" onSubmit={onSubmit}>
        <input aria-label="chat_id" />
      </FormDrawer>,
    )
    fireEvent.click(document.querySelector('.MuiBackdrop-root') as HTMLElement)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('submitDisabled 可单独禁用提交（内容未填）', () => {
    render(
      <FormDrawer open onClose={() => {}} title="添加群" onSubmit={() => {}} submitDisabled>
        <input aria-label="chat_id" />
      </FormDrawer>,
    )
    expect(screen.getByRole('button', { name: '提交' })).toBeDisabled()
  })
})
