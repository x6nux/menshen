// 群组详情：启停、演练、群内告警、处罚方式、补录入群时间、移除。
import {
  Box,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  Typography,
} from '@mui/material'
import { useChatMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { muteOptLabel, punishLabel } from '../../lib/format'
import { chatStatus } from '../../lib/status'
import { CardBlock, ConfirmButton, InfoList, PageHeader, StatusBadge } from '../components'
import { useRunFeedback } from '../feedback'

export function ChatDetailView({ botId, chatId }: { botId: number; chatId: number }) {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const chatMut = useChatMutation()

  if (!state.data) return null
  const chat = state.data.chats.find((c) => c.bot_id === botId && c.chat_id === chatId)
  const bot = state.data.bots.find((b) => b.bot_id === botId)
  const { bot_settings, global_defaults } = state.data

  if (!chat) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>群组不存在，或不在你的管理范围内。</Typography>
      </CardBlock>
    )
  }

  const update = (fields: Record<string, unknown>, okText: string) =>
    run(chatMut.mutateAsync({ action: 'update', bot_id: botId, chat_id: chatId, ...fields }), okText)

  return (
    <>
      <PageHeader
        title={chat.title || `chat ${chat.chat_id}`}
        subtitle={`${bot?.label ?? `bot ${botId}`} · chat id ${chat.chat_id}`}
        actions={<StatusBadge info={chatStatus(chat)} />}
      />

      <CardBlock title="运行状态">
        <Box sx={{ display: 'grid', gap: 1 }}>
          <ToggleRow
            label="启用判定"
            hint="停用后该群的消息不再判定。"
            checked={chat.enabled}
            disabled={chatMut.isPending}
            onChange={(v) => update({ enabled: v }, v ? '已启用' : '已停用')}
          />
          <ToggleRow
            label="演练模式"
            hint="只落库、不处置、不发告警；观察误伤时用。"
            checked={chat.dryrun}
            disabled={chatMut.isPending}
            onChange={(v) => update({ dryrun: v }, v ? '已进入演练' : '已退出演练')}
          />
          <ToggleRow
            label="群内展示"
            hint="逐条贴出处置结果（不含昵称与原文），按人节流并自动撤回。"
            checked={chat.group_alert}
            disabled={chatMut.isPending}
            onChange={(v) => update({ group_alert: v }, v ? '已开启群内展示' : '已关闭群内展示')}
          />
        </Box>
      </CardBlock>

      <CardBlock title="处罚方式">
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'center', flexWrap: 'wrap' }}>
          <FormControl size="small" sx={{ minWidth: 240 }}>
            <InputLabel>处罚</InputLabel>
            <Select
              label="处罚"
              value={chat.punish}
              disabled={chatMut.isPending}
              onChange={(event) => update({ punish: Number(event.target.value) }, '处罚方式已更新')}
            >
              <MenuItem value={-1}>跟随机器人全局</MenuItem>
              <MenuItem value={0}>{`禁言（${muteOptLabel(bot_settings, global_defaults, botId)}）`}</MenuItem>
              <MenuItem value={1}>封禁出群（永久）</MenuItem>
            </Select>
          </FormControl>
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
            当前生效：{punishLabel(bot_settings, global_defaults, botId, chat.punish)}
          </Typography>
        </Box>
      </CardBlock>

      <CardBlock title="其他">
        <InfoList
          items={[
            { label: '机器人', value: bot?.label ?? `bot ${botId}` },
            { label: 'chat id', value: chat.chat_id },
          ]}
        />
        <Box sx={{ display: 'flex', gap: 1, mt: 2, flexWrap: 'wrap' }}>
          <Button
            variant="outlined"
            disabled={chatMut.isPending}
            onClick={() => run(chatMut.mutateAsync({ action: 'backfill', bot_id: botId, chat_id: chatId }), '已开始补录')}
          >
            补录入群时间
          </Button>
          <ConfirmButton
            variant="outlined"
            color="error"
            title="移除群组"
            description="移除后该群不再判定，历史记录保留。"
            confirmLabel="移除"
            danger
            disabled={chatMut.isPending}
            onConfirm={() =>
              run(chatMut.mutateAsync({ action: 'remove', bot_id: botId, chat_id: chatId }), '已移除')
            }
          >
            移除群组
          </ConfirmButton>
        </Box>
      </CardBlock>
    </>
  )
}

function ToggleRow({
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  label: string
  hint: string
  checked: boolean
  disabled: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
      <Box sx={{ flex: 1 }}>
        <Typography sx={{ fontSize: 13.5 }}>{label}</Typography>
        <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{hint}</Typography>
      </Box>
      <Switch checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
    </Box>
  )
}
