// 机器人列表（计划 4.2）：本地搜索（label/username/bot_id）+ 状态徽标 + 空态引导。
import { Box } from '@mui/material'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { filterRows } from '../lib/filters'
import { useNav } from '../nav'
import { Badge, EmptyState, ErrorState, ListRow, SearchField, SectionCard, Skeletons } from '../ui'
import { BotStatusBadge } from './shared'
import { useState } from 'react'

export function BotsPage() {
  const nav = useNav()
  const state = useMiniState(true)
  const [q, setQ] = useState('')

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const bots = state.data.bots
  const visible = filterRows(bots, q, (b) => `${b.label} ${b.username} ${b.bot_id}`)

  return (
    <Box data-testid="bots-page">
      {bots.length > 0 && (
        <SearchField
          value={q}
          onChange={setQ}
          placeholder="搜索名称 / 用户名 / bot ID"
          ariaLabel="搜索机器人"
          sx={{ mb: 1.5 }}
        />
      )}
      {visible.length === 0 ? (
        bots.length === 0 ? (
          <EmptyState
            title="还没有可管理的机器人"
            description="接入新 bot 需要在 Telegram 里私聊主 bot，发送 /start 按引导操作；本页只展示你名下的机器人。"
          />
        ) : (
          <EmptyState title="没有匹配的机器人" description="换个关键词试试。" />
        )
      ) : (
        <SectionCard>
          {visible.map((bot) => (
            <ListRow
              key={bot.bot_id}
              primary={
                <>
                  {bot.label}
                  {bot.is_main && (
                    <>
                      {' '}
                      <Badge>主 bot</Badge>
                    </>
                  )}
                </>
              }
              secondary={`@${bot.username || '—'} · bot_id ${bot.bot_id}`}
              badge={<BotStatusBadge bot={bot} />}
              chevron
              onClick={() => nav.push({ k: 'bot', id: bot.bot_id })}
            />
          ))}
        </SectionCard>
      )}
    </Box>
  )
}
