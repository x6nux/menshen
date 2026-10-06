// 判定记录详情：完整正文与理由，人工处置按钮（AI 复查 / 解封 / 加白 / 标记 / 联封）。
import { Box, Button, Typography } from '@mui/material'
import { useLog, useMiniState } from '../../api/hooks'
import { useLogactMutation, useRulesMutation } from '../../api/mutations'
import { actionLabel, deciderLabel, displayTz, fmtTSFull, kindLabel, verdictLabel } from '../../lib/format'
import { verdictInfo } from '../../lib/status'
import { openLink } from '../../telegram'
import { CardBlock, ConfirmButton, InfoList, PageHeader, StatusBadge } from '../components'
import { useRunFeedback } from '../feedback'
import { useAdminNav } from '../nav'
import { ApiError } from '../../api/client'

export function LogDetailView({ id }: { id: number }) {
  const log = useLog(id)
  const state = useMiniState(true)
  const run = useRunFeedback()
  const nav = useAdminNav()
  const actMut = useLogactMutation()
  const rulesMut = useRulesMutation<{ ok: boolean }>()

  if (log.isPending) return <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>加载中…</Typography>
  if (log.isError) {
    const status = log.error instanceof ApiError ? log.error.status : 0
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>
          {status === 404 ? '记录不存在。' : '记录加载失败，请稍后重试。'}
        </Typography>
      </CardBlock>
    )
  }

  const row = log.data
  const tz = state.data ? displayTz(state.data) : undefined
  const main = state.data?.me.main ?? false
  const chat = state.data?.chats.find((c) => c.chat_id === row.chat_id && c.bot_id === row.bot_id)

  const act = (action: string, okText: string) => run(actMut.mutateAsync({ id, action }), okText)

  return (
    <>
      <PageHeader
        title={`记录 #${row.id}`}
        subtitle={fmtTSFull(row.created_at, tz)}
        actions={<StatusBadge info={verdictInfo(row.verdict)} />}
      />

      <CardBlock title="判定">
        <InfoList
          items={[
            { label: '结论', value: verdictLabel(row.verdict) },
            { label: '置信度', value: row.confidence },
            { label: '判定方', value: deciderLabel(row.decider) },
            { label: '类型', value: kindLabel(row.kind) },
            { label: '处置', value: actionLabel(row.action) },
            { label: '开销', value: row.cost_text || '—' },
            { label: '用户', value: `uid ${row.user_id}` },
            {
              label: '群组',
              value: chat?.title ? `${chat.title}（${row.chat_id}）` : String(row.chat_id),
            },
          ]}
        />
      </CardBlock>

      <CardBlock
        title="处置"
        actions={
          <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
            <Button size="small" variant="outlined" disabled={actMut.isPending} onClick={() => act('review', '已发起 AI 复查')}>
              AI 复查
            </Button>
            <Button size="small" variant="outlined" disabled={actMut.isPending} onClick={() => act('unmute', '已解除限制')}>
              解封
            </Button>
            <Button size="small" variant="outlined" disabled={actMut.isPending} onClick={() => act('white', '已加入白名单')}>
              加白名单 24h
            </Button>
            <ConfirmButton
              size="small"
              variant="outlined"
              danger
              title="人工标记为广告"
              description="不经 AI，按最高档处置（删除 + 禁言/封禁）并进联合封禁名单。"
              confirmLabel="标记"
              disabled={actMut.isPending}
              onConfirm={() => act('ban', '已标记')}
            >
              人工标记广告
            </ConfirmButton>
            {main && (
              <>
                <ConfirmButton
                  size="small"
                  variant="outlined"
                  danger
                  title="加入全局联合封禁"
                  description="该用户将在所有使用本服务的群组被拦截。"
                  confirmLabel="联封"
                  disabled={actMut.isPending}
                  onConfirm={() => act('gban', '已加入联合封禁')}
                >
                  加入联封
                </ConfirmButton>
                <ConfirmButton
                  size="small"
                  variant="outlined"
                  title="解除联合封禁"
                  description="撤销全局与专属联合封禁名单中的这个人。"
                  confirmLabel="解除"
                  disabled={actMut.isPending}
                  onConfirm={() => act('ungban', '已解除联封')}
                >
                  解除联封
                </ConfirmButton>
                <Button
                  size="small"
                  variant="outlined"
                  disabled={rulesMut.isPending}
                  onClick={() => run(rulesMut.mutateAsync({ action: 'agent_start', record_id: id }), '已启动规则发现')}
                >
                  AI 写规则
                </Button>
              </>
            )}
          </Box>
        }
      >
        <Typography sx={{ fontSize: 12.5, color: 'text.secondary', lineHeight: 1.8 }}>
          处置会立即作用到群组。解封与加白对之后 24 小时内的发言生效；「AI 写规则」围绕本条原文提炼正则。
        </Typography>
      </CardBlock>

      <CardBlock
        title="正文"
        actions={
          row.view_url ? (
            <Button size="small" variant="text" onClick={() => openLink(row.view_url)}>
              查看原文
            </Button>
          ) : undefined
        }
      >
        <Typography sx={{ fontSize: 13.5, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
          {row.text || '（无正文留底）'}
        </Typography>
      </CardBlock>

      <CardBlock title="理由">
        <Typography sx={{ fontSize: 13.5, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
          {row.reason || '—'}
        </Typography>
      </CardBlock>

      <Box>
        <Button size="small" variant="text" onClick={() => nav.go({ k: 'user', id: row.user_id })}>
          查看该用户资料 →
        </Button>
      </Box>
    </>
  )
}
