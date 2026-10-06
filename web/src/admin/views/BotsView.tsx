// 机器人列表：搜索 + 表格。行点击进入详情。
import { useState } from 'react'
import { Typography } from '@mui/material'
import { useMiniState } from '../../api/hooks'
import { filterRows } from '../../lib/filters'
import { botStatus } from '../../lib/status'
import { SearchField } from '../../ui'
import { DataTable, PageHeader, StatusBadge, Toolbar } from '../components'
import { useAdminNav } from '../nav'
import type { Bot } from '../../api/types'

export function BotsView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const [q, setQ] = useState('')

  if (!state.data) return null
  const bots = state.data.bots
  const rows = filterRows(bots, q, (b) => `${b.label} ${b.username} ${b.bot_id}`)

  return (
    <>
      <PageHeader title="机器人" subtitle={`共 ${bots.length} 个；列表按你的归属权限过滤`} />
      <Toolbar>
        <SearchField value={q} onChange={setQ} placeholder="搜索名称 / @用户名 / bot id" sx={{ maxWidth: 360 }} />
      </Toolbar>
      <DataTable
        rows={rows}
        rowKey={(row) => row.bot_id}
        onRowClick={(row) => nav.go({ k: 'bot', id: row.bot_id })}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的机器人。</Typography>}
        columns={[
          {
            key: 'label',
            header: '机器人',
            render: (row) => <BotName bot={row} />,
          },
          { key: 'username', header: '@用户名', width: 180, render: (row) => row.username || '—' },
          { key: 'id', header: 'bot id', width: 140, render: (row) => row.bot_id },
          {
            key: 'status',
            header: '状态',
            width: 100,
            render: (row) => <StatusBadge info={botStatus(row)} />,
          },
          {
            key: 'chats',
            header: '群组',
            width: 80,
            align: 'right',
            render: (row) => (state.data?.chats ?? []).filter((c) => c.bot_id === row.bot_id).length,
          },
        ]}
      />
    </>
  )
}

/** BotName 机器人名 + 主 bot 标记。 */
export function BotName({ bot }: { bot: Bot }) {
  return (
    <Typography sx={{ fontSize: 13.5, display: 'flex', alignItems: 'center', gap: 0.75 }}>
      <span>{bot.label}</span>
      {bot.is_main && <StatusBadge info={{ label: '主 bot', tone: 'neutral' }} />}
    </Typography>
  )
}
