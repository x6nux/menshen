// 上游渠道（仅主管理员）：
// - 列表：名称 + 启用徽标 + base_url 副行；
// - `＋` 抽屉：名称 / 渠道类型 / base_url / api_key / 能力 / 启用 → upstream add；
// - 详情：渠道配置抽屉（api_key 留空 = 不改，显示当前掩码）、启停 Switch（乐观）、
//   改名抽屉、危险区删除（ActionSheet 写明对象与后果）；
// - `测试连通`：upstream {action:'test'} 发一次最小请求，结果行内
//   展示延迟/模型或可读错误，pending 时禁用自身按钮、不阻塞其他操作。
import NetworkCheckOutlined from '@mui/icons-material/NetworkCheckOutlined'
import { Box, Button, TextField, Typography } from '@mui/material'
import { useState } from 'react'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useOptimisticMiniMutation, useUpstreamMutation } from '../api/mutations'
import type { State, UpstreamKind, UpstreamTestResp } from '../api/types'
import { useNav } from '../nav'
import {
  Badge,
  EmptyState,
  ErrorState,
  FormDrawer,
  ListRow,
  SectionCard,
  Segmented,
  SettingRow,
  Skeletons,
  SwitchRow,
  useConfirm,
  useToast,
} from '../ui'

/** KIND_OPTIONS 是渠道类型选择项；顺序与后端 upstream.Kind 一致。 */
const KIND_OPTIONS: { value: UpstreamKind; label: string }[] = [
  { value: 'openai', label: 'OpenAI Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'cloudflare', label: 'Cloudflare' },
]

const KIND_LABEL: Record<UpstreamKind, string> = Object.fromEntries(
  KIND_OPTIONS.map((o) => [o.value, o.label]),
) as Record<UpstreamKind, string>

const KIND_HINT: Record<UpstreamKind, string> = {
  openai:
    'base_url 填到域名（如 https://api.example.com）。chat 走 /v1/chat/completions，systemone 走 /v1/systemone。',
  'openai-responses':
    'base_url 填到域名（如 https://api.openai.com）。请求发 /v1/responses，仅用于复判与形态总结。',
  anthropic:
    'base_url 填 https://api.anthropic.com。请求发 /v1/messages，仅用于复判与形态总结。',
  gemini:
    'base_url 填 https://generativelanguage.googleapis.com/v1beta。仅用于复判与形态总结。',
  cloudflare:
    'base_url 需含 /client/v4/accounts/<账号ID>；模型 ID 填 @cf/cloudflare/clef，主判定直调 Clef（也可开 chat）。',
}

/** isChatOnly 与后端 Kind.ChatOnly 同步：这些类型只能做复判/总结。 */
function isChatOnly(kind: UpstreamKind) {
  return kind !== 'openai' && kind !== 'cloudflare'
}

/** defaultCaps 与后端 Kind.DefaultCaps 同步：切类型时的默认能力。 */
function defaultCaps(kind: UpstreamKind): [boolean, boolean] {
  return kind === 'cloudflare' ? [false, true] : [true, false]
}

/** statusBadge 上游启用状态徽标。 */
function StatusBadge({ status }: { status: boolean }) {
  return <Badge tone={status ? 'ok' : 'no'}>{status ? '启用' : '停用'}</Badge>
}

/** KindSwitch 渠道类型分段控件 + 一行说明。 */
function KindSwitch({
  value,
  onChange,
}: {
  value: UpstreamKind
  onChange: (kind: UpstreamKind) => void
}) {
  return (
    <>
      <Typography sx={{ fontSize: 13, color: 'text.secondary', mb: 0.5 }}>渠道类型</Typography>
      <Segmented
        value={value}
        onChange={(v) => onChange(v as UpstreamKind)}
        options={KIND_OPTIONS}
        ariaLabel="渠道类型"
        sx={{ flexWrap: 'wrap', rowGap: 0.75 }}
      />
      <Typography sx={{ mt: 0.75, fontSize: 12, color: 'text.secondary', lineHeight: 1.6 }}>
        {KIND_HINT[value]}
      </Typography>
    </>
  )
}

export function UpstreamsPage() {
  const nav = useNav()
  const toast = useToast()
  const state = useMiniState(true)
  const addMut = useUpstreamMutation()

  const [addOpen, setAddOpen] = useState(false)
  const [name, setName] = useState('')
  const [kind, setKind] = useState<UpstreamKind>('openai')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setAPIKey] = useState('')
  const [supportsChat, setSupportsChat] = useState(true)
  const [supportsSystemone, setSupportsSystemone] = useState(true)
  const [enabled, setEnabled] = useState(true)

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!state.data.me.main) return <ErrorState status={403} />

  const upstreams = state.data.upstreams ?? []

  function openAdd() {
    setName('')
    setKind('openai')
    setBaseURL('')
    setAPIKey('')
    setSupportsChat(true)
    setSupportsSystemone(true)
    setEnabled(true)
    setAddOpen(true)
  }

  function changeAddKind(next: UpstreamKind) {
    setKind(next)
    const [chat, so] = defaultCaps(next)
    setSupportsChat(chat)
    setSupportsSystemone(so)
  }

  function submitAdd() {
    const chatOnly = isChatOnly(kind)
    addMut.mutate(
      {
        action: 'add',
        name: name.trim(),
        kind,
        base_url: baseURL.trim(),
        api_key: apiKey.trim(),
        supports_chat: chatOnly ? true : supportsChat,
        supports_systemone: chatOnly ? false : supportsSystemone,
        disabled: !enabled,
      },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已添加')
          setAddOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="upstreams-page">
      <SectionCard
        title="渠道"
        footer={
          <ListRow primary="＋ 添加渠道" chevron onClick={openAdd} />
        }
      >
        {upstreams.length === 0 ? (
          <Typography sx={{ px: 2, py: 1.5, fontSize: 13, color: 'text.secondary' }}>
            （还没有上游）先添加一个 API 渠道，再到「模型定价」登记模型。
          </Typography>
        ) : (
          upstreams.map((u) => (
            <ListRow
              key={u.id}
              primary={u.name}
              secondary={`${KIND_LABEL[u.kind]} · ${u.base_url}`}
              badge={<StatusBadge status={u.status} />}
              chevron
              onClick={() => nav.push({ k: 'upstream', id: u.id })}
            />
          ))
        )}
      </SectionCard>

      <FormDrawer
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="添加渠道"
        pending={addMut.isPending}
        submitText="添加"
        submitDisabled={name.trim() === '' || baseURL.trim() === ''}
        onSubmit={submitAdd}
      >
        <TextField
          fullWidth
          size="small"
          label="名称（模型名前缀）"
          placeholder="如 demo（不含 / 和 :）"
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
        <Box sx={{ mt: 1.5 }}>
          <KindSwitch value={kind} onChange={changeAddKind} />
        </Box>
        <TextField
          fullWidth
          size="small"
          label="base_url"
          placeholder="https://api.example.com"
          value={baseURL}
          onChange={(event) => setBaseURL(event.target.value)}
          sx={{ mt: 1.5 }}
        />
        <TextField
          fullWidth
          size="small"
          label="api_key"
          value={apiKey}
          onChange={(event) => setAPIKey(event.target.value)}
          sx={{ mt: 1.5 }}
        />
        <Box sx={{ mt: 1 }}>
          {isChatOnly(kind) ? (
            <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
              该类型是对话协议，固定用于复判与形态总结。
            </Typography>
          ) : (
            <>
              <SwitchRow
                primary="支持 chat（复判大模型）"
                checked={supportsChat}
                onChange={setSupportsChat}
              />
              <SwitchRow
                primary="支持 systemone（初判）"
                checked={supportsSystemone}
                onChange={setSupportsSystemone}
              />
            </>
          )}
          <SwitchRow primary="启用" checked={enabled} onChange={setEnabled} />
        </Box>
      </FormDrawer>
    </Box>
  )
}

export function UpstreamDetailPage({ id }: { id: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)

  const updateMut = useUpstreamMutation()
  const renameMut = useUpstreamMutation()
  const removeMut = useUpstreamMutation()
  const testMut = useUpstreamMutation<UpstreamTestResp>()
  const statusMut = useOptimisticMiniMutation('upstream')

  const [editOpen, setEditOpen] = useState(false)
  const [kindDraft, setKindDraft] = useState<UpstreamKind>('openai')
  const [baseDraft, setBaseDraft] = useState('')
  const [keyDraft, setKeyDraft] = useState('')
  const [chatDraft, setChatDraft] = useState(true)
  const [soDraft, setSoDraft] = useState(true)
  const [renameOpen, setRenameOpen] = useState(false)
  const [nameDraft, setNameDraft] = useState('')
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!state.data.me.main) return <ErrorState status={403} />

  const upstream = (state.data.upstreams ?? []).find((u) => u.id === id)
  if (!upstream) {
    return <EmptyState title="上游不存在" description="它可能已被删除。" />
  }

  function openEdit() {
    setKindDraft(upstream?.kind ?? 'openai')
    setBaseDraft(upstream?.base_url ?? '')
    setKeyDraft('')
    setChatDraft(upstream?.supports_chat ?? false)
    setSoDraft(upstream?.supports_systemone ?? false)
    setEditOpen(true)
  }

  function changeEditKind(next: UpstreamKind) {
    setKindDraft(next)
    const [chat, so] = defaultCaps(next)
    setChatDraft(chat)
    setSoDraft(so)
  }

  function openRename() {
    setNameDraft(upstream?.name ?? '')
    setRenameOpen(true)
  }

  function toggleStatus(next: boolean) {
    statusMut.mutate(
      {
        body: { action: 'update', id, status: next },
        apply: (s: State) => ({
          ...s,
          upstreams: (s.upstreams ?? []).map((u) => (u.id === id ? { ...u, status: next } : u)),
        }),
      },
      {
        onSuccess: (resp) => toast(resp.note ?? (next ? '已启用' : '已停用')),
        onError: (err) => toast(err.message),
      },
    )
  }

  function submitEdit() {
    const chatOnly = isChatOnly(kindDraft)
    const body: Record<string, unknown> = {
      action: 'update',
      id,
      kind: kindDraft,
      supports_chat: chatOnly ? true : chatDraft,
      supports_systemone: chatOnly ? false : soDraft,
    }
    const base = baseDraft.trim()
    if (base !== '') body.base_url = base
    // api_key 留空 = 不改：不把空串发给服务端，避免误清空已保存的密钥。
    const key = keyDraft.trim()
    if (key !== '') body.api_key = key
    updateMut.mutate(body, {
      onSuccess: (resp) => {
        toast(resp.note ?? '已保存')
        setEditOpen(false)
      },
      onError: (err) => toast(err.message),
    })
  }

  function submitRename() {
    renameMut.mutate(
      { action: 'update', id, name: nameDraft.trim() },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已改名')
          setRenameOpen(false)
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  async function removeUpstream() {
    const ok = await confirm({
      title: '删除该上游？',
      description: `将删除渠道「${upstream?.name}」（${upstream?.base_url}）。名下有模型时服务端会拒绝，请先在「模型定价」删除。此操作不可恢复。`,
      confirmText: '删除',
      danger: true,
    })
    if (!ok) return
    removeMut.mutate(
      { action: 'remove', id },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已删除')
          nav.pop()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function runTest() {
    setTestResult(null)
    testMut.mutate(
      { action: 'test', id },
      {
        onSuccess: (resp) => {
          if (resp.ok) {
            setTestResult({
              ok: true,
              text: `✅ 连通 · ${resp.latency_ms ?? 0}ms · ${resp.model ?? ''}`,
            })
          } else {
            setTestResult({ ok: false, text: `❌ ${resp.error || '测试失败'}` })
          }
        },
        // 配置类问题（没有可用模型等）服务端回 400，client 抛 ApiError。
        onError: (err) => setTestResult({ ok: false, text: `❌ ${err.message}` }),
      },
    )
  }

  return (
    <Box data-testid="upstream-detail-page">
      <SectionCard>
        <ListRow
          primary={upstream.name}
          secondary={upstream.base_url}
          badge={<StatusBadge status={upstream.status} />}
        />
        <SwitchRow
          primary="启用"
          secondary={upstream.status ? '关闭后该渠道不再被模型调用' : '开启后恢复调用'}
          checked={upstream.status}
          disabled={statusMut.isPending}
          onChange={toggleStatus}
        />
      </SectionCard>

      <SectionCard title="配置">
        <SettingRow
          label="渠道类型"
          hint="决定端点路径、鉴权与请求/响应协议"
          value={KIND_LABEL[upstream.kind]}
        />
        <SettingRow
          label="名称"
          hint="模型名的前缀，改名前请确认没有模型引用冲突"
          value={upstream.name}
          onClick={openRename}
        />
        <SettingRow
          label="渠道配置"
          hint="base_url、api_key 与能力开关"
          value="编辑"
          onClick={openEdit}
        />
        <Box sx={{ px: 2, py: 1.5 }}>
          <Button
            fullWidth
            variant="outlined"
            startIcon={<NetworkCheckOutlined />}
            loading={testMut.isPending}
            disabled={testMut.isPending}
            onClick={runTest}
          >
            测试连通
          </Button>
          {testResult && (
            <Typography
              data-testid="upstream-test-result"
              sx={{
                mt: 1,
                fontSize: 13,
                lineHeight: 1.6,
                color: testResult.ok ? 'success.main' : 'error.main',
              }}
            >
              {testResult.text}
            </Typography>
          )}
        </Box>
      </SectionCard>

      <SectionCard title="危险区">
        <ListRow
          primary="删除该上游"
          secondary="删除前需先移除它名下的模型；此操作不可恢复"
          disabled={removeMut.isPending}
          onClick={() => void removeUpstream()}
          sx={{ color: 'error.main' }}
        />
      </SectionCard>

      <FormDrawer
        open={editOpen}
        onClose={() => setEditOpen(false)}
        title="渠道配置"
        pending={updateMut.isPending}
        submitText="保存"
        onSubmit={submitEdit}
      >
        <KindSwitch value={kindDraft} onChange={changeEditKind} />
        <TextField
          fullWidth
          size="small"
          label="base_url（留空 = 不改）"
          value={baseDraft}
          onChange={(event) => setBaseDraft(event.target.value)}
          sx={{ mt: 1.5 }}
        />
        <TextField
          fullWidth
          size="small"
          label="api_key（留空 = 不改）"
          placeholder="留空 = 不改"
          value={keyDraft}
          onChange={(event) => setKeyDraft(event.target.value)}
          helperText={`当前：${upstream.api_key || '未设置'}`}
          sx={{ mt: 1.5 }}
        />
        <Box sx={{ mt: 1 }}>
          {isChatOnly(kindDraft) ? (
            <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
              该类型是对话协议，固定用于复判与形态总结。
            </Typography>
          ) : (
            <>
              <SwitchRow
                primary="支持 chat（复判大模型）"
                checked={chatDraft}
                onChange={setChatDraft}
              />
              <SwitchRow
                primary="支持 systemone（初判）"
                checked={soDraft}
                onChange={setSoDraft}
              />
            </>
          )}
        </Box>
      </FormDrawer>

      <FormDrawer
        open={renameOpen}
        onClose={() => setRenameOpen(false)}
        title="重命名上游"
        pending={renameMut.isPending}
        submitText="保存"
        submitDisabled={nameDraft.trim() === ''}
        onSubmit={submitRename}
      >
        <TextField
          fullWidth
          size="small"
          label="名称"
          value={nameDraft}
          onChange={(event) => setNameDraft(event.target.value)}
        />
        <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
          名称是模型名前缀（上游名/模型 ID），改名会同步更新引用它的模型。
        </Typography>
      </FormDrawer>
    </Box>
  )
}
