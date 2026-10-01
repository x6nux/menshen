// 申诉详情（计划 4.5）：数据一律 POST appeal {id}——列表接口把 statement/ai_reason
// 截到 300 字且不含解禁码/兑换/网页验证记录，详情必须单独请求、完整展示。
// 底部操作栏按状态渲染（沿用旧页规则）：未结状态显示人工解除/驳回；
// web/noweb 追加签发解禁码，web/noweb/ai 追加重跑 AI 复核。操作走 appealact。
import { Box, Button, Typography } from '@mui/material'
import { errorStatus } from '../api/client'
import { useAppeal, useMiniState } from '../api/hooks'
import { useAppealactMutation } from '../api/mutations'
import { apAI, fmtTS } from '../lib/format'
import { appealStatusInfo } from '../lib/status'
import { useNav } from '../nav'
import { openLink } from '../telegram'
import { Badge, ErrorState, SectionCard, Skeletons, useConfirm, useToast } from '../ui'
import { InfoRow, UserLink } from './shared'
import { useBarReserve } from './useBarReserve'

/** 未结状态集合：与旧页 viewAppealDetail 的 open 数组一致。 */
const OPEN_STATUSES = ['statement', 'ai', 'web', 'noweb', 'code']

/** 底部操作栏兜底高度（4 按钮 + 说明）；有 ResizeObserver 时以实测为准。 */
const BAR_FALLBACK = 170
const MONO = 'ui-monospace, Menlo, monospace'

export function AppealDetailPage({ id }: { id: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)
  const appeal = useAppeal(id)
  const act = useAppealactMutation()
  // hooks 必须无条件调用：条形高度在数据到达前先用兜底估算。
  const { barRef, reserved } = useBarReserve(
    appeal.data !== undefined && OPEN_STATUSES.includes(appeal.data.status) ? BAR_FALLBACK : 0,
  )

  if (appeal.isPending) return <Skeletons rows={4} />
  if (appeal.isError) {
    return <ErrorState status={errorStatus(appeal.error)} onRetry={() => void appeal.refetch()} />
  }

  const a = appeal.data
  const status = appealStatusInfo(a.status)
  const bot = state.data?.bots.find((b) => b.bot_id === a.bot_id)
  const open = OPEN_STATUSES.includes(a.status)
  const canIssueCode = a.status === 'web' || a.status === 'noweb'
  const canRerun = canIssueCode || a.status === 'ai'

  const run = (action: string) => {
    act.mutate(
      { id, action },
      {
        onSuccess: (resp) => toast(resp.note ?? '已执行'),
        onError: (err) => toast(err.message),
      },
    )
  }

  const approve = async () => {
    const ok = await confirm({
      title: '人工解除？',
      description: '确认通过并解除该用户全部限制？',
      confirmText: '解除',
    })
    if (ok) run('approve')
  }

  const reject = async () => {
    const ok = await confirm({
      title: '驳回申诉？',
      description: '确认驳回？限制保持原样。',
      confirmText: '确认驳回',
      danger: true,
    })
    if (ok) run('reject')
  }

  return (
    <Box
      data-testid="appeal-detail-page"
      data-reserved={reserved}
      sx={{ pb: open ? `calc(${reserved}px + env(safe-area-inset-bottom))` : 0 }}
    >
      <SectionCard>
        <InfoRow label="状态">
          <Badge tone={status.tone}>{status.label}</Badge>
        </InfoRow>
        <InfoRow label="申诉人 / bot">
          <UserLink userId={a.user_id} onClick={() => nav.push({ k: 'user', id: a.user_id })} />
          {` / ${bot ? bot.label : a.bot_id}`}
        </InfoRow>
        <InfoRow label="提交 / 更新">
          <Box component="span" sx={{ fontFamily: MONO }}>
            {fmtTS(a.created_at)} / {fmtTS(a.updated_at)}
          </Box>
        </InfoRow>
      </SectionCard>

      <SectionCard title="申诉理由">
        <Box sx={{ px: 2, py: 1.5 }}>
          {a.statement !== '' ? (
            <Typography
              data-testid="appeal-statement"
              sx={{ fontSize: 14, lineHeight: 1.7, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
            >
              {a.statement}
            </Typography>
          ) : (
            <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>（未填写）</Typography>
          )}
        </Box>
      </SectionCard>

      <SectionCard title="AI 复核">
        <InfoRow label="结论">
          {apAI(a.ai_result)}
          {a.ai_conf > 0 ? ` · ${Math.round(a.ai_conf * 100)}%` : ''}
          {a.ai_model !== '' ? ` · ${a.ai_model}` : ''}
          {a.ai_cost !== '' ? ` · 开销 ${a.ai_cost}` : ''}
        </InfoRow>
        {a.ai_reason !== '' && (
          <InfoRow label="理由">
            <Box component="span" sx={{ whiteSpace: 'pre-wrap' }}>
              {a.ai_reason}
            </Box>
          </InfoRow>
        )}
        <InfoRow label="网页验证">
          {a.web_attempts} 次尝试 · 验证记录 {a.web_checks} 条
          {a.web_checks > 0 ? `（通过 ${a.web_passes}）` : ''}
        </InfoRow>
      </SectionCard>

      <SectionCard title="解禁码">
        <InfoRow label="解禁码">
          {a.code !== '' ? (
            <Box component="span" sx={{ fontFamily: MONO }}>
              {a.code}
            </Box>
          ) : a.has_code ? (
            '已签发'
          ) : (
            '未签发'
          )}
        </InfoRow>
        <InfoRow label="到期">
          {a.has_code || a.code !== ''
            ? a.code_expires > 0
              ? fmtTS(a.code_expires)
              : '无到期时间'
            : '—'}
        </InfoRow>
      </SectionCard>

      <SectionCard title="兑换记录">
        {a.redeems.length === 0 ? (
          <Typography sx={{ px: 2, py: 1.5, fontSize: 14, color: 'text.secondary' }}>
            暂无兑换
          </Typography>
        ) : (
          a.redeems.map((redeem, index) => (
            <InfoRow key={`${redeem.chat_id}-${redeem.at}-${index}`} label={`兑换 ${index + 1}`}>
              <Box component="span" sx={{ fontFamily: MONO }}>
                群 {redeem.chat_id} · uid {redeem.by_uid} · {fmtTS(redeem.at)}
              </Box>
            </InfoRow>
          ))
        )}
      </SectionCard>

      {a.detail_url !== '' && (
        <Box sx={{ px: 2, mb: 1.5 }}>
          <Button fullWidth variant="outlined" onClick={() => openLink(a.detail_url)}>
            打开申诉详情页
          </Button>
        </Box>
      )}

      {open ? (
        <Box
          ref={barRef}
          data-testid="appeal-action-bar"
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
            <Button
              size="small"
              variant="outlined"
              disabled={act.isPending}
              onClick={() => void approve()}
            >
              人工解除
            </Button>
            <Button
              size="small"
              variant="outlined"
              color="error"
              disabled={act.isPending}
              onClick={() => void reject()}
            >
              驳回
            </Button>
            {canIssueCode && (
              <Button
                size="small"
                variant="outlined"
                disabled={act.isPending}
                onClick={() => run('issue_code')}
              >
                直接签发解禁码
              </Button>
            )}
            {canRerun && (
              <Button
                size="small"
                variant="outlined"
                disabled={act.isPending}
                onClick={() => run('rerun')}
              >
                重跑 AI 复核
              </Button>
            )}
          </Box>
          <Typography sx={{ mt: 0.75, fontSize: 12, color: 'text.secondary', lineHeight: 1.5 }}>
            人工解除与 AI 撤销同效：解除禁言与冷判定限制；联合封禁只有主管理员能在此一并解除。
          </Typography>
        </Box>
      ) : (
        <Box sx={{ px: 2 }}>
          <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
            该申诉已结（{status.label}），无需再处理。
          </Typography>
        </Box>
      )}
    </Box>
  )
}
