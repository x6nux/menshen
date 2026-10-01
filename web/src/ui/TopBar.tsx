// 顶部导航栏（计划 3.2）：居中标题 17px/600，可选副标题 13px 灰；
// 无阴影、背景同页面底色。左侧返回在导航栈非空时自动出现并 pop（与 Telegram
// BackButton 双绑由 NavProvider 负责），也可用 onBack 覆盖行为；右侧可放动作。
// 依赖 NavProvider：TopBar 只作为应用外壳使用。
import ArrowBackIosNew from '@mui/icons-material/ArrowBackIosNew'
import { AppBar, Box, IconButton, Toolbar, Typography } from '@mui/material'
import type { ReactNode } from 'react'
import { useNav } from '../nav'

export interface TopBarProps {
  title: ReactNode
  subtitle?: ReactNode
  /** 右侧动作（IconButton 等）。 */
  actions?: ReactNode
  /** 覆盖默认的 pop 行为；传了 onBack 时即使栈为空也显示返回。 */
  onBack?: () => void
}

const SLOT_WIDTH = 40

export function TopBar({ title, subtitle, actions, onBack }: TopBarProps) {
  const nav = useNav()
  const canBack = onBack !== undefined || nav.stack.length > 0

  const handleBack = () => {
    if (onBack !== undefined) onBack()
    else nav.pop()
  }

  return (
    <AppBar
      position="sticky"
      elevation={0}
      color="transparent"
      sx={{
        bgcolor: 'background.default',
        backgroundImage: 'none',
        boxShadow: 'none',
      }}
    >
      <Toolbar
        disableGutters
        sx={{
          minHeight: 44,
          '@media (min-width:600px)': { minHeight: 44 },
          px: 1,
          py: 0.5,
        }}
      >
        {canBack ? (
          <IconButton
            aria-label="返回"
            size="small"
            onClick={handleBack}
            sx={{ width: SLOT_WIDTH, height: SLOT_WIDTH, color: 'text.primary' }}
          >
            <ArrowBackIosNew sx={{ fontSize: 18 }} />
          </IconButton>
        ) : (
          <Box sx={{ width: SLOT_WIDTH, flexShrink: 0 }} />
        )}
        <Box sx={{ flex: 1, minWidth: 0, textAlign: 'center' }}>
          <Typography noWrap sx={{ fontSize: 17, fontWeight: 600, lineHeight: 1.3 }}>
            {title}
          </Typography>
          {subtitle !== undefined && (
            <Typography noWrap sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.3 }}>
              {subtitle}
            </Typography>
          )}
        </Box>
        <Box
          sx={{
            width: SLOT_WIDTH,
            flexShrink: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'flex-end',
          }}
        >
          {actions}
        </Box>
      </Toolbar>
    </AppBar>
  )
}
