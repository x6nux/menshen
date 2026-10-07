// 群组列表：本地搜索 + intent 过滤条 + 添加群抽屉 +
// `管理` 批量模式（一次只允许同一 bot，底部操作条走 chat.bulk_update）。
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
import { useEffect, useRef, useState } from 'react'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useChatMutation } from '../api/mutations'
import type { Bot, Chat } from '../api/types'
import { filterRows } from '../lib/filters'
import { muteOptLabel } from '../lib/format'
import { BULK_CHAT_LIMIT, toggleChatSelection } from '../lib/selection'
import { useNav, usePageParam } from '../nav'
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

/** applyIntent 把 nav.intent 翻译成群组过滤：dryrun=演练中的群，bot:<id>=该 bot 的群。 */
function applyIntent(chats: Chat[], intent: string | null): Chat[] {
  if (intent === 'dryrun') return chats.filter((c) => c.dryrun)
  if (intent?.startsWith('bot:')) {
    const botID = Number(intent.slice(4))
    return chats.filter((c) => c.bot_id === botID)
  }
  return chats
}

/** botLabelOf 按 bot_id 找展示名，找不到回退 bot <id>。 */
function botLabelOf(bots: Bot[], botID: number): string {
  return bots.find((b) => b.bot_id === botID)?.label ?? `bot ${botID}`
}

/** 批量操作条未测到高度前的兜底预留（窄屏换行时约 135px，取 140 更稳）。 */
const BATCH_BAR_MIN_RESERVE = 140

const BULK_BUTTON_SX = { minWidth: 0, minHeight: 32, px: 1.25, fontSize: 13 }

export function ChatsPage({ bulkLimit = BULK_CHAT_LIMIT }: { bulkLimit?: number } = {}) {
  const nav = useNav()
  const toast = useToast()
  const state = useMiniState(true)
  const addMut = useChatMutation()
  const bulkMut = useChatMutation()

  const [q, setQ] = usePageParam('q')
  const [batch, setBatch] = useState(false)
  const [selected, setSelected] = useState<Chat[]>([])
  const [addOpen, setAddOpen] = useState(false)
  const [addBotID, setAddBotID] = useState(0)
  const [addChatID, setAddChatID] = useState('')
  const [punishOpen, setPunishOpen] = useState(false)

  // 批量操作条实测高度：窄屏按钮换行时动态给列表留出底部空间，避免压住最后一行。
  // ResizeObserver 首次 observe 会立即回调一次；没有 RO 的环境用兜底预留值。
  const barRef = useRef<HTMLDivElement | null>(null)
  const [barHeight, setBarHeight] = useState(0)
  useEffect(() => {
    if (!batch) return
    const el = barRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver((entries) => {
      const h = entries[0]?.contentRect.height ?? 0
      if (h > 0) setBarHeight(h)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [batch])

  const intent = nav.intent
  const filterSignature = `${intent ?? ''}|${q.trim().toLowerCase()}`
  const data = state.data
  const [prunedFor, setPrunedFor] = useState<string | null>(null)

  // 过滤条件（搜索词/意图）变化时清理不可见的选中项，避免误改被藏起来的群。
  // 用渲染期调整状态的官方模式：不引入 effect 的额外一轮渲染。
  if (data && prunedFor !== filterSignature) {
    setPrunedFor(filterSignature)
    const keys = new Set(
      filterRows(applyIntent(data.chats, intent), q, (c) =>
        `${c.title} ${c.chat_id} ${botLabelOf(data.bots, c.bot_id)}`,
      ).map(chatKey),
    )
    setSelected((prev) => {
      const kept = prev.filter((c) => keys.has(chatKey(c)))
      return kept.length === prev.length ? prev : kept
    })
  }

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const { chats, bots } = state.data
  const botLabel = (botID: number) => botLabelOf(bots, botID)

  let intentLabel = ''
  if (intent === 'dryrun') intentLabel = '演练中的群'
  else if (intent?.startsWith('bot:')) intentLabel = `${botLabel(Number(intent.slice(4)))} 的群`
  const intentChats = applyIntent(chats, intent)
  const visible = filterRows(intentChats, q, (c) => `${c.title} ${c.chat_id} ${botLabel(c.bot_id)}`)
  const reserved = batch ? Math.max(barHeight, BATCH_BAR_MIN_RESERVE) : 0

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
    const outcome = toggleChatSelection(
      selected,
      chat,
      (a, b) => chatKey(a) === chatKey(b),
      (c) => c.bot_id,
      bulkLimit,
    )
    if (outcome.rejected === 'bot') {
      toast('一次只能批量管理同一个机器人的群，请先取消已选中的群')
      return
    }
    if (outcome.rejected === 'limit') {
      toast(`一次最多批量管理 ${bulkLimit} 个群`)
      return
    }
    setSelected(outcome.selected)
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
    <Box
      data-testid="chats-page"
      data-reserved={reserved}
      sx={{
        // Shell 已为 TabBar 留了 72px；这里补批量操作条实测高度（未测到时用兜底值）。
        pb: batch ? `calc(${reserved}px + env(safe-area-inset-bottom))` : 0,
      }}
    >
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
          ref={barRef}
          data-testid="batch-bar"
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
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5, justifyContent: 'center' }}>
            <Button
              size="small"
              variant="outlined"
              sx={BULK_BUTTON_SX}
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ enabled: true })}
            >
              启用
            </Button>
            <Button
              size="small"
              variant="outlined"
              sx={BULK_BUTTON_SX}
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ enabled: false })}
            >
              停用
            </Button>
            <Button
              size="small"
              variant="outlined"
              sx={BULK_BUTTON_SX}
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ dryrun: true })}
            >
              开启演练
            </Button>
            <Button
              size="small"
              variant="outlined"
              sx={BULK_BUTTON_SX}
              disabled={selected.length === 0 || bulkMut.isPending}
              onClick={() => bulkUpdate({ dryrun: false })}
            >
              关闭演练
            </Button>
            <Button
              size="small"
              variant="outlined"
              sx={BULK_BUTTON_SX}
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
