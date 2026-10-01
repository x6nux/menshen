// 搜索框（计划 3.2）：圆角灰底、Search 图标、受控 value/onChange、可清空。
import Close from '@mui/icons-material/Close'
import Search from '@mui/icons-material/Search'
import { IconButton, InputAdornment, TextField } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'

export interface SearchFieldProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  /** 输入框的无障碍名称；缺省用 placeholder。 */
  ariaLabel?: string
  autoFocus?: boolean
  disabled?: boolean
  /** 有内容时显示清空按钮，默认显示。 */
  clearable?: boolean
  sx?: SxProps<Theme>
}

export function SearchField({
  value,
  onChange,
  placeholder = '搜索',
  ariaLabel,
  autoFocus = false,
  disabled = false,
  clearable = true,
  sx,
}: SearchFieldProps) {
  const fieldSx: SxProps<Theme> = {
    '& .MuiOutlinedInput-root': {
      borderRadius: '10px',
      minHeight: 40,
      bgcolor: (theme) => (theme.palette.mode === 'dark' ? 'rgba(255,255,255,.08)' : '#F2F2F2'),
      '& fieldset': { border: 'none' },
      '&:hover fieldset': { border: 'none' },
      '&.Mui-focused fieldset': { border: 'none' },
    },
    '& .MuiOutlinedInput-input': { fontSize: 15, py: '9px' },
  }

  return (
    <TextField
      value={value}
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      autoFocus={autoFocus}
      disabled={disabled}
      fullWidth
      size="small"
      sx={[fieldSx, ...(Array.isArray(sx) ? sx : [sx])]}
      slotProps={{
        // 显式给 input 一个无障碍名称：placeholder 只作视觉提示，读屏更依赖 aria-label。
        htmlInput: { 'aria-label': ariaLabel ?? placeholder },
        input: {
          startAdornment: (
            <InputAdornment position="start">
              <Search sx={{ fontSize: 20, color: 'text.secondary' }} />
            </InputAdornment>
          ),
          endAdornment:
            clearable && value ? (
              <InputAdornment position="end">
                <IconButton aria-label="清空" size="small" edge="end" onClick={() => onChange('')}>
                  <Close sx={{ fontSize: 18 }} />
                </IconButton>
              </InputAdornment>
            ) : undefined,
        },
      }}
    />
  )
}
