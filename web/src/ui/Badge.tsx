// 语义徽标（计划 3.1/3.4）：ok/no/warn 三色，底色沿用旧页 .badge.ok/.badge.no 的
// 半透明色块；neutral 对应旧页无修饰的 .badge，用于「主 bot」这类中性标记。
import { Box, alpha } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'

export type BadgeTone = 'ok' | 'no' | 'warn' | 'neutral'

export interface BadgeProps {
  tone?: BadgeTone
  children: ReactNode
  sx?: SxProps<Theme>
}

function toneSx(tone: BadgeTone): SxProps<Theme> {
  switch (tone) {
    case 'ok':
      return { color: 'success.main', bgcolor: (theme) => alpha(theme.palette.success.main, 0.16) }
    case 'no':
      return { color: 'error.main', bgcolor: (theme) => alpha(theme.palette.error.main, 0.16) }
    case 'warn':
      return { color: 'warning.main', bgcolor: (theme) => alpha(theme.palette.warning.main, 0.16) }
    default:
      return { color: 'text.secondary', bgcolor: 'action.hover' }
  }
}

export function Badge({ tone = 'neutral', children, sx }: BadgeProps) {
  const base: SxProps<Theme> = {
    display: 'inline-flex',
    alignItems: 'center',
    flex: '0 0 auto',
    px: 0.75,
    py: '2px',
    borderRadius: '6px',
    fontSize: 11,
    fontWeight: 600,
    lineHeight: 1.5,
    whiteSpace: 'nowrap',
  }
  return (
    <Box component="span" data-tone={tone} sx={[base, toneSx(tone), ...(Array.isArray(sx) ? sx : [sx])]}>
      {children}
    </Box>
  )
}
