// 占位页（Task 1）：保证五个 tab 与二级页导航可达。
// Task 2 起真实页面会替换 App.tsx 里集中维护的映射，此文件随后删除。
import ConstructionOutlined from '@mui/icons-material/ConstructionOutlined'
import { Box } from '@mui/material'
import { EmptyState } from '../ui'

export interface PlaceholderProps {
  /** 当前页名称，仅用于确认导航到了哪一页。 */
  name?: string
}

export function Placeholder({ name }: PlaceholderProps) {
  return (
    <Box sx={{ pt: 4 }}>
      <EmptyState
        icon={<ConstructionOutlined />}
        title="页面开发中"
        description={name !== undefined ? `${name}将在后续版本上线` : undefined}
      />
    </Box>
  )
}
