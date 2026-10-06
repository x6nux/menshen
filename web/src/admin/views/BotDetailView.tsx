// oxlint-disable react/set-state-in-effect -- 草稿态需要跟随服务端返回的值重置（保存后刷新）。
// 机器人详情：基本信息、启用开关、归属与模型（主管理员）、逐项参数覆盖、移除。
import { useEffect, useState } from 'react'
import {
  Box,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import { useBotMutation, useSetMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { controlKind, overrideKeys, specHint, specUnit, specValueText, validateSpecValue } from '../../lib/settings'
import { botStatus } from '../../lib/status'
import { Badge } from '../../ui'
import { CardBlock, ConfirmButton, InfoList, PageHeader, StatusBadge } from '../components'
import { useRunFeedback } from '../feedback'
import type { Bot, Spec } from '../../api/types'

export function BotDetailView({ botId }: { botId: number }) {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const botMut = useBotMutation()

  if (!state.data) return null
  const bot = state.data.bots.find((b) => b.bot_id === botId)
  if (!bot) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>
          机器人不存在，或不在你的管理范围内。
        </Typography>
      </CardBlock>
    )
  }

  const main = state.data.me.main
  const chats = state.data.chats.filter((c) => c.bot_id === botId)
  const overrides = state.data.bot_settings[String(botId)] ?? {}

  return (
    <>
      <PageHeader
        title={bot.label}
        subtitle={bot.username ? `@${bot.username} · bot id ${bot.bot_id}` : `bot id ${bot.bot_id}`}
        actions={<StatusBadge info={botStatus(bot)} />}
      />

      <CardBlock title="基本信息">
        <InfoList
          items={[
            { label: '归属', value: ownerLabel(state.data.owner_opts, bot.owner_id) },
            { label: '群组数', value: `${chats.length} 个` },
            { label: '主 bot', value: bot.is_main ? '是（只做配置管理，不入群、不判定）' : '否' },
          ]}
        />
      </CardBlock>

      <CardBlock
        title="运行开关"
        actions={
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
              {bot.enabled ? '已启用' : '已停用'}
            </Typography>
            <Switch
              checked={bot.enabled}
              disabled={bot.is_main || botMut.isPending}
              onChange={(event) =>
                run(
                  botMut.mutateAsync({
                    bot_id: bot.bot_id,
                    action: event.target.checked ? 'enable' : 'disable',
                  }),
                  event.target.checked ? '已启用' : '已停用',
                )
              }
            />
          </Box>
        }
      >
        <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.8 }}>
          {bot.is_main
            ? '主 bot 的保护在服务端强制：不能停用、不能入群判定。'
            : '停用后该机器人不再接收与处理任何消息。'}
        </Typography>
      </CardBlock>

      {main && !bot.is_main && (
        <>
          <OwnerCard
            bot={bot}
            owners={state.data.owner_opts ?? []}
            disabled={botMut.isPending}
            onSave={(ownerId) =>
              run(botMut.mutateAsync({ bot_id: bot.bot_id, action: 'owner', owner_id: ownerId }))
            }
          />
          <ModelsCard
            bot={bot}
            disabled={botMut.isPending}
            models={(state.data.models ?? []).filter((m) => m.enabled).map((m) => m.name)}
            onSave={(which, value) =>
              run(botMut.mutateAsync({ bot_id: bot.bot_id, action: 'models', which, value }))
            }
          />
        </>
      )}

      <OverridesCard
        botId={bot.bot_id}
        specs={state.data.specs.filter((s) => s.group === 'antiad' || s.group === 'both')}
        overrides={overrides}
        enabled={bot.enabled}
      />

      {!bot.is_main && (
        <CardBlock title="移除机器人">
          <Typography sx={{ fontSize: 13, color: 'text.secondary', mb: 1.5, lineHeight: 1.8 }}>
            移除后该机器人立即停止工作，其 webhook 与配置一并删除。此操作不可撤销。
          </Typography>
          <ConfirmButton
            danger
            variant="outlined"
            title="移除机器人"
            description={`确认移除「${bot.label}」？它将立即停止工作。`}
            confirmLabel="移除"
            disabled={botMut.isPending}
            onConfirm={() =>
              run(botMut.mutateAsync({ bot_id: bot.bot_id, action: 'remove' }), '已移除')
            }
          >
            移除机器人
          </ConfirmButton>
        </CardBlock>
      )}
    </>
  )
}

function ownerLabel(opts: { user_id: number; label: string }[] | undefined, ownerId: number): string {
  return opts?.find((o) => o.user_id === ownerId)?.label ?? `uid ${ownerId}`
}

function OwnerCard({
  bot,
  owners,
  disabled,
  onSave,
}: {
  bot: Bot
  owners: { user_id: number; label: string }[]
  disabled: boolean
  onSave: (ownerId: number) => void
}) {
  const [owner, setOwner] = useState(bot.owner_id)
  useEffect(() => setOwner(bot.owner_id), [bot.owner_id])
  return (
    <CardBlock title="归属管理员">
      <Box sx={{ display: 'flex', gap: 1.5, alignItems: 'center', flexWrap: 'wrap' }}>
        <FormControl size="small" sx={{ minWidth: 240 }}>
          <InputLabel>归属</InputLabel>
          <Select label="归属" value={owner} onChange={(event) => setOwner(Number(event.target.value))}>
            {owners.map((o) => (
              <MenuItem key={o.user_id} value={o.user_id}>
                {o.label}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <Button variant="contained" disabled={disabled || owner === bot.owner_id} onClick={() => onSave(owner)}>
          改派
        </Button>
      </Box>
    </CardBlock>
  )
}

function ModelsCard({
  bot,
  models,
  disabled,
  onSave,
}: {
  bot: Bot
  models: string[]
  disabled: boolean
  onSave: (which: 'so' | 'llm', value: string) => void
}) {
  const [so, setSo] = useState(bot.so_models.join(', '))
  const [llm, setLlm] = useState(bot.llm_models.join(', '))
  useEffect(() => setSo(bot.so_models.join(', ')), [bot.so_models])
  useEffect(() => setLlm(bot.llm_models.join(', ')), [bot.llm_models])
  return (
    <CardBlock title="判定模型">
      <Typography sx={{ fontSize: 12.5, color: 'text.secondary', mb: 1.5, lineHeight: 1.7 }}>
        逗号分隔的有序列表，格式 <code>上游名/模型ID</code>。留空表示跟随全局默认。
        {models.length > 0 &&
          ` 已启用模型：${models.slice(0, 6).join('、')}${models.length > 6 ? '…' : ''}`}
      </Typography>
      <Box sx={{ display: 'grid', gap: 1.5 }}>
        <ModelRow label="初判（systemone）" value={so} onChange={setSo} disabled={disabled} onSave={() => onSave('so', so)} />
        <ModelRow label="复判（llm）" value={llm} onChange={setLlm} disabled={disabled} onSave={() => onSave('llm', llm)} />
      </Box>
    </CardBlock>
  )
}

function ModelRow({
  label,
  value,
  onChange,
  onSave,
  disabled,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  onSave: () => void
  disabled: boolean
}) {
  return (
    <Box sx={{ display: 'flex', gap: 1 }}>
      <TextField label={label} value={value} onChange={(event) => onChange(event.target.value)} fullWidth size="small" />
      <Button variant="outlined" disabled={disabled} onClick={onSave}>
        保存
      </Button>
    </Box>
  )
}

/** OverridesCard 只列该 bot 已显式覆盖的参数项，可编辑或恢复全局。 */
function OverridesCard({
  botId,
  specs,
  overrides,
  enabled,
}: {
  botId: number
  specs: Spec[]
  overrides: Record<string, string>
  enabled: boolean
}) {
  const setMut = useSetMutation()
  const run = useRunFeedback()
  const overridden = overrideKeys(specs, overrides)

  return (
    <CardBlock title={`参数覆盖（${overridden.length}）`}>
      {overridden.length === 0 ? (
        <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
          该机器人没有覆盖任何全局参数，全部跟随全局设置。
        </Typography>
      ) : (
        <Box sx={{ display: 'grid', gap: 1 }}>
          {overridden.map((spec) => (
            <OverrideRow
              key={spec.key}
              spec={spec}
              value={String(overrides[spec.key] ?? '')}
              disabled={setMut.isPending || !enabled}
              onCommit={(value) =>
                run(
                  setMut.mutateAsync({ scope: 'bot', bot_id: botId, key: spec.key, value }),
                  value === '' ? '已恢复全局' : '已保存',
                )
              }
            />
          ))}
        </Box>
      )}
    </CardBlock>
  )
}

function OverrideRow({
  spec,
  value,
  disabled,
  onCommit,
}: {
  spec: Spec
  value: string
  disabled: boolean
  onCommit: (value: string) => void
}) {
  const [draft, setDraft] = useState(value)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    setDraft(value)
    setError(null)
  }, [value])

  const kind = controlKind(spec)
  const commit = (next: string) => {
    const err = validateSpecValue(spec, next)
    if (err) {
      setError(err)
      return
    }
    setError(null)
    if (next.trim() === value) return
    onCommit(next.trim())
  }

  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, flexWrap: 'wrap' }}>
      <Box sx={{ minWidth: 220, flex: 1 }}>
        <Typography sx={{ fontSize: 13.5 }}>{spec.label}</Typography>
        <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{specHint(spec)}</Typography>
      </Box>
      {kind === 'toggle' ? (
        <Switch
          checked={draft === '1'}
          disabled={disabled}
          onChange={(event) => commit(event.target.checked ? '1' : '0')}
        />
      ) : (
        <TextField
          value={draft}
          disabled={disabled}
          error={error !== null}
          helperText={error ?? (draft === '' ? '' : specValueText(draft, spec))}
          sx={{ width: 240 }}
          size="small"
          slotProps={{
            input: {
              endAdornment: specUnit(spec) ? <Badge tone="neutral">{specUnit(spec)}</Badge> : undefined,
            },
          }}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={() => commit(draft)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') commit(draft)
          }}
        />
      )}
      <Button size="small" variant="text" disabled={disabled} onClick={() => onCommit('')}>
        恢复全局
      </Button>
    </Box>
  )
}
