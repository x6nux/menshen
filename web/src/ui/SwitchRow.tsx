// 开关行：列表行的 Switch 形态，整行与开关都可点，但一次点击
// 只触发一次 onChange —— 开关自己的 click 阻止冒泡，避免行与开关各翻一次。
import { Box, ListItemButton, ListItemText, Switch } from '@mui/material'
import type { ReactNode } from 'react'

export interface SwitchRowProps {
  primary: ReactNode
  secondary?: ReactNode
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  badge?: ReactNode
  /** 开关的无障碍名；primary 不是纯文本时（如群名带链接）必须显式给。 */
  ariaLabel?: string
}

export function SwitchRow({
  primary,
  secondary,
  checked,
  onChange,
  disabled = false,
  badge,
  ariaLabel,
}: SwitchRowProps) {
  return (
    <ListItemButton
      onClick={() => {
        if (!disabled) onChange(!checked)
      }}
      disabled={disabled}
      sx={{ px: 2, py: 0.5 }}
    >
      <ListItemText
        primary={primary}
        secondary={secondary}
        slotProps={{
          primary: { sx: { fontSize: 16 } },
          secondary: { sx: { fontSize: 13, lineHeight: 1.4 } },
        }}
        sx={{ my: 0, minWidth: 0 }}
      />
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, pl: 1, flexShrink: 0 }}>
        {badge}
        <Switch
          edge="end"
          size="small"
          checked={checked}
          disabled={disabled}
          onChange={(event) => {
            if (!disabled) onChange(event.target.checked)
          }}
          onClick={(event) => event.stopPropagation()}
          slotProps={{
            input: {
              'aria-label': ariaLabel ?? (typeof primary === 'string' ? primary : '开关'),
            },
          }}
        />
      </Box>
    </ListItemButton>
  )
}
