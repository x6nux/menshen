// 机器人详情（计划 4.2 / 3.5）：
// - 启用 Switch 用乐观更新（失败回滚 + toast）；
// - 归属改派（仅主管理员、非主 bot）走底部抽屉选 owner_opts；
// - 模型卡仅主管理员可编辑（已登记且启用的模型快选），次管只读；
// - 参数覆盖卡只渲染 specs∩覆盖表（antiad/both 分组），行内智能控件 + 恢复全局；
// - 「管理其群组」带 bot 过滤切到群组页；危险区移除该 bot（ActionSheet）。
import { Box, Button, Chip, IconButton, MenuItem, Switch, TextField, Typography } from '@mui/material'
import AddCircleOutline from '@mui/icons-material/AddCircleOutline'
import RemoveCircleOutline from '@mui/icons-material/RemoveCircleOutline'
import { useState } from 'react'
import { errorStatus } from '../api/client'
import { useBotMutation, useOptimisticMiniMutation, useSetMutation } from '../api/mutations'
import { useMiniState } from '../api/hooks'
import { settingOf } from '../lib/format'
import type { State, Spec } from '../api/types'
import {
  controlKind,
  minutePresets,
  overrideKeys,
  specHint,
  specValueText,
  validateSpecValue,
} from '../lib/settings'
import { useNav } from '../nav'
import {
  Badge,
  EmptyState,
  ErrorState,
  FormDrawer,
  ListRow,
  SectionCard,
  SettingRow,
  Skeletons,
  SwitchRow,
  useConfirm,
  useToast,
} from '../ui'
import { BotStatusBadge } from './shared'

/** parseModelList 把全局默认模型键（JSON 数组字符串）转成展示文本。 */
function parseModelList(raw: string | undefined): string {
  if (!raw) return ''
  try {
    const parsed: unknown = JSON.parse(raw)
    if (Array.isArray(parsed)) {
      return parsed.filter((v): v is string => typeof v === 'string').join(', ')
    }
  } catch {
    // 旧格式（逗号串）直接原样展示。
    return raw
  }
  return ''
}

export function BotDetailPage({ botId }: { botId: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)

  const enableMut = useOptimisticMiniMutation('bot')
  const ownerMut = useBotMutation()
  const modelsMut = useBotMutation()
  const removeMut = useBotMutation()
  const setMut = useSetMutation()
  const optimisticSet = useOptimisticMiniMutation('set')

  const [ownerOpen, setOwnerOpen] = useState(false)
  const [ownerID, setOwnerID] = useState(0)
  const [modelWhich, setModelWhich] = useState<'so' | 'llm' | null>(null)
  const [modelText, setModelText] = useState('')
  const [addOpen, setAddOpen] = useState(false)
  const [editing, setEditing] = useState<Spec | null>(null)
  const [draft, setDraft] = useState('')

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const bot = state.data.bots.find((b) => b.bot_id === botId)
  if (!bot) {
    return <EmptyState title="机器人不存在或无权管理" description="它可能已被移除，或归属已改派给其他管理员。" />
  }

  const { main } = state.data.me
  const overrides = state.data.bot_settings[String(botId)] ?? {}
  const overridden = overrideKeys(state.data.specs, overrides)
  const overriddenKeys = new Set(overridden.map((sp) => sp.key))
  const scopeSpecs = state.data.specs.filter(
    (sp) => sp.group === 'antiad' || sp.group === 'both',
  )
  const addable = scopeSpecs.filter((sp) => !overriddenKeys.has(sp.key))
  const groupCount = state.data.chats.filter((c) => c.bot_id === botId).length
  const modelOptions = (state.data.models ?? []).filter((m) => m.enabled)

  const ownerOpt = (state.data.owner_opts ?? []).find((o) => o.user_id === bot.owner_id)
  const ownerText = ownerOpt ? `${ownerOpt.label} · ${bot.owner_id}` : `uid ${bot.owner_id}`

  const setPending = setMut.isPending || optimisticSet.isPending

  const toggleEnabled = (next: boolean) => {
    enableMut.mutate(
      {
        body: { bot_id: botId, action: next ? 'enable' : 'disable' },
        apply: (s: State) => ({
          ...s,
          bots: s.bots.map((b) => (b.bot_id === botId ? { ...b, enabled: next } : b)),
        }),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? (next ? '已启用' : '已停用')),
        onError: (err) => toast(err.message),
      },
    )
  }

  const submitOwner = () => {
    ownerMut.mutate(
      { bot_id: botId, action: 'owner', owner_id: ownerID },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已改派')
          setOwnerOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  const openModels = (which: 'so' | 'llm') => {
    const list = which === 'so' ? bot.so_models : bot.llm_models
    setModelText(list.join(', '))
    setModelWhich(which)
  }

  const toggleModel = (name: string) => {
    const list = modelText
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    const index = list.indexOf(name)
    if (index >= 0) list.splice(index, 1)
    else list.push(name)
    setModelText(list.join(', '))
  }

  const submitModels = () => {
    if (!modelWhich) return
    modelsMut.mutate(
      { bot_id: botId, action: 'models', which: modelWhich, value: modelText.trim() },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setModelWhich(null)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  const openEditor = (sp: Spec) => {
    setDraft(settingOf(state.data.bot_settings, state.data.global_defaults, botId, sp.key))
    setEditing(sp)
    setAddOpen(false)
  }

  const toggleOverride = (sp: Spec, next: boolean) => {
    optimisticSet.mutate(
      {
        body: { scope: 'bot', bot_id: botId, key: sp.key, value: next ? '1' : '0' },
        apply: (s: State) => {
          const key = String(botId)
          const current = s.bot_settings[key] ?? {}
          return {
            ...s,
            bot_settings: {
              ...s.bot_settings,
              [key]: { ...current, [sp.key]: next ? '1' : '0' },
            },
          }
        },
      },
      {
        onSuccess: (resp) => toast(resp.note ?? '已保存'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const restoreOverride = (sp: Spec) => {
    setMut.mutate(
      { scope: 'bot', bot_id: botId, key: sp.key, value: '' },
      {
        onSuccess: (resp) => toast(resp.note ?? '已恢复全局'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const submitEditor = () => {
    if (!editing) return
    const invalid = validateSpecValue(editing, draft)
    if (invalid) {
      toast(invalid)
      return
    }
    setMut.mutate(
      { scope: 'bot', bot_id: botId, key: editing.key, value: draft.trim() },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setEditing(null)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  const stepDraft = (delta: number) => {
    if (!editing) return
    const parsed = parseInt(draft, 10)
    const base = Number.isFinite(parsed) ? parsed : editing.min
    let next = base + delta
    if (next < editing.min) next = editing.min
    if (editing.max !== 0 && next > editing.max) next = editing.max
    setDraft(String(next))
  }

  const removeBot = async () => {
    const ok = await confirm({
      title: '移除该 bot？',
      description: `将删除「${bot.label}」的群配置与阈值，webhook 会被撤销；判定流水保留。此操作不可恢复。`,
      confirmText: '移除',
      danger: true,
    })
    if (!ok) return
    removeMut.mutate(
      { bot_id: botId, action: 'remove' },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已移除')
          nav.pop()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  const modelRow = (which: 'so' | 'llm', label: string) => {
    const models = which === 'so' ? bot.so_models : bot.llm_models
    const globalText = parseModelList(
      state.data.global_defaults[`antiad_${which}_models`],
    )
    return (
      <Box key={which} sx={{ px: 2, py: 1.25, borderTop: '1px solid', borderColor: 'divider' }}>
        <Box sx={{ display: 'flex', alignItems: 'center' }}>
          <Typography sx={{ flex: 1, fontSize: 16 }}>{label}</Typography>
          {main ? (
            <Button size="small" onClick={() => openModels(which)} sx={{ minWidth: 0 }}>
              编辑
            </Button>
          ) : (
            <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>由主管理员配置</Typography>
          )}
        </Box>
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 0.5 }}>
          {models.length > 0 ? (
            models.map((name) => <Chip key={name} size="small" label={name} />)
          ) : (
            <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
              跟随全局{globalText ? `（${globalText}）` : '（全局未配置）'}
            </Typography>
          )}
        </Box>
      </Box>
    )
  }

  const editingIsToggle = editing !== null && controlKind(editing) === 'toggle'
  const editingIsMinutes = editing !== null && editing.key.endsWith('_minutes')
  const stepSize = editing !== null && editing.key.endsWith('_ms') ? 100 : 1
  // 空输入表示「恢复全局」，不能把 0（永久）误高亮成已选档位。
  const presetSelected = (value: number) => {
    if (draft.trim() === '') return false
    const n = Number(draft)
    return Number.isFinite(n) && n === value
  }

  return (
    <Box data-testid="bot-detail-page">
      <SectionCard>
        <ListRow
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
        />
        <SwitchRow
          primary="启用"
          secondary={bot.enabled ? '关闭后停止判定与处置' : '开启后按配置恢复判定与处置'}
          checked={bot.enabled}
          disabled={enableMut.isPending}
          onChange={toggleEnabled}
        />
        {bot.is_main || !main ? (
          <ListRow
            primary="归属"
            secondary={bot.is_main ? '主 bot 的归属由配置文件决定，不能改派' : undefined}
            value={`uid ${bot.owner_id}`}
          />
        ) : (
          <SettingRow
            label="归属"
            hint="点此把该 bot 改派给其他管理员"
            value={ownerText}
            onClick={() => {
              setOwnerID(bot.owner_id)
              setOwnerOpen(true)
            }}
          />
        )}
        <SettingRow
          label="管理其群组"
          hint="跳到群组页并只看这台 bot 的群"
          value={`${groupCount} 个`}
          onClick={() => nav.switchTab('chats', `bot:${botId}`)}
        />
      </SectionCard>

      <SectionCard title="模型">
        {modelRow('so', '判定模型')}
        {modelRow('llm', '复判模型')}
      </SectionCard>

      <SectionCard
        title="参数覆盖（只显示已覆盖，清空 = 恢复全局）"
        footer={
          <ListRow
            primary="＋ 添加参数覆盖"
            disabled={addable.length === 0}
            chevron={addable.length > 0}
            onClick={() => setAddOpen(true)}
          />
        }
      >
        {overridden.length === 0 ? (
          <Typography sx={{ px: 2, py: 1.5, fontSize: 14, color: 'text.secondary' }}>
            当前没有覆盖项，全部跟随全局。
          </Typography>
        ) : (
          overridden.map((sp) => {
            const effective = settingOf(
              state.data.bot_settings,
              state.data.global_defaults,
              botId,
              sp.key,
            )
            const kind = controlKind(sp)
            return (
              <ListRow
                key={sp.key}
                primary={sp.label}
                secondary={specHint(sp)}
                trailing={
                  kind === 'toggle' ? (
                    <>
                      <Switch
                        size="small"
                        checked={effective === '1'}
                        disabled={setPending}
                        onChange={(event) => toggleOverride(sp, event.target.checked)}
                        slotProps={{ input: { 'aria-label': sp.label } }}
                      />
                      <Button
                        size="small"
                        color="inherit"
                        disabled={setPending}
                        onClick={() => restoreOverride(sp)}
                        sx={{ minWidth: 0, color: 'text.secondary' }}
                      >
                        恢复全局
                      </Button>
                    </>
                  ) : (
                    <>
                      <Button
                        size="small"
                        disabled={setPending}
                        onClick={() => openEditor(sp)}
                        sx={{ minWidth: 0 }}
                      >
                        {specValueText(effective, sp)}
                      </Button>
                      <Button
                        size="small"
                        color="inherit"
                        disabled={setPending}
                        onClick={() => restoreOverride(sp)}
                        sx={{ minWidth: 0, color: 'text.secondary' }}
                      >
                        恢复全局
                      </Button>
                    </>
                  )
                }
              />
            )
          })
        )}
      </SectionCard>

      {!bot.is_main && (
        <SectionCard title="危险区">
          <ListRow
            primary="移除该 bot"
            secondary="删除群配置与阈值并撤销 webhook；判定流水保留"
            disabled={removeMut.isPending}
            onClick={() => void removeBot()}
            sx={{ color: 'error.main' }}
          />
        </SectionCard>
      )}

      <FormDrawer
        open={ownerOpen}
        onClose={() => setOwnerOpen(false)}
        title="改派归属"
        pending={ownerMut.isPending}
        submitText="保存"
        onSubmit={submitOwner}
      >
        <TextField
          select
          fullWidth
          size="small"
          label="新的归属人"
          value={String(ownerID)}
          onChange={(event) => setOwnerID(Number(event.target.value))}
        >
          {(state.data.owner_opts ?? []).map((opt) => (
            <MenuItem key={opt.user_id} value={String(opt.user_id)}>
              {opt.label} · {opt.user_id}
            </MenuItem>
          ))}
        </TextField>
        <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
          改派后，该 bot 的配置只能由新的归属人与主管理员管理。
        </Typography>
      </FormDrawer>

      <FormDrawer
        open={modelWhich !== null}
        onClose={() => setModelWhich(null)}
        title={modelWhich === 'llm' ? '复判模型' : '判定模型'}
        pending={modelsMut.isPending}
        submitText="保存"
        onSubmit={submitModels}
      >
        <TextField
          fullWidth
          multiline
          minRows={2}
          size="small"
          label="模型列表（逗号分隔，按重试顺序）"
          placeholder="例如 demo/gpt-5-mini, demo/gpt-5"
          value={modelText}
          onChange={(event) => setModelText(event.target.value)}
          helperText="留空 = 跟随全局默认；模型需先在「我的 → 模型定价」登记并启用。"
        />
        {modelOptions.length > 0 && (
          <>
            <Typography sx={{ mt: 1.5, fontSize: 13, color: 'text.secondary' }}>
              已登记且启用的模型（点选加入/移出）
            </Typography>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 0.75 }}>
              {modelOptions.map((model) => {
                const picked = modelText
                  .split(',')
                  .map((s) => s.trim())
                  .includes(model.name)
                return (
                  <Chip
                    key={model.name}
                    size="small"
                    label={model.name}
                    color={picked ? 'primary' : 'default'}
                    variant={picked ? 'filled' : 'outlined'}
                    onClick={() => toggleModel(model.name)}
                  />
                )
              })}
            </Box>
          </>
        )}
      </FormDrawer>

      <FormDrawer
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="添加参数覆盖"
      >
        {addable.length === 0 ? (
          <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>
            可覆盖的参数都已在列表里。
          </Typography>
        ) : (
          <>
            <Typography sx={{ mb: 1, fontSize: 13, color: 'text.secondary' }}>
              只列出反广告相关的参数；模型覆盖在「模型」卡里改。
            </Typography>
            {addable.map((sp) => (
              <ListRow
                key={sp.key}
                primary={sp.label}
                secondary={specHint(sp)}
                chevron
                onClick={() => openEditor(sp)}
              />
            ))}
          </>
        )}
      </FormDrawer>

      <FormDrawer
        open={editing !== null}
        onClose={() => setEditing(null)}
        title={editing ? `设置：${editing.label}` : ''}
        pending={setMut.isPending}
        submitText="保存"
        onSubmit={submitEditor}
      >
        {editing !== null && (
          <>
            {editingIsToggle ? (
              <SwitchRow
                primary={editing.label}
                secondary={specHint(editing)}
                checked={draft === '1'}
                onChange={(next) => setDraft(next ? '1' : '0')}
              />
            ) : (
              <>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <IconButton aria-label="减一" onClick={() => stepDraft(-stepSize)}>
                    <RemoveCircleOutline />
                  </IconButton>
                  <TextField
                    fullWidth
                    size="small"
                    label={editing.label}
                    value={draft}
                    onChange={(event) => setDraft(event.target.value)}
                    slotProps={{ htmlInput: { inputMode: 'numeric' } }}
                    helperText={specHint(editing)}
                  />
                  <IconButton aria-label="加一" onClick={() => stepDraft(stepSize)}>
                    <AddCircleOutline />
                  </IconButton>
                </Box>
                {editingIsMinutes && minutePresets(editing).length > 0 && (
                  <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 1.5 }}>
                    {minutePresets(editing).map((preset) => {
                      const picked = presetSelected(preset.value)
                      return (
                        <Chip
                          key={preset.value}
                          size="small"
                          label={preset.label}
                          color={picked ? 'primary' : 'default'}
                          variant={picked ? 'filled' : 'outlined'}
                          data-testid={`preset-${preset.value}`}
                          data-selected={picked ? 'true' : undefined}
                          onClick={() => setDraft(String(preset.value))}
                        />
                      )
                    })}
                  </Box>
                )}
                <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary' }}>
                  {draft.trim() === ''
                    ? '留空并保存 = 恢复全局。'
                    : `生效值：${specValueText(draft, editing)}`}
                </Typography>
              </>
            )}
          </>
        )}
      </FormDrawer>
    </Box>
  )
}
