// 概览：待办、近 24 小时指标、最近命中、机器人状态。桌面端用指标卡 + 表格。
import { Box, Typography } from '@mui/material'
import { useLogs, useMiniState } from '../../api/hooks'
import { actionLabel, displayTz, fmtTS, kindLabel, verdictLabel } from '../../lib/format'
import { botStatus, verdictInfo } from '../../lib/status'
import { CardBlock, DataTable, PageHeader, StatCard, StatusBadge } from '../components'
import { useAdminNav } from '../nav'
import type { Bot, LogRow } from '../../api/types'

export function OverviewView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const hits = useLogs({ filter: 'ad', page: 1 })

  if (!state.data) return null
  const { todo, stats, bots } = state.data

  return (
    <Box>
      <PageHeader title="概览" subtitle="先看需要处理的事，再看近 24 小时的判定情况" />

      {(todo.open_appeals > 0 || todo.dryrun_chats > 0 || todo.disabled_bots > 0) && (
        <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(180px,1fr))', gap: 1.5, mb: 2 }}>
          {todo.open_appeals > 0 && (
            <StatCard
              label="未结申诉"
              value={todo.open_appeals}
              hint="点开查看并处置"
              onClick={() => nav.go({ k: 'records' })}
            />
          )}
          {todo.dryrun_chats > 0 && (
            <StatCard
              label="演练中的群"
              value={todo.dryrun_chats}
              hint="只记录不处置"
              onClick={() => nav.go({ k: 'chats' })}
            />
          )}
          {todo.disabled_bots > 0 && (
            <StatCard
              label="停用的机器人"
              value={todo.disabled_bots}
              hint="点开检查"
              onClick={() => nav.go({ k: 'bots' })}
            />
          )}
        </Box>
      )}

      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(160px,1fr))', gap: 1.5, mb: 2 }}>
        <StatCard label="已判定" value={stats.checked} hint="近 24 小时" />
        <StatCard label="命中广告" value={stats.hits} hint="近 24 小时" />
        <StatCard label="模型开销" value={stats.cost_text} hint="近 24 小时" />
        <StatCard label="生效群组" value={stats.chats} hint="已启用的群" />
      </Box>

      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: '1.25fr 0.75fr' }, gap: 2 }}>
        <CardBlock title="最近命中">
          <RecentHits rows={hits.data?.logs ?? []} loading={hits.isPending} tz={displayTz(state.data)} onOpen={(id) => nav.go({ k: 'log', id })} />
        </CardBlock>
        <CardBlock title="机器人状态">
          <BotStatusTable bots={bots} onOpen={(id) => nav.go({ k: 'bot', id })} />
        </CardBlock>
      </Box>
    </Box>
  )
}

function RecentHits({
  rows,
  loading,
  tz,
  onOpen,
}: {
  rows: LogRow[]
  loading: boolean
  tz?: string
  onOpen: (id: number) => void
}) {
  return (
    <DataTable
      loading={loading}
      rows={rows.slice(0, 6)}
      rowKey={(row) => row.id}
      onRowClick={(row) => onOpen(row.id)}
      empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>近 24 小时没有命中记录。</Typography>}
      columns={[
        { key: 'time', header: '时间', width: 110, render: (row) => fmtTS(row.created_at, tz) },
        {
          key: 'verdict',
          header: '判定',
          width: 90,
          render: (row) => <StatusBadge info={verdictInfo(row.verdict)} />,
        },
        { key: 'kind', header: '类型', width: 100, render: (row) => kindLabel(row.kind) },
        { key: 'action', header: '处置', width: 120, render: (row) => actionLabel(row.action) },
        { key: 'user', header: '用户', width: 120, render: (row) => `uid ${row.user_id}` },
        {
          key: 'verdictText',
          header: '摘要',
          render: (row) => <Typography sx={{ fontSize: 12.5, color: 'text.secondary' }}>{verdictLabel(row.verdict)}</Typography>,
        },
      ]}
    />
  )
}

function BotStatusTable({ bots, onOpen }: { bots: Bot[]; onOpen: (id: number) => void }) {
  return (
    <DataTable
      rows={bots}
      rowKey={(row) => row.bot_id}
      onRowClick={(row) => onOpen(row.bot_id)}
      empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>还没有接入机器人。</Typography>}
      columns={[
        {
          key: 'name',
          header: '机器人',
          render: (row) => (
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, minWidth: 0 }}>
              <Typography sx={{ fontSize: 13.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {row.label}
              </Typography>
              {row.is_main && <StatusBadge info={{ label: '主 bot', tone: 'neutral' }} />}
            </Box>
          ),
        },
        { key: 'status', header: '状态', width: 96, render: (row) => <StatusBadge info={botStatus(row)} /> },
      ]}
    />
  )
}
