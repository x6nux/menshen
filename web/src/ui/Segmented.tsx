// 分段控件：横向胶囊 chips，受控；用于记录[判定|申诉]、名单分段。
import { ToggleButton, ToggleButtonGroup } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'
import type { ReactNode } from 'react'

export interface SegmentedOption {
  value: string
  label: ReactNode
}

export interface SegmentedProps {
  value: string
  onChange: (value: string) => void
  options: SegmentedOption[]
  ariaLabel?: string
  sx?: SxProps<Theme>
}

export function Segmented({ value, onChange, options, ariaLabel, sx }: SegmentedProps) {
  const groupSx: SxProps<Theme> = {
    gap: 0.75,
    '& .MuiToggleButton-root': {
      border: 0,
      borderRadius: '999px',
      px: 1.5,
      py: 0.5,
      fontSize: 13,
      lineHeight: 1.4,
      textTransform: 'none',
      color: 'text.secondary',
      bgcolor: 'action.hover',
      '&.Mui-selected': {
        color: 'primary.contrastText',
        bgcolor: 'primary.main',
        '&:hover': { bgcolor: 'primary.main' },
      },
    },
  }

  return (
    <ToggleButtonGroup
      exclusive
      value={value}
      onChange={(_event, next: string | null) => {
        // 取消选择（再点当前项）时保持原值，避免出现没有分段的空态。
        if (next !== null) onChange(next)
      }}
      aria-label={ariaLabel}
      sx={[groupSx, ...(Array.isArray(sx) ? sx : [sx])]}
    >
      {options.map((option) => (
        <ToggleButton key={option.value} value={option.value}>
          {option.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  )
}
