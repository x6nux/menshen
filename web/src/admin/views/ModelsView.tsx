// oxlint-disable react/set-state-in-effect -- 草稿态需要跟随服务端返回的值重置（保存后刷新）。
// 模型定价（主管理员）：列表 + 新增；详情页改价、启停、删除。
import { useEffect, useState } from 'react'
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
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import { useModelMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { filterRows } from '../../lib/filters'
import { SearchField } from '../../ui'
import { CardBlock, ConfirmButton, DataTable, InfoList, PageHeader, StatusBadge, Toolbar } from '../components'
import type { Column } from '../components'
import { useRunFeedback } from '../feedback'
import { useAdminNav } from '../nav'
import type { Model } from '../../api/types'

export function ModelsView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  if (!state.data) return null

  const models = state.data.models ?? []
  const rows = filterRows(models, q, (m) => m.name)
  const columns: Column<Model>[] = [
    { key: 'name', header: '模型名', render: (row) => row.name },
    { key: 'upstream', header: '上游', width: 140, render: (row) => row.upstream || '—' },
    { key: 'in', header: '输入价', width: 110, align: 'right', render: (row) => row.prompt_price },
    { key: 'out', header: '输出价', width: 110, align: 'right', render: (row) => row.completion_price },
    { key: 'cr', header: '缓存读', width: 100, align: 'right', render: (row) => row.cache_read_price },
    { key: 'cw', header: '缓存写', width: 100, align: 'right', render: (row) => row.cache_write_price },
    {
      key: 'status',
      header: '状态',
      width: 96,
      render: (row) => <StatusBadge info={row.enabled ? { label: '启用', tone: 'ok' } : { label: '停用', tone: 'neutral' }} />,
    },
  ]

  return (
    <>
      <PageHeader
        title="模型定价"
        subtitle={`共 ${models.length} 个；单价用于把开销折算成钱`}
        actions={
          <Button variant="contained" onClick={() => setOpen(true)}>
            ＋ 新增模型
          </Button>
        }
      />
      <Toolbar>
        <SearchField value={q} onChange={setQ} placeholder="搜索模型名" sx={{ maxWidth: 320 }} />
      </Toolbar>
      <DataTable
        rows={rows}
        rowKey={(row) => row.name}
        onRowClick={(row) => nav.go({ k: 'model', name: row.name })}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的模型。</Typography>}
        columns={columns}
      />
      <AddModelDialog open={open} onClose={() => setOpen(false)} upstreams={(state.data.upstreams ?? []).filter((u) => u.status).map((u) => u.name)} />
    </>
  )
}

function AddModelDialog({ open, onClose, upstreams }: { open: boolean; onClose: () => void; upstreams: string[] }) {
  const run = useRunFeedback()
  const mut = useModelMutation()
  const [upstream, setUpstream] = useState(upstreams[0] ?? '')
  const [modelId, setModelId] = useState('')
  const [pp, setPp] = useState('')
  const [cp, setCp] = useState('')
  const [crp, setCrp] = useState('')
  const [cwp, setCwp] = useState('')
  const valid = upstream !== '' && modelId.trim() !== ''

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle sx={{ fontSize: 17 }}>新增模型</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'grid', gap: 2, pt: 0.5 }}>
          <FormControl size="small" fullWidth>
            <InputLabel>上游</InputLabel>
            <Select label="上游" value={upstream} onChange={(e) => setUpstream(e.target.value)}>
              {upstreams.map((name) => (
                <MenuItem key={name} value={name}>
                  {name}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <TextField label="模型 ID" value={modelId} onChange={(e) => setModelId(e.target.value)} helperText="上游侧的模型 ID，可含 /" fullWidth />
          <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 2 }}>
            <TextField label="输入价（每百万 token）" value={pp} onChange={(e) => setPp(e.target.value)} />
            <TextField label="输出价" value={cp} onChange={(e) => setCp(e.target.value)} />
            <TextField label="缓存读价" value={crp} onChange={(e) => setCrp(e.target.value)} />
            <TextField label="缓存写价" value={cwp} onChange={(e) => setCwp(e.target.value)} />
          </Box>
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button
          variant="contained"
          disabled={!valid || mut.isPending}
          onClick={() => {
            run(
              mut.mutateAsync({
                action: 'add',
                upstream,
                model_id: modelId.trim(),
                prompt_price: pp.trim(),
                completion_price: cp.trim(),
                cache_read_price: crp.trim(),
                cache_write_price: cwp.trim(),
              }),
              '已新增',
            )
            setModelId('')
            setPp('')
            setCp('')
            setCrp('')
            setCwp('')
            onClose()
          }}
        >
          新增
        </Button>
      </DialogActions>
    </Dialog>
  )
}

export function ModelDetailView({ name }: { name: string }) {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const mut = useModelMutation()
  if (!state.data) return null

  const model = (state.data.models ?? []).find((m) => m.name === name)
  if (!model) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>模型不存在。</Typography>
      </CardBlock>
    )
  }
  return <ModelDetailBody key={model.name} model={model} run={run} mut={mut} />
}

function ModelDetailBody({
  model,
  run,
  mut,
}: {
  model: Model
  run: (p: Promise<unknown>, ok?: string) => void
  mut: ReturnType<typeof useModelMutation>
}) {
  const [pp, setPp] = useState(String(model.prompt_price))
  const [cp, setCp] = useState(String(model.completion_price))
  const [crp, setCrp] = useState(String(model.cache_read_price))
  const [cwp, setCwp] = useState(String(model.cache_write_price))
  useEffect(() => {
    setPp(String(model.prompt_price))
    setCp(String(model.completion_price))
    setCrp(String(model.cache_read_price))
    setCwp(String(model.cache_write_price))
  }, [model])

  return (
    <>
      <PageHeader
        title={model.name}
        subtitle={model.upstream ? `上游 ${model.upstream} · 模型 ID ${model.model_id}` : '旧格式模型（无上游前缀）'}
        actions={<StatusBadge info={model.enabled ? { label: '启用', tone: 'ok' } : { label: '停用', tone: 'neutral' }} />}
      />

      <CardBlock
        title="启用"
        actions={
          <Switch
            checked={model.enabled}
            disabled={mut.isPending}
            onChange={(e) =>
              run(mut.mutateAsync({ action: 'update', name: model.name, enabled: e.target.checked }), e.target.checked ? '已启用' : '已停用')
            }
          />
        }
      >
        <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>只有启用的模型能出现在判定/复判模型选择里。</Typography>
      </CardBlock>

      <CardBlock title="单价">
        <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(200px,1fr))', gap: 2, maxWidth: 720 }}>
          <TextField label="输入价" value={pp} onChange={(e) => setPp(e.target.value)} />
          <TextField label="输出价" value={cp} onChange={(e) => setCp(e.target.value)} />
          <TextField label="缓存读价" value={crp} onChange={(e) => setCrp(e.target.value)} />
          <TextField label="缓存写价" value={cwp} onChange={(e) => setCwp(e.target.value)} />
        </Box>
        <Box sx={{ mt: 2 }}>
          <Button
            variant="contained"
            disabled={mut.isPending}
            onClick={() =>
              run(
                mut.mutateAsync({
                  action: 'update',
                  name: model.name,
                  prompt_price: pp.trim(),
                  completion_price: cp.trim(),
                  cache_read_price: crp.trim(),
                  cache_write_price: cwp.trim(),
                }),
                '已保存',
              )
            }
          >
            保存单价
          </Button>
        </Box>
      </CardBlock>

      <CardBlock title="其他">
        <InfoList
          items={[
            { label: '模型名', value: model.name },
            { label: '上游', value: model.upstream || '—' },
            { label: '模型 ID', value: model.model_id || '—' },
          ]}
        />
        <Box sx={{ mt: 2 }}>
          <ConfirmButton
            variant="outlined"
            color="error"
            danger
            title="删除模型"
            description="删除后引用它的判定/复判列表需要重新配置。"
            confirmLabel="删除"
            disabled={mut.isPending}
            onConfirm={() => run(mut.mutateAsync({ action: 'remove', name: model.name }), '已删除')}
          >
            删除模型
          </ConfirmButton>
        </Box>
      </CardBlock>
    </>
  )
}
