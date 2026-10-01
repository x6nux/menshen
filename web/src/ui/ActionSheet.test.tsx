import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { ActionSheetProvider, useConfirm } from './ActionSheet'

function Probe() {
  const confirm = useConfirm()
  const [result, setResult] = useState('pending')
  return (
    <>
      <button
        onClick={() => {
          void confirm({
            title: '移除该群？',
            description: '移除后该群不再受监控，历史记录保留。',
            confirmText: '移除',
            danger: true,
          }).then((ok) => setResult(ok ? 'yes' : 'no'))
        }}
      >
        ask
      </button>
      <div data-testid="result">{result}</div>
    </>
  )
}

function setup() {
  return render(
    <ActionSheetProvider>
      <Probe />
    </ActionSheetProvider>,
  )
}

describe('ActionSheet / useConfirm', () => {
  it('点确认解析 true，点取消解析 false', async () => {
    setup()
    fireEvent.click(screen.getByText('ask'))
    expect(screen.getByText('移除该群？')).toBeInTheDocument()
    expect(screen.getByText('移除后该群不再受监控，历史记录保留。')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    await waitFor(() => expect(screen.getByTestId('result')).toHaveTextContent('yes'))

    fireEvent.click(screen.getByText('ask'))
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.getByTestId('result')).toHaveTextContent('no'))
  })

  it('点遮罩等于取消', async () => {
    setup()
    fireEvent.click(screen.getByText('ask'))
    const backdrop = document.querySelector('.MuiBackdrop-root')
    expect(backdrop).not.toBeNull()
    fireEvent.click(backdrop as HTMLElement)
    await waitFor(() => expect(screen.getByTestId('result')).toHaveTextContent('no'))
  })

  it('danger 时确认行用错误色', () => {
    setup()
    fireEvent.click(screen.getByText('ask'))
    expect(screen.getByRole('button', { name: '移除' })).toHaveClass('MuiButton-colorError')
  })
})
