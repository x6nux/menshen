// 列表骨架（计划 3.3）：首屏加载时的 3 行占位，行数可调。
import { Box, Skeleton } from '@mui/material'
import type { SxProps, Theme } from '@mui/material/styles'

export interface SkeletonsProps {
  /** 占位行数，默认 3。 */
  rows?: number
  sx?: SxProps<Theme>
}

export function Skeletons({ rows = 3, sx }: SkeletonsProps) {
  return (
    <Box data-testid="skeletons" sx={sx}>
      {Array.from({ length: rows }, (_, index) => (
        <Box
          key={index}
          data-testid="skeleton-row"
          sx={{ display: 'flex', alignItems: 'center', gap: 1.5, py: 1.5 }}
        >
          <Skeleton variant="circular" width={40} height={40} />
          <Box sx={{ flex: 1 }}>
            <Skeleton variant="text" width="55%" />
            <Skeleton variant="text" width="30%" height={16} />
          </Box>
        </Box>
      ))}
    </Box>
  )
}
