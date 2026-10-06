// 上游渠道（主管理员）：列表 + 新增。行点击进入详情。
import { useState } from 'react'
import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  TextField,
  Typography,
} from '@mui/material'
import { useUpstreamMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { DataTable, PageHeader, StatusBadge } from '../components'
import type { Column } from '../components'
import { useRunFeedback } from '../feedback'
import { useAdminNav } from '../nav'
import { KINDS, kindCaps } from './upstreamKinds'
import type { Upstream, UpstreamKind } from '../../api/types'

export function UpstreamsView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const [open, setOpen] = useState(false)
  if (!state.data) return null

  const upstreams = state.data.upstreams ?? []
  const columns: Column<Upstream>[] = [
    { key: 'name', header: '名称', render: (row) => row.name },
    { key: 'kind', header: '渠道类型', width: 180, render: (row) => KINDS.find((k) => k.value === row.kind)?.label ?? row.kind },
    { key: 'base', header: 'base_url', render: (row) => row.base_url || '（默认）' },
    { key: 'key', header: 'api_key', width: 160, render: (row) => row.api_key },
    { key: 'weight', header: '权重', width: 80, align: 'right', render: (row) => row.weight },
    {
      key: 'status',
      header: '状态',
      width: 96,
      render: (row) => <StatusBadge info={row.status ? { label: '启用', tone: 'ok' } : { label: '停用', tone: 'neutral' }} />,
    },
    {
      key: 'caps',
      header: '能力',
      width: 180,
      render: (row) =>
        [row.supports_chat && 'chat', row.supports_systemone && 'systemone'].filter(Boolean).join(' · ') || '—',
    },
  ]

  return (
    <>
      <PageHeader
        title="上游渠道"
        subtitle="AI 上游的端点与密钥；渠道类型决定请求/响应协议"
        actions={
          <Button variant="contained" onClick={() => setOpen(true)}>
            ＋ 新增上游
          </Button>
        }
      />
      <DataTable
        rows={upstreams}
        rowKey={(row) => row.id}
        onRowClick={(row) => nav.go({ k: 'upstream', id: row.id })}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>还没有配置上游渠道。</Typography>}
        columns={columns}
      />
      <AddUpstreamDialog open={open} onClose={() => setOpen(false)} />
    </>
  )
}

function AddUpstreamDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const run = useRunFeedback()
  const mut = useUpstreamMutation()
  const [name, setName] = useState('')
  const [kind, setKind] = useState<UpstreamKind>('openai')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setApiKey] = useState('')

  const valid = name.trim() !== '' && apiKey.trim() !== ''

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle sx={{ fontSize: 17 }}>新增上游渠道</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'grid', gap: 2, pt: 0.5 }}>
          <TextField label="名称" value={name} onChange={(e) => setName(e.target.value)} helperText="用于模型名前缀，如 pai/llm" fullWidth />
          <FormControl size="small" fullWidth>
            <InputLabel>渠道类型</InputLabel>
            <Select label="渠道类型" value={kind} onChange={(e) => setKind(e.target.value as UpstreamKind)}>
              {KINDS.map((k) => (
                <MenuItem key={k.value} value={k.value}>
                  {k.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <TextField
            label="base_url"
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
            helperText="留空用渠道默认端点"
            fullWidth
          />
          <TextField label="api_key" value={apiKey} onChange={(e) => setApiKey(e.target.value)} fullWidth />
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button
          variant="contained"
          disabled={!valid || mut.isPending}
          onClick={() => {
            const caps = kindCaps(kind)
            run(
              mut.mutateAsync({
                action: 'add',
                name: name.trim(),
                kind,
                base_url: baseURL.trim(),
                api_key: apiKey.trim(),
                supports_chat: caps.chat,
                supports_systemone: caps.systemone,
                disabled: false,
              }),
              '已新增',
            )
            setName('')
            setApiKey('')
            setBaseURL('')
            onClose()
          }}
        >
          新增
        </Button>
      </DialogActions>
    </Dialog>
  )
}
