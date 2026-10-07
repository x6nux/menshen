// 全局轻提示：单例 Toast，屏幕中下、黑 75% 圆角 8、约 2.2s 自动消失。
// 连续 show 会替换文案并重置计时；组件始终挂载一个固定定位节点，用 opacity 过渡。
import { Box } from '@mui/material'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

interface ToastContextValue {
  show(message: string): void
}

const ToastContext = createContext<ToastContextValue | null>(null)

const TOAST_DURATION_MS = 2200

export interface ToastProviderProps {
  children: ReactNode
}

export function ToastProvider({ children }: ToastProviderProps) {
  const [message, setMessage] = useState<string | null>(null)
  const timer = useRef<number | null>(null)

  const show = useCallback((next: string) => {
    setMessage(next)
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      timer.current = null
      setMessage(null)
    }, TOAST_DURATION_MS)
  }, [])

  // 卸载时清掉计时器，避免对已卸载组件 setState。
  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current)
    },
    [],
  )

  const value = useMemo(() => ({ show }), [show])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <Box
        data-testid="toast"
        role="status"
        aria-live="polite"
        sx={{
          position: 'fixed',
          left: '50%',
          bottom: '15%',
          transform: 'translateX(-50%)',
          maxWidth: '80vw',
          px: 1.75,
          py: 1,
          borderRadius: '8px',
          bgcolor: 'rgba(0,0,0,.75)',
          color: '#fff',
          fontSize: 13,
          lineHeight: 1.5,
          textAlign: 'center',
          pointerEvents: 'none',
          opacity: message === null ? 0 : 1,
          transition: 'opacity .2s',
          zIndex: (theme) => theme.zIndex.snackbar,
        }}
      >
        {message}
      </Box>
    </ToastContext.Provider>
  )
}

/** useToast 返回全局 show(message)；必须在 ToastProvider 内使用。 */
// oxlint-disable-next-line react/only-export-components -- Provider 与 hook 共享同一 context，故同文件导出。
export function useToast(): (message: string) => void {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast 必须在 ToastProvider 内使用')
  return ctx.show
}
