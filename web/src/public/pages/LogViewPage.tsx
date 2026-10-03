// 原文查看页（_w/v/<id>/<sig>）：门槛凭据 → 判定记录详情。
import { Box, Button, CircularProgress, Typography } from '@mui/material'
import type { LogRecordView, WebRoute } from '../api'
import { InvalidState } from '../InvalidState'
import { useGate } from '../useGate'

const cardSx = { bgcolor: 'background.paper', borderRadius: 2, p: 2, mb: 2 }
const titleSx = { fontSize: 15, fontWeight: 700, mb: 1 }
const tdSx = { borderBottom: '1px solid', borderColor: 'divider', py: 0.75, pr: 1.5, fontSize: 13.5 }

export function LogViewPage({ route }: { route: WebRoute }) {
  const { gate, view, error, loading, reveal } = useGate<LogRecordView>(route)

  if (error) return <InvalidState message={error} />
  if (view) return <LogContent view={view} />
  if (!gate)
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
        <CircularProgress size={22} />
      </Box>
    )
  return (
    <>
      <WarnCard text={gate.warn} />
      <Box sx={cardSx}>
        <Button variant="contained" disabled={loading} onClick={() => void reveal()}>
          {loading ? '正在读取…' : '查看内容'}
        </Button>
        <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 1 }}>
          链接只对持有者有效；查看凭据 {Math.round((gate.ttl_seconds || 300) / 60)} 分钟内有效。
        </Typography>
      </Box>
    </>
  )
}

function LogContent({ view }: { view: LogRecordView }) {
  const r = view.record
  return (
    <Box data-testid="log-view">
      <WarnCard text="敏感内容，请勿转发。页面仅供管理员核对判定依据。" />
      <Box sx={cardSx}>
        <Typography sx={{ fontSize: 13.5, lineHeight: 1.9 }}>
          记录：<Mono>#{r.id}</Mono> ｜ 时间：{r.created}
          <br />
          用户：<Mono>{r.user_id}</Mono>
          {r.user_name && `（${r.user_name}）`}
          <br />
          群组：{r.chat}
          <br />
          入群时间：{view.member.joined || '未知'} ｜ 群内发言：{view.member.msgs} ｜ 历史命中：
          {view.member.hits}
        </Typography>
      </Box>

      <Box sx={cardSx}>
        <Typography sx={titleSx}>判定</Typography>
        <Typography sx={{ fontSize: 13.5, lineHeight: 1.9 }}>
          结论：{r.verdict} ｜ 类型：{r.kind || '—'} ｜ 置信度：
          {Math.round(r.confidence * 100)}%
          <br />
          来源：{r.decider}
          {r.reason && ` ｜ 理由：${r.reason}`}
          <br />
          处置：{r.action_label || r.action}
        </Typography>
        <Typography sx={{ ...titleSx, mt: 1.5 }}>被拦原文</Typography>
        <Box
          component="pre"
          sx={{
            m: 0,
            p: 1.25,
            bgcolor: 'action.hover',
            borderRadius: 1,
            fontSize: 13,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            fontFamily: 'ui-monospace, Menlo, monospace',
          }}
        >
          {r.text}
        </Box>
      </Box>

      {view.history.length > 0 && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>该群最近留底（{view.history.length} 条）</Typography>
          <Box component="table" sx={{ width: '100%', borderCollapse: 'collapse' }}>
            <tbody>
              {view.history.map((h) => (
                <Box component="tr" key={h.message_id} sx={h.blocked ? { bgcolor: 'error.light' } : undefined}>
                  <Box component="td" sx={{ ...tdSx, color: 'text.secondary', whiteSpace: 'nowrap' }}>
                    {h.time}
                  </Box>
                  <Box component="td" sx={tdSx}>
                    {h.text}
                    {h.blocked && <b> ← 被拦</b>}
                  </Box>
                </Box>
              ))}
            </tbody>
          </Box>
        </Box>
      )}

      {view.logs.length > 0 && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>该群最近判定记录（{view.logs.length} 条）</Typography>
          <Box component="table" sx={{ width: '100%', borderCollapse: 'collapse' }}>
            <tbody>
              {view.logs.map((l) => (
                <Box component="tr" key={l.id}>
                  <Box component="td" sx={tdSx}>
                    <Mono>#{l.id}</Mono>
                  </Box>
                  <Box component="td" sx={{ ...tdSx, color: 'text.secondary', whiteSpace: 'nowrap' }}>
                    {l.time}
                  </Box>
                  <Box component="td" sx={tdSx}>
                    {l.verdict} {Math.round(l.confidence * 100)}%
                  </Box>
                  <Box component="td" sx={tdSx}>
                    {l.action_label || l.action}
                  </Box>
                </Box>
              ))}
            </tbody>
          </Box>
        </Box>
      )}
    </Box>
  )
}

function WarnCard({ text }: { text: string }) {
  return (
    <Box
      sx={{
        bgcolor: 'warning.main',
        color: '#000',
        borderRadius: 2,
        p: 1.5,
        mb: 2,
        fontSize: 13,
        lineHeight: 1.7,
      }}
    >
      {text}
    </Box>
  )
}

function Mono({ children }: { children: React.ReactNode }) {
  return <Box component="span" sx={{ fontFamily: 'ui-monospace, Menlo, monospace' }}>{children}</Box>
}
