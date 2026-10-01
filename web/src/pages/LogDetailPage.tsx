// 记录详情（计划 4.4）：信息卡 + 原文卡 + 底部固定操作栏。
// 数据必须单独请求 log{id}（列表正文里后端会回查全量留底，列表数据不完整）。
// 操作走 logact；成功后由 mutation 统一失效 ['log']/['logs']/['user']/['state']，
// 详情与列表都会刷新；服务端 note 优先 toast。
import { Box, Button, Typography } from '@mui/material'
import type { ReactNode } from 'react'
import { errorStatus } from '../api/client'
import { useLog, useMiniState } from '../api/hooks'
import { useLogactMutation } from '../api/mutations'
import { actionLabel, fmtTS, kindLabel, verdictLabel } from '../lib/format'
import { useNav } from '../nav'
import { openLink } from '../telegram'
import { ErrorState, SectionCard, Skeletons, useConfirm, useToast } from '../ui'
import { InfoRow } from './shared'

// 主操作（与旧页四个按钮一一对应）；危险项带确认文案。
// 底部操作栏 6 个按钮最多 3 行（主管理员）+ 说明文字，内容区预留 150px。
interface ActionSpec {
  action: string
  label: string
  danger?: boolean
  /** 确认面板文案；缺省直接执行。 */
  confirmTitle?: string
  confirmDescription?: string
}

const PRIMARY_ACTIONS: ActionSpec[] = [
  { action: 'review', label: 'AI 复查' },
  { action: 'unmute', label: '解封（判定维持）' },
  { action: 'white', label: '加白名单 24h' },
  {
    action: 'ban',
    label: '人工标记广告',
    danger: true,
    confirmTitle: '人工标记广告？',
    // 旧页 confirm 文案原样保留。
    confirmDescription: '不经 AI 直接按最高档处置？',
  },
]

const GBAN_ACTIONS: ActionSpec[] = [
  {
    action: 'gban',
    label: '联合封禁',
    danger: true,
    confirmTitle: '联合封禁？',
    confirmDescription: '加入联合封禁名单并全平台执行？',
  },
  {
    action: 'ungban',
    label: '解除联合封禁',
    danger: true,
    confirmTitle: '解除联合封禁？',
    confirmDescription: '把该用户从你能解除的联合封禁名单里移除。',
  },
]

const BOTTOM_RESERVE = 'calc(150px + env(safe-area-inset-bottom))'

export function LogDetailPage({ id }: { id: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)
  const log = useLog(id)
  const act = useLogactMutation()

  if (log.isPending) return <Skeletons rows={4} />
  if (log.isError) {
    return <ErrorState status={errorStatus(log.error)} onRetry={() => void log.refetch()} />
  }

  const l = log.data
  const chat = state.data?.chats.find((c) => c.chat_id === l.chat_id)
  const isMain = state.data?.me.main ?? false

  const run = (spec: ActionSpec) => {
    return async () => {
      if (spec.confirmTitle) {
        const ok = await confirm({
          title: spec.confirmTitle,
          description: spec.confirmDescription,
          confirmText: '确定',
          danger: spec.danger,
        })
        if (!ok) return
      }
      act.mutate(
        { id, action: spec.action },
        {
          onSuccess: (resp) => toast(resp.note ?? '已执行'),
          onError: (err) => toast(err.message),
        },
      )
    }
  }

  const details: { label: string; value: ReactNode }[] = [
    { label: '时间', value: <Mono>{fmtTS(l.created_at)}</Mono> },
    {
      label: '群 / 用户',
      value: (
        <>
          <Mono>{String(l.chat_id)}</Mono>
          {chat?.title ? ` · ${chat.title}` : ''}
          {' / '}
          <Box
            component="span"
            role="link"
            tabIndex={0}
            onClick={() => nav.push({ k: 'user', id: l.user_id })}
            sx={{ color: 'primary.main' }}
          >
            uid {l.user_id}（资料）
          </Box>
        </>
      ),
    },
    {
      label: '判定',
      value: [
        verdictLabel(l.verdict),
        `${Math.round(l.confidence * 100)}%`,
        l.decider,
        l.kind ? kindLabel(l.kind) : '',
      ]
        .filter(Boolean)
        .join(' · '),
    },
    { label: '处置', value: actionLabel(l.action) },
    { label: '开销', value: <Mono>{l.cost_text}</Mono> },
    ...(l.reason ? [{ label: '理由', value: l.reason }] : []),
  ]

  return (
    <Box data-testid="log-detail-page" sx={{ pb: BOTTOM_RESERVE }}>
      <SectionCard>
        {details.map((row) => (
          <InfoRow key={row.label} label={row.label}>
            {row.value}
          </InfoRow>
        ))}
      </SectionCard>

      <SectionCard title="原文">
        <Box sx={{ px: 2, py: 1.5 }}>
          {l.text !== '' ? (
            <Typography
              component="pre"
              sx={{
                m: 0,
                fontFamily: 'ui-monospace, Menlo, monospace',
                fontSize: 13,
                lineHeight: 1.7,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
              }}
            >
              {l.text}
            </Typography>
          ) : (
            <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>（正文为空）</Typography>
          )}
          {l.view_url !== '' && (
            <Button
              fullWidth
              variant="outlined"
              sx={{ mt: 1.5 }}
              onClick={() => openLink(l.view_url)}
            >
              打开原文查看页
            </Button>
          )}
        </Box>
      </SectionCard>

      <Box
        data-testid="log-action-bar"
        sx={{
          position: 'fixed',
          left: 0,
          right: 0,
          bottom: 0,
          px: 1.5,
          pt: 1,
          pb: 'calc(8px + env(safe-area-inset-bottom))',
          bgcolor: 'background.paper',
          borderTop: '1px solid',
          borderColor: 'divider',
          zIndex: (theme) => theme.zIndex.appBar,
        }}
      >
        <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 0.75 }}>
          {PRIMARY_ACTIONS.map((spec) => (
            <Button
              key={spec.action}
              size="small"
              variant="outlined"
              color={spec.danger ? 'error' : 'primary'}
              disabled={act.isPending}
              onClick={() => void run(spec)()}
            >
              {spec.label}
            </Button>
          ))}
          {isMain &&
            GBAN_ACTIONS.map((spec) => (
              <Button
                key={spec.action}
                size="small"
                variant="outlined"
                color="error"
                disabled={act.isPending}
                onClick={() => void run(spec)()}
              >
                {spec.label}
              </Button>
            ))}
        </Box>
        <Typography sx={{ mt: 0.75, fontSize: 12, color: 'text.secondary', lineHeight: 1.5 }}>
          复查结果发到群里（受群内静默开关约束）；人工标记与群内 /ban 命令同效。
        </Typography>
      </Box>
    </Box>
  )
}

/** Mono 给标识类值（时间/群号/开销）一个等宽字体。 */
function Mono({ children }: { children: ReactNode }) {
  return <Box component="span" sx={{ fontFamily: 'ui-monospace, Menlo, monospace' }}>{children}</Box>
}
