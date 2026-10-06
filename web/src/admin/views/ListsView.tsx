// 名单管理：白名单 / 资料放行 / 联合封禁（全局 + 专属）/ 次级管理员。
import { useState } from 'react'
import {
  Box,
  Button,
  Checkbox,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Tab,
  Tabs,
  TextField,
  Typography,
} from '@mui/material'
import { useAdminMutation, useGbanMutation, useGbanOwnMutation, useWhitelistMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { filterRows, gbanKey, profileOKKey, whiteKey } from '../../lib/filters'
import { fmtTSFull, whiteSourceLabel } from '../../lib/format'
import { SearchField } from '../../ui'
import { CardBlock, DataTable, PageHeader, Toolbar } from '../components'
import type { Column } from '../components'
import { useRunFeedback } from '../feedback'
import type { AdminRow, GbanRow, ProfileOKRow, WhiteRow } from '../../api/types'

type Seg = 'white' | 'profile' | 'gban' | 'admins'

export function ListsView({ section, main }: { section?: string; main: boolean }) {
  const state = useMiniState(true)
  const [seg, setSeg] = useState<Seg>(() => normalizeSeg(section, main))

  if (!state.data) return null
  const effective = main ? seg : 'gban'

  return (
    <>
      <PageHeader title="名单管理" subtitle="白名单、资料放行、联合封禁与次级管理员的集中入口" />
      {main && (
        <Tabs
          value={effective}
          onChange={(_event, next: Seg) => setSeg(next)}
          sx={{ mb: 1.5, minHeight: 36, '& .MuiTab-root': { minHeight: 36, fontSize: 14 } }}
        >
          <Tab value="white" label="白名单" />
          <Tab value="profile" label="资料放行" />
          <Tab value="gban" label="联合封禁" />
          <Tab value="admins" label="次级管理员" />
        </Tabs>
      )}
      {effective === 'white' && <WhitelistSection />}
      {effective === 'profile' && <ProfileSection />}
      {effective === 'gban' && <GbanSection />}
      {effective === 'admins' && <AdminsSection />}
    </>
  )
}

function normalizeSeg(section: string | undefined, main: boolean): Seg {
  if (!main) return 'gban'
  if (section === 'white' || section === 'profile' || section === 'gban' || section === 'admins') return section
  return 'white'
}

function WhitelistSection() {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const mut = useWhitelistMutation()
  const [q, setQ] = useState('')
  const [botId, setBotId] = useState(0)
  const [chatId, setChatId] = useState('0')
  const [userId, setUserId] = useState('')
  const [hours, setHours] = useState(24)

  if (!state.data) return null
  const { whitelist, bots, tz_name } = state.data
  const rows = filterRows(whitelist, q, whiteKey)
  const botLabel = (id: number) => (id === 0 ? '全平台' : (bots.find((b) => b.bot_id === id)?.label ?? `bot ${id}`))
  const scope = (row: WhiteRow) =>
    row.bot_id === 0 ? '全平台' : row.chat_id === 0 ? `${botLabel(row.bot_id)} 下所有群` : `${botLabel(row.bot_id)} · ${row.chat_id}`

  const columns: Column<WhiteRow>[] = [
    { key: 'user', header: '用户', width: 120, render: (row) => `uid ${row.user_id}` },
    { key: 'scope', header: '范围', render: (row) => scope(row) },
    { key: 'source', header: '来源', width: 120, render: (row) => whiteSourceLabel(row.source) },
    { key: 'by', header: '操作人', width: 110, render: (row) => (row.by_uid ? `uid ${row.by_uid}` : '—') },
    {
      key: 'expires',
      header: '到期',
      width: 170,
      render: (row) => (row.expires_at > 0 ? fmtTSFull(row.expires_at, tz_name) : '永久'),
    },
    {
      key: 'actions',
      header: '',
      width: 80,
      render: (row) => (
        <Button
          size="small"
          color="error"
          variant="text"
          disabled={mut.isPending}
          onClick={() =>
            run(
              mut.mutateAsync({ action: 'remove', bot_id: row.bot_id, chat_id: row.chat_id, user_id: row.user_id }),
              '已移除',
            )
          }
        >
          移除
        </Button>
      ),
    },
  ]

  const valid = /^\d+$/.test(userId.trim()) && /^-?\d+$/.test(chatId.trim())

  return (
    <>
      <CardBlock title="加入白名单">
        <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'wrap', alignItems: 'center' }}>
          <FormControl size="small" sx={{ minWidth: 180 }}>
            <InputLabel>机器人</InputLabel>
            <Select label="机器人" value={botId} onChange={(e) => setBotId(Number(e.target.value))}>
              <MenuItem value={0}>全平台</MenuItem>
              {bots.map((b) => (
                <MenuItem key={b.bot_id} value={b.bot_id}>
                  {b.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <TextField
            label="chat id（0=该 bot 所有群）"
            value={chatId}
            onChange={(e) => setChatId(e.target.value)}
            sx={{ width: 220 }}
          />
          <TextField label="用户 uid" value={userId} onChange={(e) => setUserId(e.target.value)} sx={{ width: 160 }} />
          <FormControl size="small" sx={{ minWidth: 130 }}>
            <InputLabel>时长</InputLabel>
            <Select label="时长" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
              <MenuItem value={24}>24 小时</MenuItem>
              <MenuItem value={168}>7 天</MenuItem>
              <MenuItem value={0}>永久</MenuItem>
            </Select>
          </FormControl>
          <Button
            variant="contained"
            disabled={!valid || mut.isPending}
            onClick={() => {
              run(
                mut.mutateAsync({
                  action: 'add',
                  bot_id: botId,
                  chat_id: Number(chatId),
                  user_id: userId.trim(),
                  hours,
                }),
                '已加入白名单',
              )
              setUserId('')
            }}
          >
            加入
          </Button>
        </Box>
      </CardBlock>

      <Toolbar>
        <SearchField value={q} onChange={setQ} placeholder="搜索用户 / 来源 / 范围" sx={{ maxWidth: 320 }} />
      </Toolbar>
      <DataTable
        rows={rows}
        rowKey={(row) => `${row.bot_id}:${row.chat_id}:${row.user_id}`}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>白名单为空。</Typography>}
        columns={columns}
      />
    </>
  )
}

function ProfileSection() {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const mut = useWhitelistMutation()
  const [q, setQ] = useState('')
  if (!state.data) return null
  const { profile_ok, bots, tz_name } = state.data
  const botLabel = (id: number) => bots.find((b) => b.bot_id === id)?.label ?? `bot ${id}`
  const rows = filterRows(profile_ok, q, (row) => profileOKKey(row, botLabel))

  const columns: Column<ProfileOKRow>[] = [
    { key: 'user', header: '用户', width: 120, render: (row) => `uid ${row.user_id}` },
    { key: 'bot', header: '机器人', width: 160, render: (row) => botLabel(row.bot_id) },
    { key: 'hours', header: '放行时长', width: 100, render: (row) => `${row.hours} 小时` },
    { key: 'reason', header: '原因', render: (row) => row.reason || '—' },
    { key: 'expires', header: '到期', width: 170, render: (row) => fmtTSFull(row.expires_at, tz_name) },
    {
      key: 'actions',
      header: '',
      width: 90,
      render: (row) => (
        <Button
          size="small"
          color="error"
          variant="text"
          disabled={mut.isPending}
          onClick={() => run(mut.mutateAsync({ action: 'unprofile', bot_id: row.bot_id, user_id: row.user_id }), '已撤销')}
        >
          撤销
        </Button>
      ),
    },
  ]

  return (
    <>
      <Toolbar>
        <SearchField value={q} onChange={setQ} placeholder="搜索用户 / 机器人" sx={{ maxWidth: 320 }} />
      </Toolbar>
      <DataTable
        rows={rows}
        rowKey={(row) => `${row.bot_id}:${row.user_id}`}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有资料放行记录。</Typography>}
        columns={columns}
      />
    </>
  )
}

function GbanSection() {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const gbanMut = useGbanMutation()
  const ownMut = useGbanOwnMutation()
  const [sub, setSub] = useState<'global' | 'own'>('global')
  const [q, setQ] = useState('')
  const [userId, setUserId] = useState('')
  const [reason, setReason] = useState('')

  if (!state.data) return null
  const { gban, gban_own, tz_name, chats, bots, me } = state.data
  const valid = /^\d+$/.test(userId.trim())
  const ownChatIDs = new Set(chats.filter((c) => bots.some((b) => b.bot_id === c.bot_id && (me.main || b.owner_id === me.uid))).map((c) => c.chat_id))

  const gbanColumns = (onRemove: (row: GbanRow) => void): Column<GbanRow>[] => [
    { key: 'user', header: '用户', width: 120, render: (row) => `uid ${row.user_id}` },
    { key: 'reason', header: '原因', render: (row) => row.reason || '—' },
    { key: 'at', header: '加入时间', width: 170, render: (row) => fmtTSFull(row.created_at, tz_name) },
    {
      key: 'actions',
      header: '',
      width: 80,
      render: (row) => (
        <Button size="small" color="error" variant="text" onClick={() => onRemove(row)}>
          移除
        </Button>
      ),
    },
  ]

  const addForm = (
    <CardBlock title="加入联合封禁">
      <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'wrap', alignItems: 'center' }}>
        <TextField label="用户 uid" value={userId} onChange={(e) => setUserId(e.target.value)} sx={{ width: 160 }} />
        <TextField label="原因" value={reason} onChange={(e) => setReason(e.target.value)} sx={{ width: 280 }} />
        <Button
          variant="contained"
          disabled={!valid || (sub === 'global' ? gbanMut.isPending : ownMut.isPending)}
          onClick={() => {
            const body = { action: 'add', user_id: userId.trim(), reason: reason.trim() }
            run(sub === 'global' ? gbanMut.mutateAsync(body) : ownMut.mutateAsync(body), '已加入联合封禁')
            setUserId('')
            setReason('')
          }}
        >
          加入{sub === 'global' ? '全局组' : '专属组'}
        </Button>
      </Box>
    </CardBlock>
  )

  return (
    <>
      <Tabs
        value={sub}
        onChange={(_event, next: 'global' | 'own') => setSub(next)}
        sx={{ mb: 1.5, minHeight: 32, '& .MuiTab-root': { minHeight: 32, fontSize: 13 } }}
      >
        <Tab value="global" label="全局组" />
        <Tab value="own" label="专属组" />
      </Tabs>

      {sub === 'global' ? (
        <>
          {addForm}
          <Toolbar>
            <SearchField value={q} onChange={setQ} placeholder="搜索用户 / 原因" sx={{ maxWidth: 320 }} />
          </Toolbar>
          <DataTable
            rows={filterRows(gban, q, gbanKey)}
            rowKey={(row) => row.user_id}
            empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>全局联合封禁名单为空。</Typography>}
            columns={gbanColumns((row) =>
              run(gbanMut.mutateAsync({ action: 'remove', user_id: row.user_id }), '已移除'),
            )}
          />
        </>
      ) : (
        <>
          <CardBlock
            title="专属组开关"
            actions={
              <Checkbox
                checked={gban_own.enabled}
                onChange={(e) => run(ownMut.mutateAsync({ action: 'enable', on: e.target.checked }), '已更新')}
              />
            }
          >
            <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.8 }}>
              专属组只在本归属人的机器人之间共享封禁；关闭后不生效。
            </Typography>
          </CardBlock>
          {addForm}
          <CardBlock title={`生效群组（${gban_own.chats.length}）`}>
            {chats.filter((c) => ownChatIDs.has(c.chat_id)).length === 0 ? (
              <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>你名下还没有群组。</Typography>
            ) : (
              <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
                {chats
                  .filter((c) => ownChatIDs.has(c.chat_id))
                  .map((c) => {
                    const on = gban_own.chats.includes(c.chat_id)
                    return (
                      <Button
                        key={`${c.bot_id}:${c.chat_id}`}
                        size="small"
                        variant={on ? 'contained' : 'outlined'}
                        onClick={() => run(ownMut.mutateAsync({ action: 'chat', chat_id: c.chat_id, on: !on }), '已更新')}
                      >
                        {c.title || c.chat_id}
                      </Button>
                    )
                  })}
              </Box>
            )}
          </CardBlock>
          <Toolbar>
            <SearchField value={q} onChange={setQ} placeholder="搜索用户 / 原因" sx={{ maxWidth: 320 }} />
          </Toolbar>
          <DataTable
            rows={filterRows(gban_own.bans, q, gbanKey)}
            rowKey={(row) => row.user_id}
            empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>专属组名单为空。</Typography>}
            columns={gbanColumns((row) =>
              run(ownMut.mutateAsync({ action: 'remove', user_id: row.user_id }), '已移除'),
            )}
          />
        </>
      )}
    </>
  )
}

function AdminsSection() {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const mut = useAdminMutation()
  const [userId, setUserId] = useState('')
  const [note, setNote] = useState('')

  if (!state.data) return null
  const admins = state.data.admins ?? []
  const valid = /^\d+$/.test(userId.trim())

  const columns: Column<AdminRow>[] = [
    { key: 'user', header: '用户', width: 140, render: (row) => `uid ${row.user_id}` },
    { key: 'note', header: '备注', render: (row) => row.note || '—' },
    {
      key: 'actions',
      header: '',
      width: 80,
      render: (row) => (
        <Button
          size="small"
          color="error"
          variant="text"
          disabled={mut.isPending}
          onClick={() => run(mut.mutateAsync({ action: 'remove', user_id: row.user_id }), '已移除')}
        >
          移除
        </Button>
      ),
    },
  ]

  return (
    <>
      <CardBlock title="添加次级管理员">
        <Box sx={{ display: 'flex', gap: 1.5, flexWrap: 'wrap', alignItems: 'center' }}>
          <TextField label="用户 uid" value={userId} onChange={(e) => setUserId(e.target.value)} sx={{ width: 160 }} />
          <TextField label="备注" value={note} onChange={(e) => setNote(e.target.value)} sx={{ width: 280 }} />
          <Button
            variant="contained"
            disabled={!valid || mut.isPending}
            onClick={() => {
              run(mut.mutateAsync({ action: 'add', user_id: userId.trim(), note: note.trim() }), '已添加')
              setUserId('')
              setNote('')
            }}
          >
            添加
          </Button>
        </Box>
      </CardBlock>
      <DataTable
        rows={admins}
        rowKey={(row) => row.user_id}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有次级管理员。</Typography>}
        columns={columns}
      />
    </>
  )
}
