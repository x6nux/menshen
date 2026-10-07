// 分组卡片：13px 灰色组标题（左缩进）+ 白底圆角 12 卡片，无边框无阴影。
// footer 区域自带顶部分割线，用于 `＋ 添加` 这类整行操作。
import { Box, Card, Typography } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'

export interface SectionCardProps {
  /** 组标题；不传则只渲染卡片本体。 */
  title?: ReactNode
  children: ReactNode
  /** 卡片底部区域（如 `添加参数覆盖`）。 */
  footer?: ReactNode
  sx?: SxProps<Theme>
}

export function SectionCard({ title, children, footer, sx }: SectionCardProps) {
  const cardSx: SxProps<Theme> = {
    bgcolor: 'background.paper',
    backgroundImage: 'none',
    borderRadius: '12px',
    boxShadow: 'none',
  }
  return (
    <Box sx={{ mb: 1.5 }}>
      {title !== undefined && (
        <Typography
          component="h2"
          sx={{ fontSize: 13, fontWeight: 500, color: 'text.secondary', mb: 0.75, pl: 1 }}
        >
          {title}
        </Typography>
      )}
      <Card elevation={0} sx={[cardSx, ...(Array.isArray(sx) ? sx : [sx])]}>
        {children}
        {footer !== undefined && (
          <Box sx={{ borderTop: '1px solid', borderColor: 'divider' }}>{footer}</Box>
        )}
      </Card>
    </Box>
  )
}
