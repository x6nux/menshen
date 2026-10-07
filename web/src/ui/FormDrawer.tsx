// 底部表单抽屉：标题 + children + 全宽提交按钮。
// pending 由调用方从 mutation.isPending 传入：提交中禁用关闭（关闭按钮/遮罩/Esc）
// 与提交按钮；错误时不自动关闭——失败由调用方 toast，抽屉保持打开、输入不丢。
import Close from '@mui/icons-material/Close'
import { Box, Button, Drawer, IconButton, Typography } from '@mui/material'
import type { ReactNode } from 'react'

export interface FormDrawerProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  children: ReactNode
  /** 点提交按钮触发；pending 时不会触发。 */
  onSubmit?: () => void
  submitText?: ReactNode
  /** 提交中：禁用一切关闭途径与提交按钮。 */
  pending?: boolean
  /** 额外的提交禁用条件（如内容未填），与 pending 独立。 */
  submitDisabled?: boolean
}

export function FormDrawer({
  open,
  onClose,
  title,
  children,
  onSubmit,
  submitText = '提交',
  pending = false,
  submitDisabled = false,
}: FormDrawerProps) {
  return (
    <Drawer
      anchor="bottom"
      open={open}
      onClose={pending ? undefined : onClose}
      disableEscapeKeyDown={pending}
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
      <Box sx={{ px: 2, pt: 2, pb: 2 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', mb: 2 }}>
          <Typography sx={{ flex: 1, fontSize: 17, fontWeight: 600 }}>{title}</Typography>
          <IconButton aria-label="关闭" size="small" disabled={pending} onClick={onClose}>
            <Close />
          </IconButton>
        </Box>
        {children}
        {onSubmit !== undefined && (
          <Button
            fullWidth
            variant="contained"
            loading={pending}
            disabled={pending || submitDisabled}
            onClick={onSubmit}
            sx={{ mt: 2 }}
          >
            {submitText}
          </Button>
        )}
      </Box>
    </Drawer>
  )
}
