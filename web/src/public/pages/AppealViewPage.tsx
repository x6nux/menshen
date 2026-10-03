// 申诉详情页（_w/apv/<id>/<sig>）：左栏账号资料与留底，右栏申诉单复核。
import { Box, Button, CircularProgress, Typography } from '@mui/material'
import type { AppealDossierView, WebRoute } from '../api'
import { InvalidState } from '../InvalidState'
import { useGate } from '../useGate'

const cardSx = { bgcolor: 'background.paper', borderRadius: 2, p: 2, mb: 2 }
const titleSx = { fontSize: 15, fontWeight: 700, mb: 1 }

export function AppealViewPage({ route }: { route: WebRoute }) {
  const { gate, view, error, loading, reveal } = useGate<AppealDossierView>(route)

  if (error) return <InvalidState message={error} />
  if (view) return <AppealContent view={view} />
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
          链接只对持有者有效；查看凭据 5 分钟内有效。
        </Typography>
      </Box>
    </>
  )
}

function AppealContent({ view }: { view: AppealDossierView }) {
  return (
    <Box data-testid="appeal-view">
      <WarnCard text="敏感内容，请勿转发。以下为账号资料、留底与申诉处理详情。" />
      <Box
        sx={{
          display: 'flex',
          gap: 2,
          alignItems: 'flex-start',
          flexWrap: 'wrap',
        }}
      >
        <Box sx={{ flex: '1 1 420px', minWidth: 0 }}>
          <MainColumn view={view} />
        </Box>
        <Box sx={{ flex: '1 1 380px', minWidth: 0, maxWidth: { lg: 520 } }}>
          <SideColumn view={view} />
        </Box>
      </Box>
    </Box>
  )
}

function MainColumn({ view }: { view: AppealDossierView }) {
  return (
    <>
      <Box sx={cardSx}>
        <Typography sx={{ fontSize: 13.5, lineHeight: 1.9 }}>
          申诉单：<Mono>#{view.id}</Mono> ｜ 状态：{view.status}
          <br />
          申诉人：<Mono>{view.uid}</Mono>
          {view.u_name && `（${view.u_name}）`} ｜ 提交时间：{view.created}
          <br />
          申诉理由：{view.statement || '（未填写）'}
        </Typography>
      </Box>
      <Box sx={cardSx}>
        <Typography sx={titleSx}>AI 复核</Typography>
        <Typography sx={{ fontSize: 13.5, lineHeight: 1.9 }}>
          {view.ai_result || '（无结论）'}
          {view.ai_model && ` ｜ 模型 `}
          {view.ai_model && <Mono>{view.ai_model}</Mono>}
          {view.ai_conf > 0 && ` ｜ 置信度 ${Math.round(view.ai_conf)}%`}
        </Typography>
        {view.ai_reason && (
          <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 0.5 }}>
            理由：{view.ai_reason}
          </Typography>
        )}
        <Typography sx={{ ...titleSx, mt: 2 }}>网页验证</Typography>
        <Typography sx={{ fontSize: 13.5 }}>尝试次数：{view.web_attempts}</Typography>
        {view.code && (
          <Typography sx={{ fontSize: 13.5, mt: 0.5 }}>
            解禁码：<Mono>{view.code}</Mono>
            {view.code_expires && `（有效期至 ${view.code_expires}）`}
          </Typography>
        )}
        <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 0.5 }}>
          验证与发言记录见资料栏。
        </Typography>
      </Box>
    </>
  )
}

function SideColumn({ view }: { view: AppealDossierView }) {
  return (
    <>
      <Box sx={cardSx}>
        <Typography sx={titleSx}>账号信息</Typography>
        <Typography sx={{ fontSize: 13.5, lineHeight: 1.9 }}>
          用户 ID：<Mono>{view.uid}</Mono>
          {view.u_name && (
            <>
              <br />
              判定时昵称：{view.u_name}
            </>
          )}
          <br />
          所属 bot：{view.bot}
          {view.first_seen && (
            <>
              <br />
              首次见到：{view.first_seen}
            </>
          )}
          {view.joined && (
            <>
              <br />
              最早入群：{view.joined}
            </>
          )}
          {view.last_msg && (
            <>
              <br />
              最近发言：{view.last_msg}
            </>
          )}
          <br />
          群内发言：{view.msgs} 条 ｜ 历史命中：{view.hits} 次
          <br />
          涉及群组：{view.chats} 个
          {view.gban && (
            <>
              <br />
              联合封禁：{view.gban}
            </>
          )}
          <br />
          生效中限制：{view.limits.length} 条
        </Typography>
      </Box>

      <PenaltyList title="当前生效限制" items={view.limits} active />
      <PenaltyList title={`历史处罚（${view.penalties.length} 条）`} items={view.penalties} />

      {view.history.length > 0 && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>群内留底发言（最近 {view.history_count} 条）</Typography>
          <HistoryList items={view.history} />
          {view.history_more.length > 0 && (
            <Box component="details" sx={{ mt: 1 }}>
              <summary style={{ cursor: 'pointer', fontSize: 13 }}>
                展开其余 {view.history_more.length} 条
              </summary>
              <HistoryList items={view.history_more} />
            </Box>
          )}
        </Box>
      )}

      {view.logs.length > 0 && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>判定流水（{view.logs.length} 条）</Typography>
          <List>
            {view.logs.map((l) => (
              <li key={l.id}>
                <Mono>{l.time}</Mono> {l.chat} · {l.verdict} {Math.round(l.confidence * 100)}% →{' '}
                {l.action_label || l.action}
                {l.reason && <Sub>理由：{l.reason}</Sub>}
              </li>
            ))}
          </List>
        </Box>
      )}

      {view.checks.length > 0 && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>网页验证记录（{view.checks.length} 条）</Typography>
          <List>
            {view.checks.map((c, i) => (
              <li key={i}>
                <Mono>{c.time}</Mono> {c.result}
                {(c.ip || c.fp) && (
                  <Sub>
                    IP：<Mono>{c.ip || '—'}</Mono>
                    {c.fp && (
                      <>
                        {' ｜ 指纹：'}
                        <Mono>{c.fp}</Mono>
                      </>
                    )}
                  </Sub>
                )}
                {c.flags && <Sub>信号：{c.flags}</Sub>}
                {c.ua && <Sub>UA：{c.ua}</Sub>}
              </li>
            ))}
          </List>
        </Box>
      )}

      {(view.strong.length > 0 || view.weak.length > 0) && (
        <Box sx={cardSx}>
          <Typography sx={titleSx}>关联账号</Typography>
          {view.strong.length > 0 && (
            <>
              <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 0.5 }}>
                强关联（同指纹）
              </Typography>
              <Related items={view.strong} />
            </>
          )}
          {view.weak.length > 0 && (
            <>
              <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 0.5 }}>
                弱关联（30 天内同 IP，仅供参考）
              </Typography>
              <Related items={view.weak} />
            </>
          )}
        </Box>
      )}
    </>
  )
}

function PenaltyList({
  title,
  items,
  active,
}: {
  title: string
  items: AppealDossierView['limits']
  active?: boolean
}) {
  if (items.length === 0) return null
  return (
    <Box sx={cardSx}>
      <Typography sx={titleSx}>{title}</Typography>
      <List>
        {items.map((p, i) => (
          <li key={i}>
            <Badge active={active}>{active ? '生效中' : '历史'}</Badge> {p.label || p.type}
            {p.chat && ` · ${p.chat}`}
            {p.time && ` `}
            {p.time && <Mono>{p.time}</Mono>}
            {p.reason && <Sub>理由：{p.reason}</Sub>}
            {p.text && <Sub>原消息：{p.text}</Sub>}
          </li>
        ))}
      </List>
    </Box>
  )
}

function HistoryList({ items }: { items: AppealDossierView['history'] }) {
  return (
    <List>
      {items.map((h, i) => (
        <li key={i} style={h.blocked ? { background: 'rgba(250,81,81,0.08)', borderRadius: 4 } : undefined}>
          <Mono>{h.time}</Mono>
          {h.chat && <span style={{ color: '#888' }}> {h.chat}</span>}
          <div style={{ wordBreak: 'break-word' }}>
            {h.text}
            {h.mark && <b> ← {h.mark}</b>}
          </div>
        </li>
      ))}
    </List>
  )
}

function Related({ items }: { items: AppealDossierView['strong'] }) {
  return (
    <List>
      {items.map((r) => (
        <li key={r.uid}>
          <Mono>{r.uid}</Mono> {r.mark}
        </li>
      ))}
    </List>
  )
}

function List({ children }: { children: React.ReactNode }) {
  return <Box component="ul" sx={{ m: 0, pl: 2.5, '& li': { fontSize: 13.5, mb: 1 } }}>{children}</Box>
}

function Sub({ children }: { children: React.ReactNode }) {
  return <Typography sx={{ fontSize: 12.5, color: 'text.secondary', mt: 0.25 }}>{children}</Typography>
}

function Badge({ children, active }: { children: React.ReactNode; active?: boolean }) {
  return (
    <Box
      component="span"
      sx={{
        display: 'inline-block',
        fontSize: 11,
        px: 0.75,
        borderRadius: 1,
        bgcolor: active ? 'error.light' : 'action.selected',
        verticalAlign: '1px',
      }}
    >
      {children}
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
