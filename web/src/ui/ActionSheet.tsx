// 底部操作面板（计划 3.3）：不可逆操作用底部 Drawer 确认。
// confirm() 返回 Promise<boolean>：点确认 → true；点取消/遮罩/Esc → false。
// danger 时确认行用错误色，否则用主色；取消行始终灰色。
import { Box, Button, Drawer, Typography } from '@mui/material'
import { createContext, useCallback, useContext, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

export interface ConfirmOptions {
  /** 动作标题，如「移除该群？」。 */
  title: ReactNode
  /** 对象与后果说明，沿用旧版文案。 */
  description?: ReactNode
  confirmText?: string
  cancelText?: string
  /** 不可逆操作用红色确认行。 */
  danger?: boolean
}

export type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>

const ConfirmContext = createContext<ConfirmFn | null>(null)

export interface ActionSheetProviderProps {
  children: ReactNode
}

export function ActionSheetProvider({ children }: ActionSheetProviderProps) {
  const [sheet, setSheet] = useState<ConfirmOptions | null>(null)
  const resolver = useRef<((ok: boolean) => void) | null>(null)

  const confirm = useCallback<ConfirmFn>((options) => {
    // 极端情况（连点）下上一个确认还没结算：按取消处理，避免 Promise 悬空。
    resolver.current?.(false)
    return new Promise<boolean>((resolve) => {
      resolver.current = resolve
      setSheet(options)
    })
  }, [])

  const settle = useCallback((ok: boolean) => {
    const resolve = resolver.current
    resolver.current = null
    setSheet(null)
    resolve?.(ok)
  }, [])

  const value = useMemo(() => confirm, [confirm])

  return (
    <ConfirmContext.Provider value={value}>
      {children}
      <Drawer
        anchor="bottom"
        open={sheet !== null}
        onClose={() => settle(false)}
        slotProps={{
          paper: {
            sx: {
              borderTopLeftRadius: '16px',
              borderTopRightRadius: '16px',
              pb: 'env(safe-area-inset-bottom)',
            },
          },
        }}
      >
        <Box sx={{ px: 2, pt: 2, pb: 0.5, textAlign: 'center' }}>
          <Typography sx={{ fontSize: 16, fontWeight: 600 }}>{sheet?.title}</Typography>
          {sheet?.description !== undefined && (
            <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
              {sheet.description}
            </Typography>
          )}
        </Box>
        <Box sx={{ px: 2, pt: 1.5, pb: 1.5 }}>
          <Button
            fullWidth
            color={sheet?.danger ? 'error' : 'primary'}
            onClick={() => settle(true)}
            sx={{ fontWeight: 600, fontSize: 16 }}
          >
            {sheet?.confirmText ?? '确定'}
          </Button>
          <Button
            fullWidth
            color="inherit"
            onClick={() => settle(false)}
            sx={{ mt: 0.5, color: 'text.secondary', fontSize: 16 }}
          >
            {sheet?.cancelText ?? '取消'}
          </Button>
        </Box>
      </Drawer>
    </ConfirmContext.Provider>
  )
}

/** useConfirm 拿到命令式 confirm()；必须在 ActionSheetProvider 内使用。 */
// oxlint-disable-next-line react/only-export-components -- Provider 与 hook 必须共享同一个 context，拆分反而绕。
export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext)
  if (!ctx) throw new Error('useConfirm 必须在 ActionSheetProvider 内使用')
  return ctx
}
