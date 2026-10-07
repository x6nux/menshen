// 错误态：按 ApiError.status 分流文案。
// 401 → Telegram 引导；403 → 无权限；其余（0 网络/5xx）→ 网络异常 + 重试。
import BlockOutlined from '@mui/icons-material/BlockOutlined'
import CloudOffOutlined from '@mui/icons-material/CloudOffOutlined'
import LockOutlined from '@mui/icons-material/LockOutlined'
import { Box, Button, Typography } from '@mui/material'
import type { ReactNode } from 'react'

export interface ErrorStateProps {
  /** ApiError.status：0 表示网络层失败。 */
  status: number
  onRetry?: () => void
}

interface ErrorCopy {
  icon: ReactNode
  title: string
  description: string
}

function copyFor(status: number): ErrorCopy {
  if (status === 401) {
    return {
      icon: <LockOutlined />,
      title: '请通过 Telegram 菜单按钮打开',
      description: '当前身份已失效。请在 Telegram 内通过 bot 菜单按钮重新打开本页。',
    }
  }
  if (status === 403) {
    return {
      icon: <BlockOutlined />,
      title: '没有权限',
      description: '当前账号无权访问该数据。请确认你已被设置为管理员，或改用有权限的账号。',
    }
  }
  return {
    icon: <CloudOffOutlined />,
    title: '网络异常',
    description: '请检查网络后重试；如果一直失败，可能是服务暂时不可用。',
  }
}

export function ErrorState({ status, onRetry }: ErrorStateProps) {
  const { icon, title, description } = copyFor(status)
  return (
    <Box
      data-testid="error-state"
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        textAlign: 'center',
        py: 6,
        px: 3,
      }}
    >
      <Box sx={{ color: 'text.disabled', '& > svg': { fontSize: 48 }, mb: 1 }}>{icon}</Box>
      <Typography sx={{ fontSize: 15 }}>{title}</Typography>
      <Typography sx={{ mt: 0.5, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
        {description}
      </Typography>
      {onRetry !== undefined && (
        <Button variant="outlined" onClick={onRetry} sx={{ mt: 2, px: 3 }}>
          重试
        </Button>
      )}
    </Box>
  )
}
