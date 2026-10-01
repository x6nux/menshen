// 全局设置（计划 3.5 / 4.10，仅主管理员；次管按无权限处理）：
// - 总开关：antiad_enabled / alert_copy_main / gban_enabled，Switch 乐观更新，
//   提交必须是字符串 '1'/'0'（对应旧 TestMiniAppGlobalTogglesValidJS 的迁移不变量）；
// - 默认模型：判定/复判/识图，抽屉内已启用模型快选 + 逗号输入，按重试顺序；
// - 主题折叠卡（state.sections）：卡头「已设置 N 项」；逐项智能控件——
//   toggle → Switch、number → 抽屉（时长类预设按 spec.min/max 过滤并显示换算）；
// - 展示时区 / 群内提示附加链接 / 形态摘要 / 修正文本。
import AddCircleOutline from '@mui/icons-material/AddCircleOutline'
import ExpandMore from '@mui/icons-material/ExpandMore'
import RemoveCircleOutline from '@mui/icons-material/RemoveCircleOutline'
import {
  Box,
  Button,
  Card,
  Chip,
  Collapse,
  IconButton,
  ListItemButton,
  ListItemText,
  TextField,
  Typography,
} from '@mui/material'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useDigestMutation, useOptimisticMiniMutation, useSetMutation } from '../api/mutations'
import type { Spec, State } from '../api/types'
import {
  controlKind,
  minutePresets,
  specHint,
  specValueText,
  validateSpecValue,
} from '../lib/settings'
import {
  ErrorState,
  FormDrawer,
  ListRow,
  SectionCard,
  SettingRow,
  Skeletons,
  SwitchRow,
  useToast,
} from '../ui'

/** parseModelList 把模型列表键（JSON 数组或旧逗号串）转成逗号分隔文本。 */
function parseModelList(raw: string | undefined): string {
  if (!raw) return ''
  try {
    const parsed: unknown = JSON.parse(raw)
    if (Array.isArray(parsed)) {
      return parsed.filter((v): v is string => typeof v === 'string').join(', ')
    }
  } catch {
    // 旧格式（逗号串）原样展示。
    return raw
  }
  return ''
}

const TIMEZONES = [
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'UTC',
]

const MASTER_TOGGLES: { key: string; label: string; hint: string }[] = [
  { key: 'antiad_enabled', label: '反广告总开关', hint: '关闭后所有机器人停止判定与处置' },
  { key: 'alert_copy_main', label: '告警抄送主管理员', hint: '命中告警同时私聊主管理员一份' },
  { key: 'gban_enabled', label: '联合封禁', hint: '关闭后联封名单不再自动执行' },
]

type ModelWhich = 'so' | 'llm' | 'vision'

const MODEL_ROWS: { which: ModelWhich; key: string; label: string; hint: string }[] = [
  {
    which: 'so',
    key: 'antiad_so_models',
    label: '判定模型（systemone）',
    hint: '按重试顺序，逗号分隔；各 bot 未覆盖时使用',
  },
  {
    which: 'llm',
    key: 'antiad_llm_models',
    label: '复判模型（大模型）',
    hint: '按重试顺序，逗号分隔',
  },
  { which: 'vision', key: 'antiad_vision_model', label: '识图模型', hint: '留空 = 图片与贴纸不判' },
]

/** SettingsSection 是设置主题折叠卡：卡头显示已设置项数，展开后渲染子行。 */
function SettingsSection({
  name,
  configured,
  children,
}: {
  name: string
  configured: number
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <Card
      elevation={0}
      sx={{
        bgcolor: 'background.paper',
        backgroundImage: 'none',
        borderRadius: '12px',
        boxShadow: 'none',
        mb: 1.5,
      }}
    >
      <ListItemButton onClick={() => setOpen((prev) => !prev)} aria-expanded={open}>
        <ListItemText
          primary={name}
          secondary={`已设置 ${configured} 项`}
          slotProps={{
            primary: { sx: { fontSize: 15 } },
            secondary: { sx: { fontSize: 12 } },
          }}
          sx={{ my: 0 }}
        />
        <ExpandMore
          sx={{
            color: 'text.disabled',
            transition: 'transform .2s',
            transform: open ? 'rotate(180deg)' : 'none',
          }}
        />
      </ListItemButton>
      <Collapse in={open} unmountOnExit>
        {children}
      </Collapse>
    </Card>
  )
}

export function SettingsPage() {
  const toast = useToast()
  const state = useMiniState(true)
  const setMut = useSetMutation()
  const toggleMut = useOptimisticMiniMutation('set')
  const digestMut = useDigestMutation()
  const runMut = useDigestMutation()

  const [modelEdit, setModelEdit] = useState<ModelWhich | null>(null)
  const [modelDraft, setModelDraft] = useState('')
  const [editingSpec, setEditingSpec] = useState<Spec | null>(null)
  const [specDraft, setSpecDraft] = useState('')
  const [tzOpen, setTzOpen] = useState(false)
  const [tzDraft, setTzDraft] = useState('')
  const [footerOpen, setFooterOpen] = useState(false)
  const [footerDraft, setFooterDraft] = useState('')
  const [digestOpen, setDigestOpen] = useState(false)
  const [digestDraft, setDigestDraft] = useState('')
  const [fixOpen, setFixOpen] = useState(false)
  const [fixDraft, setFixDraft] = useState('')

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!state.data.me.main) return <ErrorState status={403} />

  const data = state.data
  const global = data.global ?? {}
  const enabledModels = (data.models ?? []).filter((m) => m.enabled)

  /** toggleKey 是总开关与 spec 开关共用的乐观写：全局值只认字符串 '1'/'0'。 */
  function toggleKey(key: string, next: boolean) {
    const value = next ? '1' : '0'
    toggleMut.mutate(
      {
        body: { scope: 'global', key, value },
        apply: (s: State) => ({ ...s, global: { ...(s.global ?? {}), [key]: value } }),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? '已保存'),
        onError: (err) => toast(err.message),
      },
    )
  }

  function openModel(which: ModelWhich) {
    const row = MODEL_ROWS.find((r) => r.which === which)
    if (!row) return
    setModelDraft(which === 'vision' ? (global[row.key] ?? '') : parseModelList(global[row.key]))
    setModelEdit(which)
  }

  function toggleModelChip(name: string) {
    if (modelEdit === 'vision') {
      setModelDraft(modelDraft.trim() === name ? '' : name)
      return
    }
    const list = modelDraft
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    const index = list.indexOf(name)
    if (index >= 0) list.splice(index, 1)
    else list.push(name)
    setModelDraft(list.join(', '))
  }

  function submitModel() {
    const row = MODEL_ROWS.find((r) => r.which === modelEdit)
    if (!row) return
    setMut.mutate(
      { scope: 'global', key: row.key, value: modelDraft.trim() },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setModelEdit(null)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function openSpec(spec: Spec) {
    setSpecDraft(global[spec.key] ?? '')
    setEditingSpec(spec)
  }

  function submitSpec() {
    if (!editingSpec) return
    const val = specDraft.trim()
    if (val === '') {
      toast('请输入取值；全局设置项不回退默认，请填一个合法整数')
      return
    }
    const invalid = validateSpecValue(editingSpec, val)
    if (invalid) {
      toast(invalid)
      return
    }
    setMut.mutate(
      { scope: 'global', key: editingSpec.key, value: val },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setEditingSpec(null)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function stepSpec(delta: number) {
    if (!editingSpec) return
    const parsed = parseInt(specDraft, 10)
    const base = Number.isFinite(parsed) ? parsed : editingSpec.min
    let next = base + delta
    if (next < editingSpec.min) next = editingSpec.min
    if (editingSpec.max !== 0 && next > editingSpec.max) next = editingSpec.max
    setSpecDraft(String(next))
  }

  function submitTz() {
    const value = tzDraft.trim() || 'Asia/Shanghai'
    setMut.mutate(
      { scope: 'global', key: 'tz_name', value },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setTzOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function submitFooter() {
    setMut.mutate(
      { scope: 'global', key: 'antiad_group_footer', value: footerDraft.trim() },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setFooterOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function saveDigest() {
    digestMut.mutate(
      { action: 'save', value: digestDraft },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存')
          setDigestOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function saveFix() {
    digestMut.mutate(
      { action: 'save_fix', value: fixDraft },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已保存修正文本')
          setFixOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function runDigest() {
    runMut.mutate(
      { action: 'run' },
      {
        onSuccess: (resp) => toast(resp.note ?? '已触发重新总结'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const editingIsToggle = editingSpec !== null && controlKind(editingSpec) === 'toggle'
  const editingIsMinutes = editingSpec !== null && editingSpec.key.endsWith('_minutes')
  const stepSize = editingSpec !== null && editingSpec.key.endsWith('_ms') ? 100 : 1
  const presetSelected = (value: number) => {
    if (specDraft.trim() === '') return false
    const n = Number(specDraft)
    return Number.isFinite(n) && n === value
  }

  function specRow(spec: Spec) {
    const raw = global[spec.key] ?? ''
    if (controlKind(spec) === 'toggle') {
      return (
        <SwitchRow
          key={spec.key}
          primary={spec.label}
          secondary={specHint(spec)}
          checked={raw === '1'}
          disabled={toggleMut.isPending}
          onChange={(next) => toggleKey(spec.key, next)}
        />
      )
    }
    return (
      <ListRow
        key={spec.key}
        primary={spec.label}
        secondary={specHint(spec)}
        value={raw === '' ? '未设置' : specValueText(raw, spec)}
        trailing={
          <Button size="small" onClick={() => openSpec(spec)} sx={{ minWidth: 0 }}>
            编辑
          </Button>
        }
      />
    )
  }

  const modelDisplay = (which: ModelWhich, key: string): string => {
    if (which === 'vision') return global[key] || '未设置'
    return parseModelList(global[key]) || '未设置'
  }

  const editingModelRow = MODEL_ROWS.find((r) => r.which === modelEdit)

  return (
    <Box data-testid="settings-page">
      <SectionCard title="总开关">
        {MASTER_TOGGLES.map((t) => (
          <SwitchRow
            key={t.key}
            primary={t.label}
            secondary={t.hint}
            checked={global[t.key] === '1'}
            disabled={toggleMut.isPending}
            onChange={(next) => toggleKey(t.key, next)}
          />
        ))}
      </SectionCard>

      <SectionCard title="默认模型">
        {MODEL_ROWS.map((row) => (
          <SettingRow
            key={row.key}
            label={row.label}
            hint={row.hint}
            value={modelDisplay(row.which, row.key)}
            onClick={() => openModel(row.which)}
          />
        ))}
        <Box sx={{ px: 2, pb: 1.5 }}>
          <Typography sx={{ fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
            按重试顺序排列；必须是「模型定价」里已登记且启用的模型。
          </Typography>
        </Box>
      </SectionCard>

      <Typography
        component="h2"
        sx={{ fontSize: 13, fontWeight: 500, color: 'text.secondary', mb: 0.75, pl: 1 }}
      >
        全局参数（各 bot 未覆盖时使用）
      </Typography>
      {data.sections.map((sec) => (
        <SettingsSection
          key={sec.name}
          name={sec.name}
          configured={sec.specs.filter((sp) => (global[sp.key] ?? '') !== '').length}
        >
          {sec.specs.map((sp) => specRow(sp))}
        </SettingsSection>
      ))}

      <SectionCard title="展示时区">
        <SettingRow
          label="展示时区"
          hint="所有时间按它显示；IANA 时区名"
          value={global.tz_name || 'Asia/Shanghai'}
          onClick={() => {
            setTzDraft(global.tz_name || 'Asia/Shanghai')
            setTzOpen(true)
          }}
        />
      </SectionCard>

      <SectionCard title="群内提示附加链接">
        <SettingRow
          label="附加文本"
          hint="原样附在群内告警与进群限制通知的末尾；留空 = 不附加"
          value={global.antiad_group_footer || '未设置'}
          onClick={() => {
            setFooterDraft(global.antiad_group_footer ?? '')
            setFooterOpen(true)
          }}
        />
      </SectionCard>

      <SectionCard title="形态摘要">
        <Box sx={{ px: 2, py: 1.5 }}>
          <Typography
            sx={{
              fontSize: 14,
              lineHeight: 1.7,
              whiteSpace: 'pre-wrap',
              color: data.digest ? undefined : 'text.secondary',
            }}
          >
            {data.digest || '（还没有摘要；点「立即重新总结」生成）'}
          </Typography>
          <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
            <Button
              variant="outlined"
              size="small"
              onClick={() => {
                setDigestDraft(data.digest ?? '')
                setDigestOpen(true)
              }}
            >
              编辑摘要
            </Button>
            <Button
              variant="outlined"
              size="small"
              disabled={runMut.isPending}
              onClick={runDigest}
            >
              立即重新总结
            </Button>
          </Box>
          <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
            摘要随每条群消息发给判定模型；越长越准也越贵。
          </Typography>
        </Box>
      </SectionCard>

      <SectionCard title="修正文本">
        <Box sx={{ px: 2, py: 1.5 }}>
          <Typography
            sx={{
              fontSize: 14,
              lineHeight: 1.7,
              whiteSpace: 'pre-wrap',
              color: data.digest_fix ? undefined : 'text.secondary',
            }}
          >
            {data.digest_fix || '（未设置）'}
          </Typography>
          <Button
            variant="outlined"
            size="small"
            sx={{ mt: 1.5 }}
            onClick={() => {
              setFixDraft(data.digest_fix ?? '')
              setFixOpen(true)
            }}
          >
            编辑修正文本
          </Button>
          <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
            写给总结模型的口径说明，每轮重新总结都会附上它；不改摘要正文，想让它立刻生效请点摘要卡的「立即重新总结」。
          </Typography>
        </Box>
      </SectionCard>

      <FormDrawer
        open={modelEdit !== null}
        onClose={() => setModelEdit(null)}
        title={editingModelRow?.label ?? '默认模型'}
        pending={setMut.isPending}
        submitText="保存"
        onSubmit={submitModel}
      >
        {modelEdit === 'vision' ? (
          <TextField
            fullWidth
            size="small"
            label="模型名"
            placeholder="如 demo/gpt-5-mini"
            value={modelDraft}
            onChange={(event) => setModelDraft(event.target.value)}
            helperText="留空 = 图片与贴纸不判"
          />
        ) : (
          <TextField
            fullWidth
            multiline
            minRows={2}
            size="small"
            label="模型列表（逗号分隔，按重试顺序）"
            placeholder="如 demo/gpt-5-mini, demo/gpt-5"
            value={modelDraft}
            onChange={(event) => setModelDraft(event.target.value)}
            helperText="留空 = 清空，跟随内置默认"
          />
        )}
        {enabledModels.length > 0 ? (
          <>
            <Typography sx={{ mt: 1.5, fontSize: 13, color: 'text.secondary' }}>
              已登记且启用的模型（点选加入/移出）
            </Typography>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 0.75 }}>
              {enabledModels.map((m) => {
                const picked =
                  modelEdit === 'vision'
                    ? modelDraft.trim() === m.name
                    : modelDraft
                        .split(',')
                        .map((s) => s.trim())
                        .includes(m.name)
                return (
                  <Chip
                    key={m.name}
                    size="small"
                    label={m.name}
                    color={picked ? 'primary' : 'default'}
                    variant={picked ? 'filled' : 'outlined'}
                    onClick={() => toggleModelChip(m.name)}
                  />
                )
              })}
            </Box>
          </>
        ) : (
          <Typography sx={{ mt: 1.5, fontSize: 13, color: 'text.secondary' }}>
            「模型定价」里还没有启用的模型，先去登记。
          </Typography>
        )}
      </FormDrawer>

      <FormDrawer
        open={editingSpec !== null}
        onClose={() => setEditingSpec(null)}
        title={editingSpec ? `设置：${editingSpec.label}` : ''}
        pending={setMut.isPending}
        submitText="保存"
        onSubmit={submitSpec}
      >
        {editingSpec !== null && (
          <>
            {editingIsToggle ? (
              <SwitchRow
                primary={editingSpec.label}
                secondary={specHint(editingSpec)}
                checked={specDraft === '1'}
                onChange={(next) => setSpecDraft(next ? '1' : '0')}
              />
            ) : (
              <>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                  <IconButton aria-label="减一" onClick={() => stepSpec(-stepSize)}>
                    <RemoveCircleOutline />
                  </IconButton>
                  <TextField
                    fullWidth
                    size="small"
                    label={editingSpec.label}
                    value={specDraft}
                    onChange={(event) => setSpecDraft(event.target.value)}
                    slotProps={{ htmlInput: { inputMode: 'numeric' } }}
                    helperText={specHint(editingSpec)}
                  />
                  <IconButton aria-label="加一" onClick={() => stepSpec(stepSize)}>
                    <AddCircleOutline />
                  </IconButton>
                </Box>
                {editingIsMinutes && minutePresets(editingSpec).length > 0 && (
                  <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 1.5 }}>
                    {minutePresets(editingSpec).map((preset) => {
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
                          onClick={() => setSpecDraft(String(preset.value))}
                        />
                      )
                    })}
                  </Box>
                )}
                <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary' }}>
                  {specDraft.trim() === ''
                    ? '请输入取值；全局设置项不支持留空恢复默认。'
                    : `生效值：${specValueText(specDraft, editingSpec)}`}
                </Typography>
              </>
            )}
          </>
        )}
      </FormDrawer>

      <FormDrawer
        open={tzOpen}
        onClose={() => setTzOpen(false)}
        title="展示时区"
        pending={setMut.isPending}
        submitText="保存"
        onSubmit={submitTz}
      >
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75 }}>
          {TIMEZONES.map((tz) => (
            <Chip
              key={tz}
              size="small"
              label={tz}
              color={tzDraft.trim() === tz ? 'primary' : 'default'}
              variant={tzDraft.trim() === tz ? 'filled' : 'outlined'}
              onClick={() => setTzDraft(tz)}
            />
          ))}
        </Box>
        <TextField
          fullWidth
          size="small"
          label="自定义 IANA 时区"
          placeholder="如 Asia/Shanghai"
          value={tzDraft}
          onChange={(event) => setTzDraft(event.target.value)}
          sx={{ mt: 1.5 }}
          helperText="留空 = 恢复默认 Asia/Shanghai"
        />
      </FormDrawer>

      <FormDrawer
        open={footerOpen}
        onClose={() => setFooterOpen(false)}
        title="群内提示附加链接"
        pending={setMut.isPending}
        submitText="保存"
        onSubmit={submitFooter}
      >
        <TextField
          fullWidth
          multiline
          minRows={3}
          size="small"
          label="附加文本"
          placeholder="② 使用指南 (https://t.me/your_link)"
          value={footerDraft}
          onChange={(event) => setFooterDraft(event.target.value)}
          helperText="留空 = 不附加；上限 300 字"
        />
      </FormDrawer>

      <FormDrawer
        open={digestOpen}
        onClose={() => setDigestOpen(false)}
        title="编辑形态摘要"
        pending={digestMut.isPending}
        submitText="保存摘要"
        onSubmit={saveDigest}
      >
        <TextField
          fullWidth
          multiline
          minRows={5}
          size="small"
          label="摘要正文"
          value={digestDraft}
          onChange={(event) => setDigestDraft(event.target.value)}
          helperText="随每条群消息发给判定模型；越长越准也越贵"
        />
      </FormDrawer>

      <FormDrawer
        open={fixOpen}
        onClose={() => setFixOpen(false)}
        title="编辑修正文本"
        pending={digestMut.isPending}
        submitText="保存修正文本"
        onSubmit={saveFix}
      >
        <TextField
          fullWidth
          multiline
          minRows={5}
          size="small"
          label="修正文本"
          placeholder="例：技术讨论里出现的 GitHub、npm 链接不算广告；兼职招募一律按诈骗归类。"
          value={fixDraft}
          onChange={(event) => setFixDraft(event.target.value)}
        />
      </FormDrawer>
    </Box>
  )
}
