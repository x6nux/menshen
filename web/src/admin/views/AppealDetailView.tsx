// 申诉详情：状况、AI 复核、解禁码与兑换记录，以及人工处置。
import { Box, Button, Typography } from '@mui/material'
import { useAppeal, useMiniState } from '../../api/hooks'
import { useAppealactMutation } from '../../api/mutations'
import { apAI, apStatus, displayTz, fmtTSFull } from '../../lib/format'
import { appealStatusInfo } from '../../lib/status'
import { openLink } from '../../telegram'
import { CardBlock, ConfirmButton, InfoList, MonoText, PageHeader, StatusBadge } from '../components'
import { useRunFeedback } from '../feedback'

export function AppealDetailView({ id }: { id: number }) {
  const appeal = useAppeal(id)
  const state = useMiniState(true)
  const run = useRunFeedback()
  const actMut = useAppealactMutation()

  if (appeal.isPending) return <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>加载中…</Typography>
  if (appeal.isError || !appeal.data) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>申诉加载失败或不存在。</Typography>
      </CardBlock>
    )
  }

  const a = appeal.data
  const tz = state.data ? displayTz(state.data) : undefined
  const bot = state.data?.bots.find((b) => b.bot_id === a.bot_id)
  const open = ['statement', 'ai', 'web', 'noweb', 'code'].includes(a.status)
  const act = (action: string, okText: string) => run(actMut.mutateAsync({ id, action }), okText)

  return (
    <>
      <PageHeader
        title={`申诉 #${a.id}`}
        subtitle={`uid ${a.user_id}${bot ? ` · ${bot.label}` : ''}`}
        actions={<StatusBadge info={appealStatusInfo(a.status)} />}
      />

      <CardBlock title="申诉">
        <InfoList
          items={[
            { label: '状态', value: apStatus(a.status) },
            { label: '提交时间', value: fmtTSFull(a.created_at, tz) },
            { label: '更新时间', value: fmtTSFull(a.updated_at, tz) },
            { label: '网页尝试', value: `${a.web_attempts} 次（通过 ${a.web_passes} / 校验 ${a.web_checks}）` },
          ]}
        />
        <Typography sx={{ mt: 1.5, fontSize: 13.5, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
          {a.statement || '（无申诉陈述）'}
        </Typography>
      </CardBlock>

      <CardBlock
        title="AI 复核"
        actions={
          a.detail_url ? (
            <Button size="small" variant="text" onClick={() => openLink(a.detail_url)}>
              公开详情页
            </Button>
          ) : undefined
        }
      >
        <InfoList
          items={[
            { label: '结论', value: apAI(a.ai_result) },
            { label: '置信度', value: a.ai_conf },
            { label: '模型', value: a.ai_model || '—' },
            { label: '开销', value: a.ai_cost || '—' },
          ]}
        />
        <Typography sx={{ mt: 1.5, fontSize: 13.5, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
          {a.ai_reason || '—'}
        </Typography>
      </CardBlock>

      <CardBlock title="解禁码">
        {a.code ? (
          <>
            <Typography sx={{ fontSize: 16 }}>
              <MonoText>{a.code}</MonoText>
            </Typography>
            <Typography sx={{ mt: 0.5, fontSize: 12.5, color: 'text.secondary' }}>
              {a.code_expires > 0 ? `有效期至 ${fmtTSFull(a.code_expires, tz)}` : '已过期或未签发'}
            </Typography>
          </>
        ) : (
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>尚未签发解禁码。</Typography>
        )}
        {a.redeems.length > 0 && (
          <Box sx={{ mt: 1.5 }}>
            <Typography sx={{ fontSize: 13, mb: 0.5 }}>兑换记录</Typography>
            {a.redeems.map((r, index) => (
              <Typography key={index} sx={{ fontSize: 12.5, color: 'text.secondary' }}>
                {fmtTSFull(r.at, tz)} · 群 {r.chat_id} · by uid {r.by_uid}
              </Typography>
            ))}
          </Box>
        )}
      </CardBlock>

      {open && (
        <CardBlock title="人工处置">
          <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
            <ConfirmButton
              variant="contained"
              title="人工解除"
              description="直接解除该用户的限制并加白名单缓冲。"
              confirmLabel="解除"
              disabled={actMut.isPending}
              onConfirm={() => act('approve', '已解除')}
            >
              人工解除
            </ConfirmButton>
            <ConfirmButton
              variant="outlined"
              danger
              title="驳回申诉"
              description="维持原判，申诉标记为已驳回。"
              confirmLabel="驳回"
              disabled={actMut.isPending}
              onConfirm={() => act('reject', '已驳回')}
            >
              驳回
            </ConfirmButton>
            {(a.status === 'web' || a.status === 'noweb') && (
              <Button
                variant="outlined"
                disabled={actMut.isPending}
                onClick={() => act('issue_code', '已签发解禁码')}
              >
                直接签发解禁码
              </Button>
            )}
            {['web', 'noweb', 'ai'].includes(a.status) && (
              <Button variant="outlined" disabled={actMut.isPending} onClick={() => act('rerun', '已重跑 AI 复核')}>
                重跑 AI 复核
              </Button>
            )}
          </Box>
        </CardBlock>
      )}
    </>
  )
}
