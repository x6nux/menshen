// 记录：判定记录与申诉两个分段，服务端分页 + 搜索。
import { useState } from 'react'
import {
  Box,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Tab,
  Tabs,
  Typography,
} from '@mui/material'
import { useAppeals, useLogs, useMiniState } from '../../api/hooks'
import { actionLabel, apAI, displayTz, fmtTS, kindLabel } from '../../lib/format'
import { appealStatusInfo, verdictInfo } from '../../lib/status'
import { SearchField } from '../../ui'
import { DataTable, PageHeader, StatusBadge, Toolbar } from '../components'
import { useAdminNav, useDebouncedValue } from '../nav'
import type { Column } from '../components'
import type { AppealRow, LogRow } from '../../api/types'

const PAGE_SIZE = 20

const LOG_FILTERS = [
  { value: 'deleted', label: '已删除' },
  { value: 'all', label: '全部' },
  { value: 'ad', label: '广告' },
  { value: 'clean', label: '正常' },
  { value: 'skipped', label: '未送检' },
]

export function RecordsView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const [seg, setSeg] = useState<'logs' | 'appeals'>('logs')
  const [logFilter, setLogFilter] = useState('deleted')
  const [appealFilter, setAppealFilter] = useState('open')
  const [q, setQ] = useState('')
  const [logPage, setLogPage] = useState(1)
  const [appealPage, setAppealPage] = useState(1)
  const debouncedQ = useDebouncedValue(q)

  const logs = useLogs({ filter: logFilter, q: debouncedQ, page: logPage, enabled: seg === 'logs' })
  const appeals = useAppeals({ filter: appealFilter, page: appealPage, enabled: seg === 'appeals' })
  const tz = state.data ? displayTz(state.data) : undefined

  return (
    <>
      <PageHeader
        title="记录"
        subtitle={
          seg === 'logs'
            ? `判定记录，共 ${logs.data?.total ?? 0} 条`
            : `申诉，共 ${appeals.data?.total ?? 0} 条（未结 ${state.data?.todo.open_appeals ?? 0}）`
        }
      />
      <Tabs
        value={seg}
        onChange={(_event, next: 'logs' | 'appeals') => setSeg(next)}
        sx={{ mb: 1.5, minHeight: 36, '& .MuiTab-root': { minHeight: 36, fontSize: 14 } }}
      >
        <Tab value="logs" label="判定记录" />
        <Tab value="appeals" label="申诉" />
      </Tabs>

      <Toolbar>
        {seg === 'logs' ? (
          <>
            <FormControl size="small" sx={{ minWidth: 150 }}>
              <InputLabel>判定</InputLabel>
              <Select
                label="判定"
                value={logFilter}
                onChange={(event) => {
                  setLogFilter(event.target.value)
                  setLogPage(1)
                }}
              >
                {LOG_FILTERS.map((f) => (
                  <MenuItem key={f.value} value={f.value}>
                    {f.label}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
            <SearchField
              value={q}
              onChange={(v) => {
                setQ(v)
                setLogPage(1)
              }}
              placeholder="搜索正文 / 用户 id"
              sx={{ maxWidth: 320 }}
            />
          </>
        ) : (
          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel>状态</InputLabel>
            <Select
              label="状态"
              value={appealFilter}
              onChange={(event) => {
                setAppealFilter(event.target.value)
                setAppealPage(1)
              }}
            >
              <MenuItem value="open">未结</MenuItem>
              <MenuItem value="all">全部</MenuItem>
            </Select>
          </FormControl>
        )}
      </Toolbar>

      {seg === 'logs' ? (
        <>
          <LogTable rows={logs.data?.logs ?? []} loading={logs.isPending} tz={tz} onOpen={(id) => nav.go({ k: 'log', id })} />
          <Pager
            page={logPage}
            total={logs.data?.total ?? 0}
            loading={logs.isFetching}
            onChange={setLogPage}
          />
        </>
      ) : (
        <>
          <AppealTable
            rows={appeals.data?.appeals ?? []}
            loading={appeals.isPending}
            tz={tz}
            onOpen={(id) => nav.go({ k: 'appeal', id })}
          />
          <Pager
            page={appealPage}
            total={appeals.data?.total ?? 0}
            loading={appeals.isFetching}
            onChange={setAppealPage}
          />
        </>
      )}
    </>
  )
}

function LogTable({
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
  const columns: Column<LogRow>[] = [
    { key: 'id', header: '#', width: 70, render: (row) => row.id },
    { key: 'time', header: '时间', width: 130, render: (row) => fmtTS(row.created_at, tz) },
    { key: 'verdict', header: '判定', width: 90, render: (row) => <StatusBadge info={verdictInfo(row.verdict)} /> },
    { key: 'action', header: '处置', width: 120, render: (row) => actionLabel(row.action) },
    { key: 'kind', header: '类型', width: 110, render: (row) => kindLabel(row.kind) },
    { key: 'user', header: '用户', width: 110, render: (row) => `uid ${row.user_id}` },
    {
      key: 'text',
      header: '正文',
      render: (row) => (
        <Typography sx={{ fontSize: 12.5, color: 'text.secondary', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {row.text || row.reason || '—'}
        </Typography>
      ),
    },
    { key: 'cost', header: '开销', width: 90, align: 'right', render: (row) => row.cost_text || '—' },
  ]
  return (
    <DataTable
      loading={loading}
      rows={rows}
      rowKey={(row) => row.id}
      onRowClick={(row) => onOpen(row.id)}
      empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的记录。</Typography>}
      columns={columns}
    />
  )
}

function AppealTable({
  rows,
  loading,
  tz,
  onOpen,
}: {
  rows: AppealRow[]
  loading: boolean
  tz?: string
  onOpen: (id: number) => void
}) {
  const columns: Column<AppealRow>[] = [
    { key: 'id', header: '#', width: 70, render: (row) => row.id },
    { key: 'time', header: '提交时间', width: 130, render: (row) => fmtTS(row.created_at, tz) },
    { key: 'user', header: '用户', width: 110, render: (row) => `uid ${row.user_id}` },
    { key: 'status', header: '状态', width: 100, render: (row) => <StatusBadge info={appealStatusInfo(row.status)} /> },
    { key: 'ai', header: 'AI 结论', render: (row) => apAI(row.ai_result) },
    {
      key: 'reason',
      header: 'AI 理由',
      render: (row) => (
        <Typography sx={{ fontSize: 12.5, color: 'text.secondary', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {row.ai_reason || '—'}
        </Typography>
      ),
    },
    { key: 'web', header: '网页尝试', width: 90, align: 'right', render: (row) => row.web_attempts },
    { key: 'code', header: '解禁码', width: 90, render: (row) => (row.has_code ? '已签发' : '—') },
  ]
  return (
    <DataTable
      loading={loading}
      rows={rows}
      rowKey={(row) => row.id}
      onRowClick={(row) => onOpen(row.id)}
      empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的申诉。</Typography>}
      columns={columns}
    />
  )
}

/** Pager 记录分页：上一页/下一页 + 页码信息。 */
function Pager({
  page,
  total,
  loading,
  onChange,
}: {
  page: number
  total: number
  loading: boolean
  onChange: (page: number) => void
}) {
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mt: 1.5 }}>
      <Button size="small" variant="outlined" disabled={page <= 1 || loading} onClick={() => onChange(page - 1)}>
        上一页
      </Button>
      <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
        第 {page} / {pages} 页
      </Typography>
      <Button
        size="small"
        variant="outlined"
        disabled={page >= pages || loading}
        onClick={() => onChange(page + 1)}
      >
        下一页
      </Button>
    </Box>
  )
}
