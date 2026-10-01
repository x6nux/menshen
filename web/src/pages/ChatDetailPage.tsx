// 群组详情（计划 4.3 / 5.1 迁移不变量）：
// - 三个开关（启用/演练/群内展示）乐观更新失败回滚；
// - 处罚方式 Select + 「实际执行」行 + 防错提示（称呼语时长用 muteOptLabel）；
// - 补全历史入群时间（成功 toast 用服务端 note）；危险区移除该群。
import FolderOpenOutlined from '@mui/icons-material/FolderOpenOutlined'
import { Box, Button, MenuItem, TextField, Typography } from '@mui/material'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useChatMutation, useOptimisticMiniMutation } from '../api/mutations'
import type { Chat, State } from '../api/types'
import { muteOptLabel, punishLabel } from '../lib/format'
import { useNav } from '../nav'
import {
  EmptyState,
  ErrorState,
  ListRow,
  SectionCard,
  Skeletons,
  SwitchRow,
  useConfirm,
  useToast,
} from '../ui'
import { ChatStatusBadge } from './shared'

export function ChatDetailPage({ botId, chatId }: { botId: number; chatId: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)

  const switchMut = useOptimisticMiniMutation('chat')
  const punishMut = useChatMutation()
  const backfillMut = useChatMutation()
  const removeMut = useChatMutation()

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const chat = state.data.chats.find((c) => c.bot_id === botId && c.chat_id === chatId)
  if (!chat) {
    return <EmptyState title="群不存在或无权管理" description="它可能已被移除，或机器人归属已改派。" />
  }

  const bot = state.data.bots.find((b) => b.bot_id === botId)
  const effectivePunish = punishLabel(
    state.data.bot_settings,
    state.data.global_defaults,
    botId,
    chat.punish,
  )

  const applyChat = (patch: Partial<Chat>) => {
    return (s: State): State => ({
      ...s,
      chats: s.chats.map((c) =>
        c.bot_id === botId && c.chat_id === chatId ? { ...c, ...patch } : c,
      ),
    })
  }

  const toggleField = (patch: Partial<Chat>) => {
    switchMut.mutate(
      {
        body: { action: 'update', bot_id: botId, chat_id: chatId, ...patch },
        apply: applyChat(patch),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? '已保存'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const changePunish = (next: number) => {
    punishMut.mutate(
      { action: 'update', bot_id: botId, chat_id: chatId, punish: next },
      {
        onSuccess: (resp) => toast(resp.note ?? '已保存'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const backfill = () => {
    backfillMut.mutate(
      { action: 'backfill', bot_id: botId, chat_id: chatId },
      {
        onSuccess: (resp) =>
          toast(resp.note ?? '已开始补全，结果会私聊发给管理员（大群可能要几分钟）'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const removeChat = async () => {
    const ok = await confirm({
      title: '移除该群？',
      description: `将从「${chat.title || chat.chat_id}」停止判定与处置，配置一并删除。此操作不可恢复。`,
      confirmText: '移除',
      danger: true,
    })
    if (!ok) return
    removeMut.mutate(
      { action: 'remove', bot_id: botId, chat_id: chatId },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已移除')
          nav.pop()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="chat-detail-page">
      <SectionCard>
        <ListRow
          primary={chat.title || String(chat.chat_id)}
          secondary={`chat_id ${chat.chat_id} · ${bot ? bot.label : `bot ${botId}`}`}
          badge={<ChatStatusBadge chat={chat} />}
        />
        <SwitchRow
          primary="启用判定"
          secondary="关闭后本群完全不再处理消息"
          checked={chat.enabled}
          disabled={switchMut.isPending}
          onChange={(next) => toggleField({ enabled: next })}
        />
        <SwitchRow
          primary="演练（只记不罚）"
          secondary="判定照常记录，但不实际禁言/封禁"
          checked={chat.dryrun}
          disabled={switchMut.isPending}
          onChange={(next) => toggleField({ dryrun: next })}
        />
        <SwitchRow
          primary="群内展示判定结果"
          secondary="命中时在群里回一条提示"
          checked={chat.group_alert}
          disabled={switchMut.isPending}
          onChange={(next) => toggleField({ group_alert: next })}
        />
      </SectionCard>

      <SectionCard title="处罚">
        <Box sx={{ px: 2, pt: 1.5 }}>
          <TextField
            select
            fullWidth
            size="small"
            label="处罚方式"
            value={String(chat.punish)}
            disabled={punishMut.isPending}
            onChange={(event) => changePunish(Number(event.target.value))}
          >
            <MenuItem value="-1">跟随 bot 设置</MenuItem>
            <MenuItem value="0">
              {muteOptLabel(state.data.bot_settings, state.data.global_defaults, botId)}
            </MenuItem>
            <MenuItem value="1">封禁出群（永久）</MenuItem>
          </TextField>
        </Box>
        <ListRow
          primary="实际执行"
          value={<span data-testid="effective-punish">{effectivePunish}</span>}
        />
        {effectivePunish.startsWith('禁言') && (
          <Typography sx={{ px: 2, pb: 1.5, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
            要改成永久禁言：把本 bot 的「禁言时长（分钟）」设为 0；要踢出群就选「封禁出群」。
          </Typography>
        )}
      </SectionCard>

      <SectionCard title="工具">
        <Box sx={{ px: 2, py: 1.5 }}>
          <Button
            fullWidth
            variant="outlined"
            startIcon={<FolderOpenOutlined />}
            loading={backfillMut.isPending}
            disabled={backfillMut.isPending}
            onClick={backfill}
          >
            补全历史入群时间
          </Button>
          <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
            Bot API 没有入群时间字段，这里用该 bot 的 token 走 MTProto 把「入群时就在群里」的那批成员补上（只填未知的，不覆盖实时记录）。新入群一直是自动记的。
          </Typography>
        </Box>
      </SectionCard>

      <SectionCard title="危险区">
        <ListRow
          primary="移除该群"
          secondary="判定与处置立即停止，配置一并删除"
          disabled={removeMut.isPending}
          onClick={() => void removeChat()}
          sx={{ color: 'error.main' }}
        />
      </SectionCard>
    </Box>
  )
}
