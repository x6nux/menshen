// 通用列表行：左主文案 16px + 可选灰色副行 13px（可等宽），
// 右侧徽标/值/自定义 trailing 与可选 › 箭头；有 onClick 时整行可点，
// 行高 ≥52 由 theme 的 MuiListItemButton overrides 保证，按下态用 action.hover。
import KeyboardArrowRight from '@mui/icons-material/KeyboardArrowRight'
import { Box, ListItem, ListItemButton, ListItemText } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'

export interface ListRowProps {
  primary: ReactNode
  secondary?: ReactNode
  /** 副行用等宽字体（chat_id、模型名这类标识）。 */
  secondaryMono?: boolean
  /** 右侧徽标（Badge 等）。 */
  badge?: ReactNode
  /** 右侧值文案。 */
  value?: ReactNode
  /** 右侧自定义内容（开关等），排在 value/badge 之后。 */
  trailing?: ReactNode
  chevron?: boolean
  onClick?: () => void
  disabled?: boolean
  sx?: SxProps<Theme>
}

export function ListRow({
  primary,
  secondary,
  secondaryMono = false,
  badge,
  value,
  trailing,
  chevron = false,
  onClick,
  disabled = false,
  sx,
}: ListRowProps) {
  const content = (
    <>
      <ListItemText
        primary={primary}
        secondary={secondary}
        slotProps={{
          primary: { sx: { fontSize: 16, lineHeight: 1.4 } },
          secondary: {
            sx: {
              fontSize: 13,
              lineHeight: 1.4,
              wordBreak: 'break-all',
              fontFamily: secondaryMono ? 'ui-monospace, Menlo, monospace' : undefined,
            },
          },
        }}
        sx={{ my: 0, minWidth: 0 }}
      />
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, pl: 1, flexShrink: 0 }}>
        {badge}
        {value !== undefined && <Box sx={{ fontSize: 14, color: 'text.secondary' }}>{value}</Box>}
        {trailing}
        {chevron && <KeyboardArrowRight sx={{ fontSize: 20, color: 'text.disabled', ml: -0.5 }} />}
      </Box>
    </>
  )

  const rowSx: SxProps<Theme> = [
    { px: 2, py: 0.5, '&:active': { bgcolor: 'action.hover' } },
    ...(Array.isArray(sx) ? sx : [sx]),
  ]

  if (!onClick) {
    return <ListItem sx={rowSx}>{content}</ListItem>
  }
  return (
    <ListItemButton
      onClick={() => {
        // ButtonBase 对非原生 button 的 disabled 只加 aria-disabled 与
        // pointer-events:none；这里再守一道，确保任何点击路径都不触发。
        if (!disabled) onClick()
      }}
      disabled={disabled}
      sx={rowSx}
    >
      {content}
    </ListItemButton>
  )
}
