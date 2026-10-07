// 空态：图标 + 一句文案 + 可选主按钮。
import InboxOutlined from '@mui/icons-material/InboxOutlined'
import { Box, Typography } from '@mui/material'
import type { ReactNode } from 'react'

export interface EmptyStateProps {
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
}

export function EmptyState({ icon, title, description, action }: EmptyStateProps) {
  return (
    <Box
      data-testid="empty-state"
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        textAlign: 'center',
        py: 6,
        px: 3,
      }}
    >
      <Box sx={{ color: 'text.disabled', '& > svg': { fontSize: 48 }, mb: 1 }}>
        {icon ?? <InboxOutlined />}
      </Box>
      <Typography sx={{ fontSize: 15 }}>{title}</Typography>
      {description !== undefined && (
        <Typography sx={{ mt: 0.5, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
          {description}
        </Typography>
      )}
      {action !== undefined && <Box sx={{ mt: 2 }}>{action}</Box>}
    </Box>
  )
}
