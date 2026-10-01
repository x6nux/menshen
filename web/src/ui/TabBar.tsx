// 底部 Tab 栏（计划 3.2）：五个一级 Tab；选中主色、未选中灰；
// 底部安全区用 env(safe-area-inset-bottom)；仅在 tab 根页显示——导航栈非空
// （正在看二级页）时整条隐藏，返回后自动恢复。
import GroupsOutlined from '@mui/icons-material/GroupsOutlined'
import PersonOutlineOutlined from '@mui/icons-material/PersonOutlineOutlined'
import ReceiptLongOutlined from '@mui/icons-material/ReceiptLongOutlined'
import SmartToyOutlined from '@mui/icons-material/SmartToyOutlined'
import SpaceDashboardOutlined from '@mui/icons-material/SpaceDashboardOutlined'
import { BottomNavigation, BottomNavigationAction } from '@mui/material'
import type { ReactNode } from 'react'
import { useNav } from '../nav'
import type { TabKey } from '../nav'

interface TabItem {
  key: TabKey
  label: string
  icon: ReactNode
}

const TABS: TabItem[] = [
  { key: 'overview', label: '概览', icon: <SpaceDashboardOutlined /> },
  { key: 'bots', label: '机器人', icon: <SmartToyOutlined /> },
  { key: 'chats', label: '群组', icon: <GroupsOutlined /> },
  { key: 'records', label: '记录', icon: <ReceiptLongOutlined /> },
  { key: 'mine', label: '我的', icon: <PersonOutlineOutlined /> },
]

export function TabBar() {
  const { tab, stack, switchTab } = useNav()

  // 二级页有自己的底部操作栏；Tab 栏只在 tab 根页出现。
  if (stack.length > 0) return null

  return (
    <BottomNavigation
      value={tab}
      onChange={(_event, next: TabKey) => switchTab(next)}
      showLabels
      sx={{
        position: 'fixed',
        left: 0,
        right: 0,
        bottom: 0,
        height: 'auto',
        bgcolor: 'background.paper',
        borderTop: '1px solid',
        borderColor: 'divider',
        pb: 'env(safe-area-inset-bottom)',
        zIndex: (theme) => theme.zIndex.appBar,
        '& .MuiBottomNavigationAction-root': {
          minWidth: 0,
          py: 0.75,
          color: 'text.secondary',
          '&.Mui-selected': { color: 'primary.main' },
        },
        '& .MuiBottomNavigationAction-label': {
          fontSize: 10,
          '&.Mui-selected': { fontSize: 10 },
        },
      }}
    >
      {TABS.map((item) => (
        <BottomNavigationAction key={item.key} value={item.key} label={item.label} icon={item.icon} />
      ))}
    </BottomNavigation>
  )
}
