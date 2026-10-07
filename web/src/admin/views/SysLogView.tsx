// 运行日志（主管理员）：进程内 slog 环形缓冲的只读视图。级别按不低于所选级别筛选。
import { useState } from 'react'
import { Box, Button, FormControl, InputLabel, MenuItem, Select, Typography } from '@mui/material'
import { useInfiniteSysLogs, useMiniState } from '../../api/hooks'
import { displayTz, fmtTSFull } from '../../lib/format'
import { logLevelInfo } from '../../lib/status'
import { CardBlock, DataTable, MonoText, PageHeader, StatusBadge, Toolbar } from '../components'
import { useDebouncedValue } from '../nav'
import { SearchField } from '../../ui'
import type { Column } from '../components'
import type { SysLogRow } from '../../api/types'

const LEVELS = [
  { value: '', label: '全部' },
  { value: 'debug', label: '调试及以上' },
  { value: 'info', label: '信息及以上' },
  { value: 'warn', label: '警告及以上' },
  { value: 'error', label: '仅错误' },
]

export function SysLogView() {
  const state = useMiniState(true)
  const [level, setLevel] = useState('')
  const [q, setQ] = useState('')
  const debouncedQ = useDebouncedValue(q)
  const main = state.data?.me.main ?? false
  const logs = useInfiniteSysLogs({ level, q: debouncedQ, enabled: main })

  if (!main) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>运行日志只对主管理员开放。</Typography>
      </CardBlock>
    )
  }

  const pages = logs.data?.pages ?? []
  const rows = pages.flatMap((p) => p.logs)
  const counts = pages[0]?.counts
  const tz = state.data ? displayTz(state.data) : undefined

  const columns: Column<SysLogRow>[] = [
    { key: 'time', header: '时间', width: 160, render: (row) => fmtTSFull(row.at, tz) },
    { key: 'level', header: '级别', width: 90, render: (row) => <StatusBadge info={logLevelInfo(row.level)} /> },
    { key: 'message', header: '消息', render: (row) => row.message },
    {
      key: 'attrs',
      header: '字段',
      render: (row) =>
        row.attrs.length === 0 ? (
          '—'
        ) : (
          <MonoText>{row.attrs.map((a) => `${a.k}=${a.v}`).join(' ')}</MonoText>
        ),
    },
  ]

  return (
    <>
      <PageHeader
        title="运行日志"
        subtitle={
          counts
            ? `最近 ${logs.data?.pages.length ?? 0} 页；搜索命中：调试 ${counts.debug} · 信息 ${counts.info} · 警告 ${counts.warn} · 错误 ${counts.error}`
            : '进程内缓冲（最近 2000 条，重启清空）'
        }
        actions={
          <Button variant="outlined" disabled={logs.isFetching} onClick={() => void logs.refetch()}>
            刷新
          </Button>
        }
      />
      <Toolbar>
        <FormControl size="small" sx={{ minWidth: 160 }}>
          <InputLabel>级别</InputLabel>
          <Select label="级别" value={level} onChange={(e) => setLevel(e.target.value)}>
            {LEVELS.map((l) => (
              <MenuItem key={l.value} value={l.value}>
                {l.label}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <SearchField value={q} onChange={setQ} placeholder="搜索消息 / 字段" sx={{ maxWidth: 320 }} />
      </Toolbar>

      <DataTable
        loading={logs.isPending}
        rows={rows}
        rowKey={(row) => row.seq}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的日志。</Typography>}
        columns={columns}
      />

      <Box sx={{ display: 'flex', justifyContent: 'center', mt: 1.5, gap: 1 }}>
        {logs.hasNextPage && (
          <Button
            variant="outlined"
            disabled={logs.isFetchingNextPage}
            onClick={() => void logs.fetchNextPage()}
          >
            {logs.isFetchingNextPage ? '加载中…' : '加载更多'}
          </Button>
        )}
        {!logs.hasNextPage && rows.length > 0 && (
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>已到末尾（{rows.length} 条）</Typography>
        )}
      </Box>
    </>
  )
}
