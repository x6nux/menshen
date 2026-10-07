// 语义徽标：ok/no/warn 三色，底色为对应主题色的半透明色块；
// neutral 用中性样式，用于主 bot 这类中性标记。
import { Box, alpha } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'

export type BadgeTone = 'ok' | 'no' | 'warn' | 'neutral'

export interface BadgeProps {
  tone?: BadgeTone
  children: ReactNode
  sx?: SxProps<Theme>
}

/**
 * 语义色调的固定文字色：浅色主题用深色字、深色主题用亮色字。
 * 不用 MUI 自动 dark 变体（会随 main 变，无法保证对比度）；这里的取值都在
 * 16% 同色淡底（合成到卡片白/深底）上实测 ≥4.5:1（11px 小字 AA）。
 */
const TONE_TEXT: Record<Exclude<BadgeTone, 'neutral'>, { light: string; dark: string }> = {
  ok: { light: '#067A3E', dark: '#4ADE80' },
  no: { light: '#C62828', dark: '#FF8A80' },
  warn: { light: '#7A5B00', dark: '#FFD54F' },
}

const TONE_PALETTE: Record<Exclude<BadgeTone, 'neutral'>, 'success' | 'error' | 'warning'> = {
  ok: 'success',
  no: 'error',
  warn: 'warning',
}

function toneSx(tone: BadgeTone): SxProps<Theme> {
  if (tone === 'neutral') {
    return { color: 'text.secondary', bgcolor: 'action.hover' }
  }
  const text = TONE_TEXT[tone]
  return {
    color: (theme) => (theme.palette.mode === 'dark' ? text.dark : text.light),
    bgcolor: (theme) => alpha(theme.palette[TONE_PALETTE[tone]].main, 0.16),
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
