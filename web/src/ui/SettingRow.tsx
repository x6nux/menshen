// 设置行（计划 3.2）：左 label + 可选 hint，右 value + ›，点击触发 onClick。
import type { ReactNode } from 'react'
import { ListRow } from './ListRow'

export interface SettingRowProps {
  label: ReactNode
  hint?: ReactNode
  value?: ReactNode
  onClick: () => void
  disabled?: boolean
}

export function SettingRow({ label, hint, value, onClick, disabled = false }: SettingRowProps) {
  return (
    <ListRow
      primary={label}
      secondary={hint}
      value={value}
      chevron
      onClick={onClick}
      disabled={disabled}
    />
  )
}
