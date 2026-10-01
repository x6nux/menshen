// 页面内复用的小展示件：状态徽标与指标格。纯展示，无请求逻辑。
import { Box, Typography } from '@mui/material'
import type { ReactNode } from 'react'
import type { Bot, Chat } from '../api/types'
import { botStatus, chatStatus } from '../lib/status'
import { Badge } from '../ui'

/** BotStatusBadge 渲染 运行中/未运行/已停用 徽标。 */
export function BotStatusBadge({ bot }: { bot: Pick<Bot, 'enabled' | 'live'> }) {
  const status = botStatus(bot)
  return <Badge tone={status.tone}>{status.label}</Badge>
}

/** ChatStatusBadge 渲染 判定中/演练/停用 徽标。 */
export function ChatStatusBadge({ chat }: { chat: Pick<Chat, 'enabled' | 'dryrun'> }) {
  const status = chatStatus(chat)
  return <Badge tone={status.tone}>{status.label}</Badge>
}

/** Metric 是概览 2×2 指标格里的一格。 */
export function Metric({ label, value }: { label: ReactNode; value: ReactNode }) {
  return (
    <Box>
      <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.4 }}>
        {label}
      </Typography>
      <Typography sx={{ fontSize: 20, fontWeight: 600, lineHeight: 1.5 }}>{value}</Typography>
    </Box>
  )
}
