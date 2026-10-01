// 模型定价（计划 4.9，仅主管理员）：
// - 列表：搜索（名称，本地）；行 = 等宽模型名 + 启用徽标 + 单价摘要；
// - 「＋」抽屉：上游（仅启用中）+ 模型 ID + 四个价格（数字键盘）→ model add；
// - 详情：四价格编辑、启停 Switch（乐观）、删除（ActionSheet 写明对象）。
import { Box, Button, MenuItem, TextField, Typography } from '@mui/material'
import { useState } from 'react'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useModelMutation, useOptimisticMiniMutation } from '../api/mutations'
import type { Model, State } from '../api/types'
import { filterRows } from '../lib/filters'
import { useNav } from '../nav'
import {
  Badge,
  EmptyState,
  ErrorState,
  FormDrawer,
  ListRow,
  SearchField,
  SectionCard,
  Skeletons,
  SwitchRow,
  useConfirm,
  useToast,
} from '../ui'

const MONO = 'ui-monospace, Menlo, monospace'

function priceText(m: Pick<Model, 'prompt_price' | 'completion_price'>): string {
  return `输入 $${m.prompt_price}/M · 补全 $${m.completion_price}/M`
}

function priceSummary(m: Model): string {
  return `${priceText(m)} · 缓存读 $${m.cache_read_price} / 写 $${m.cache_write_price}`
}

export function ModelsPage() {
  const nav = useNav()
  const toast = useToast()
  const state = useMiniState(true)
  const addMut = useModelMutation()

  const [q, setQ] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [upstream, setUpstream] = useState('')
  const [modelID, setModelID] = useState('')
  const [prompt, setPrompt] = useState('0')
  const [completion, setCompletion] = useState('0')
  const [cacheRead, setCacheRead] = useState('0')
  const [cacheWrite, setCacheWrite] = useState('0')

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!state.data.me.main) return <ErrorState status={403} />

  const models = state.data.models ?? []
  const enabledUpstreams = (state.data.upstreams ?? []).filter((u) => u.status)
  const visible = filterRows(models, q, (m) => m.name)

  function openAdd() {
    setUpstream(enabledUpstreams[0]?.name ?? '')
    setModelID('')
    setPrompt('0')
    setCompletion('0')
    setCacheRead('0')
    setCacheWrite('0')
    setAddOpen(true)
  }

  function submitAdd() {
    addMut.mutate(
      {
        action: 'add',
        upstream,
        model_id: modelID.trim(),
        prompt_price: prompt.trim(),
        completion_price: completion.trim(),
        cache_read_price: cacheRead.trim(),
        cache_write_price: cacheWrite.trim(),
      },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已添加；可在「全局设置 → 默认模型」里引用')
          setAddOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="models-page">
      <Box sx={{ mb: 1 }}>
        <SearchField
          value={q}
          onChange={setQ}
          placeholder="搜索模型名称"
          ariaLabel="搜索模型"
        />
      </Box>

      <SectionCard
        title={countText(visible.length, models.length)}
        footer={enabledUpstreams.length === 0 ? undefined : (
          <ListRow primary="＋ 添加模型" chevron onClick={openAdd} />
        )}
      >
        {visible.length === 0 ? (
          <Typography sx={{ px: 2, py: 1.5, fontSize: 13, color: 'text.secondary' }}>
            {models.length === 0 ? '（还没有模型）' : '没有匹配的模型'}
          </Typography>
        ) : (
          visible.map((m) => (
            <ListRow
              key={m.name}
              primary={
                <Box component="span" sx={{ fontFamily: MONO, fontSize: 15 }}>
                  {m.name}
                </Box>
              }
              secondary={priceSummary(m)}
              badge={<Badge tone={m.enabled ? 'ok' : 'no'}>{m.enabled ? '启用' : '停用'}</Badge>}
              chevron
              onClick={() => nav.push({ k: 'model', name: m.name })}
            />
          ))
        )}
      </SectionCard>

      {enabledUpstreams.length === 0 && (
        <Typography sx={{ px: 1.5, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
          还没有启用的上游渠道：先到「上游渠道」添加并启用，才能登记模型。
        </Typography>
      )}

      <FormDrawer
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="添加模型"
        pending={addMut.isPending}
        submitText="添加"
        submitDisabled={upstream === '' || modelID.trim() === ''}
        onSubmit={submitAdd}
      >
        <TextField
          select
          fullWidth
          size="small"
          label="上游（仅启用中）"
          value={upstream}
          onChange={(event) => setUpstream(event.target.value)}
        >
          {enabledUpstreams.map((u) => (
            <MenuItem key={u.id} value={u.name}>
              {u.name}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          fullWidth
          size="small"
          label="模型 ID"
          placeholder="如 gpt-5-mini"
          value={modelID}
          onChange={(event) => setModelID(event.target.value)}
          sx={{ mt: 1.5 }}
        />
        <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
          <TextField
            fullWidth
            size="small"
            label="输入价 $/M"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            slotProps={{ htmlInput: { inputMode: 'decimal' } }}
          />
          <TextField
            fullWidth
            size="small"
            label="补全价 $/M"
            value={completion}
            onChange={(event) => setCompletion(event.target.value)}
            slotProps={{ htmlInput: { inputMode: 'decimal' } }}
          />
        </Box>
        <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
          <TextField
            fullWidth
            size="small"
            label="缓存读取价"
            value={cacheRead}
            onChange={(event) => setCacheRead(event.target.value)}
            slotProps={{ htmlInput: { inputMode: 'decimal' } }}
          />
          <TextField
            fullWidth
            size="small"
            label="缓存创建价"
            value={cacheWrite}
            onChange={(event) => setCacheWrite(event.target.value)}
            slotProps={{ htmlInput: { inputMode: 'decimal' } }}
          />
        </Box>
        <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
          四项价格单位都是每百万 token 美元；留空按 0 计。模型全名 = 上游名/模型 ID。
        </Typography>
      </FormDrawer>
    </Box>
  )
}

function countText(shown: number, total: number): string {
  return shown === total ? `共 ${total} 个模型` : `匹配 ${shown} / 共 ${total} 个模型`
}

/** PriceCard 是详情的四价格编辑卡：子组件持有草稿，挂载时从模型值初始化。 */
function PriceCard({ model }: { model: Model }) {
  const toast = useToast()
  const mut = useModelMutation()
  const [prompt, setPrompt] = useState(String(model.prompt_price))
  const [completion, setCompletion] = useState(String(model.completion_price))
  const [cacheRead, setCacheRead] = useState(String(model.cache_read_price))
  const [cacheWrite, setCacheWrite] = useState(String(model.cache_write_price))

  function submit() {
    mut.mutate(
      {
        action: 'update',
        name: model.name,
        prompt_price: prompt.trim(),
        completion_price: completion.trim(),
        cache_read_price: cacheRead.trim(),
        cache_write_price: cacheWrite.trim(),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? '已保存'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const field = (
    label: string,
    value: string,
    setValue: (next: string) => void,
  ) => (
    <TextField
      fullWidth
      size="small"
      label={label}
      value={value}
      onChange={(event) => setValue(event.target.value)}
      slotProps={{ htmlInput: { inputMode: 'decimal' } }}
    />
  )

  return (
    <SectionCard title="单价（每百万 token，美元）">
      <Box sx={{ p: 1.5 }}>
        <Box sx={{ display: 'flex', gap: 1 }}>
          {field('输入价', prompt, setPrompt)}
          {field('补全价', completion, setCompletion)}
        </Box>
        <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
          {field('缓存读取价', cacheRead, setCacheRead)}
          {field('缓存创建价', cacheWrite, setCacheWrite)}
        </Box>
        <Button
          fullWidth
          variant="contained"
          loading={mut.isPending}
          disabled={mut.isPending}
          onClick={submit}
          sx={{ mt: 2 }}
        >
          保存价格
        </Button>
        <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
          留空按 0 计；改价只影响之后的核算，不会重算历史开销。
        </Typography>
      </Box>
    </SectionCard>
  )
}

export function ModelDetailPage({ name }: { name: string }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)
  const removeMut = useModelMutation()
  const enableMut = useOptimisticMiniMutation('model')

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!state.data.me.main) return <ErrorState status={403} />

  const model = (state.data.models ?? []).find((m) => m.name === name)
  if (!model) {
    return <EmptyState title="模型不存在" description="它可能已被删除。" />
  }

  function toggleEnabled(next: boolean) {
    enableMut.mutate(
      {
        body: { action: 'update', name, enabled: next },
        apply: (s: State) => ({
          ...s,
          models: (s.models ?? []).map((m) => (m.name === name ? { ...m, enabled: next } : m)),
        }),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? (next ? '已启用' : '已停用')),
        onError: (err) => toast(err.message),
      },
    )
  }

  async function removeModel() {
    const ok = await confirm({
      title: '删除该模型？',
      description: `将删除「${model?.name}」及其单价配置；仍在默认模型或机器人模型列表里引用时会判定失败，请先改掉引用。此操作不可恢复。`,
      confirmText: '删除',
      danger: true,
    })
    if (!ok) return
    removeMut.mutate(
      { action: 'remove', name },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已删除')
          nav.pop()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="model-detail-page">
      <SectionCard>
        <ListRow
          primary={
            <Box component="span" sx={{ fontFamily: MONO, fontSize: 15 }}>
              {model.name}
            </Box>
          }
          secondary={`上游 ${model.upstream || '（旧格式）'} ｜ 模型 ID ${model.model_id}`}
          badge={<Badge tone={model.enabled ? 'ok' : 'no'}>{model.enabled ? '启用' : '停用'}</Badge>}
        />
        <SwitchRow
          primary="启用"
          secondary={model.enabled ? '停用后不能再被选为默认模型或 bot 模型' : '启用后才能被引用'}
          checked={model.enabled}
          disabled={enableMut.isPending}
          onChange={toggleEnabled}
        />
      </SectionCard>

      <PriceCard key={model.name} model={model} />

      <SectionCard title="危险区">
        <ListRow
          primary="删除该模型"
          secondary="先确认没有默认模型或机器人还在引用它"
          disabled={removeMut.isPending}
          onClick={() => void removeModel()}
          sx={{ color: 'error.main' }}
        />
      </SectionCard>
    </Box>
  )
}
