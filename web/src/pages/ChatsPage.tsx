// 群组列表（计划 4.3）：本地搜索 + intent 过滤条 + 添加群抽屉 +
// 「管理」批量模式（一次只允许同一 bot，底部操作条走 chat.bulk_update）。
import Add from '@mui/icons-material/Add'
import Close from '@mui/icons-material/Close'
import {
  Box,
  Button,
  Checkbox,
  Chip,
  IconButton,
  MenuItem,
  TextField,
  Typography,
} from '@mui/material'
import { useState } from 'react'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useChatMutation } from '../api/mutations'
import type { Chat } from '../api/types'
import { filterRows } from '../lib/filters'
import { muteOptLabel } from '../lib/format'
import { useNav } from '../nav'
import {
  EmptyState,
  ErrorState,
  FormDrawer,
  ListRow,
  SearchField,
  SectionCard,
  Skeletons,
  useToast,
} from '../ui'
import { ChatStatusBadge } from './shared'

function chatKey(c: Pick<Chat, 'bot_id' | 'chat_id'>): string {
  return `${c.bot_id}:${c.chat_id}`
}

export function ChatsPage() {
  const nav = useNav()
  const toast = useToast()
  const state = useMiniState(true)
  const addMut = useChatMutation()
  const bulkMut = useChatMutation()

  const [q, setQ] = useState('')
  const [batch, setBatch] = useState(false)
  const [selected, setSelected] = useState<Chat[]>([])
  const [addOpen, setAddOpen] = useState(false)
  const [addBotID, setAddBotID] = useState(0)
  const [addChatID, setAddChatID] = useState('')
  const [punishOpen, setPunishOpen] = useState(false)

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const { chats, bots } = state.data
  const botLabel = (botID: number) => bots.find((b) => b.bot_id === botID)?.label ?? `bot ${botID}`

  const intent = nav.intent
  let intentLabel = ''
  let intentChats = chats
  if (intent === 'dryrun') {
    intentLabel = '演练中的群'
    intentChats = chats.filter((c) => c.dryrun)
  } else if (intent?.startsWith('bot:')) {
    const botID = Number(intent.slice(4))
    intentLabel = `${botLabel(botID)} 的群`
    intentChats = chats.filter((c) => c.bot_id === botID)
  }
  const visible = filterRows(intentChats, q, (c) => `${c.title} ${c.chat_id} ${botLabel(c.bot_id)}`)

  function openAdd() {
    setAddBotID(bots[0]?.bot_id ?? 0)
    setAddChatID('')
    setAddOpen(true)
  }

  function submitAdd() {
    addMut.mutate(
      { bot_id: addBotID, chat_id: addChatID.trim(), action: 'add' },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已添加（默认演练）')
          setAddOpen(false)
          setAddChatID('')
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function toggleSelect(chat: Chat) {
    const exists = selected.some((c) => chatKey(c) === chatKey(chat))
    if (exists) {
      setSelected(selected.filter((c) => chatKey(c) !== chatKey(chat)))
      return
    }
    if (selected.length > 0 && selected[0].bot_id !== chat.bot_id) {
      toast('一次只能批量管理同一个机器人的群，请先取消已选中的群')
      return
    }
    setSelected([...selected, chat])
  }

  function exitBatch() {
    setBatch(false)
    setSelected([])
  }

  function bulkUpdate(fields: Record<string, unknown>) {
    if (selected.length === 0) return
    bulkMut.mutate(
      {
        action: 'bulk_update',
        bot_id: selected[0].bot_id,
        chat_ids: selected.map((c) => c.chat_id),
        fields,
      },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已更新')
          exitBatch()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  const punishOptions = [
    { value: -1, label: '跟随 bot 设置' },
    {
      value: 0,
      label: `当前禁言档（${selected.length > 0 ? muteOptLabel(state.data.bot_settings, state.data.global_defaults, selected[0].bot_id) : '—'}）`,
    },
    { value: 1, label: '封禁出群（永久）' },
  ]

  return (
    <Box data-testid="chats-page" sx={{ pb: batch ? '72px' : 0 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, mb: 1 }}>
        <SearchField
          value={q}
          onChange={setQ}
          placeholder="搜索群名 / chat_id / 机器人"
          ariaLabel="搜索群组"
          sx={{ flex: 1 }}
        />
        <Button size="small" onClick={() => (batch ? exitBatch() : setBatch(true))}>
          {batch ? '取消' : '管理'}
        </Button>
        {!batch && (
          <IconButton aria-label="添加群" size="small" onClick={openAdd}>
            <Add />
          </IconButton>
        )}
      </Box>

      {intentLabel && (
        <Box sx={{ mb: 1, display: 'flex', alignItems: 'center', gap: 0.25 }}>
          <Chip size="small" color="primary" variant="outlined" label={`只看：${intentLabel}`} />
          <IconButton
            aria-label="清除筛选"
            size="small"
            onClick={() => nav.switchTab('chats')}
          >
            <Close sx={{ fontSize: 18 }} />
          </IconButton>
        </Box>
      )}

      {visible.length === 0 ? (
        chats.length === 0 ? (
          <EmptyState
            title="还没有接入群组"
            description="点右上角「＋」添加：把这个 bot 拉进群后填群 ID，添加后默认演练。"
            action={
              <Button variant="contained" onClick={openAdd}>
                添加群
              </Button>
            }
          />
        ) : (
          <EmptyState title="没有匹配的群" description="换个关键词，或清除筛选后重试。" />
        )
      ) : (
        <SectionCard>
          {visible.map((chat) => {
            const picked = selected.some((c) => chatKey(c) === chatKey(chat))
            return (
              <ListRow
                key={chatKey(chat)}
                primary={chat.title || String(chat.chat_id)}
                secondary={`${chat.chat_id} · ${botLabel(chat.bot_id)}`}
                badge={<ChatStatusBadge chat={chat} />}
                chevron={!batch}
                onClick={() => (batch ? toggleSelect(chat) : nav.push({ k: 'chat', botId: chat.bot_id, chatId: chat.chat_id }))}
                trailing={
                  batch ? (
                    <Checkbox
                      size="small"
                      checked={picked}
                      onChange={() => toggleSelect(chat)}
                      onClick={(event) => event.stopPropagation()}
                      slotProps={{ input: { 'aria-label': `选择 ${chat.title || chat.chat_id}` } }}
                    />
                  ) : undefined
                }
              />
            )
          })}
        </SectionCard>
      )}

      {batch && (
        <Box
          sx={{
            position: 'fixed',
            left: 0,
            right: 0,
            bottom: 'calc(56px + env(safe-area-inset-bottom))',
            bgcolor: 'background.paper',
            borderTop: '1px solid',
            borderColor: 'divider',
            px: 1,
            py: 1,
            zIndex: (theme) => theme.zIndex.appBar,
          }}
        >
          <Typography sx={{ mb: 0.75, fontSize: 12, color: 'text.secondary', textAlign: 'center' }}>
            已选 {selected.length} 个群
            {selected.length > 0 ? ` · ${botLabel(selected[0].bot_id)}` : ''}
          </Typography>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, justifyContent: 'center' }}>
            <Button
              size="small"
              variant="outlined"
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ enabled: true })}
            >
              启用
            </Button>
            <Button
              size="small"
              variant="outlined"
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ enabled: false })}
            >
              停用
            </Button>
            <Button
              size="small"
              variant="outlined"
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ dryrun: true })}
            >
              开启演练
            </Button>
            <Button
              size="small"
              variant="outlined"
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ dryrun: false })}
            >
              关闭演练
            </Button>
            <Button
              size="small"
              variant="outlined"
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => setPunishOpen(true)}
            >
              处罚方式
            </Button>
          </Box>
        </Box>
      )}

      <FormDrawer
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="添加群"
        pending={addMut.isPending}
        submitText="添加"
        submitDisabled={addBotID === 0 || addChatID.trim() === ''}
        onSubmit={submitAdd}
      >
        <TextField
          select
          fullWidth
          size="small"
          label="机器人"
          value={String(addBotID)}
          onChange={(event) => setAddBotID(Number(event.target.value))}
        >
          {bots.map((bot) => (
            <MenuItem key={bot.bot_id} value={String(bot.bot_id)}>
              {bot.label} · {bot.bot_id}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          fullWidth
          size="small"
          label="群 ID"
          placeholder="-1001234567890"
          value={addChatID}
          onChange={(event) => setAddChatID(event.target.value)}
          slotProps={{ htmlInput: { inputMode: 'numeric' } }}
          helperText="以 -100 开头的群 ID；先把机器人拉进群再加。"
          sx={{ mt: 1.5 }}
        />
        <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
          添加后默认「演练」：只记录判定结果、不处罚，确认无误后再关闭演练。
        </Typography>
      </FormDrawer>

      <FormDrawer
        open={punishOpen}
        onClose={() => setPunishOpen(false)}
        title="批量设置处罚方式"
      >
        <Typography sx={{ mb: 1, fontSize: 13, color: 'text.secondary' }}>
          将对已选的 {selected.length} 个群生效。
        </Typography>
        {punishOptions.map((option) => (
          <ListRow
            key={option.value}
            primary={option.label}
            chevron
            onClick={() => {
              setPunishOpen(false)
              bulkUpdate({ punish: option.value })
            }}
          />
        ))}
      </FormDrawer>
    </Box>
  )
}
