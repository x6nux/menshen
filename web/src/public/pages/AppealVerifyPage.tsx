// 申诉验证页（_w/ap/<id>/<sig>）：展示处罚依据 + Turnstile + 采集浏览器
// 特征提交，换取解禁码。原服务端模板页的 React 重写。
import { Box, CircularProgress, Typography } from '@mui/material'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api'
import type { AppealData, WebRoute } from '../api'
import { collectSignals } from '../fingerprint'
import { errorText, InvalidState } from '../InvalidState'
import { loadTurnstile } from '../turnstile'

const LIMIT_LABEL: Record<string, string> = {
  join_profile: '进群资料审核限制',
  message: '消息判定处置',
  gban: '联合封禁 · 全平台',
  gban_own: '联合封禁 · 本 bot 名下群组',
}

const cardSx = { bgcolor: 'background.paper', borderRadius: 2, p: 2, mb: 2 }
const sectionSx = { fontSize: 15, fontWeight: 700, mb: 1 }

export function AppealVerifyPage({ route }: { route: WebRoute }) {
  const [data, setData] = useState<AppealData | null>(null)
  const [error, setError] = useState('')
  const [result, setResult] = useState<{ ok: boolean; code?: string; msg: string } | null>(null)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    let alive = true
    getJSON<AppealData>(route.path + '?json=1')
      .then((d) => {
        if (alive) setData(d)
      })
      .catch((err: unknown) => {
        if (alive) setError(errorText(err))
      })
    return () => {
      alive = false
    }
  }, [route.path])

  const submit = useCallback(
    async (token: string) => {
      setSubmitting(true)
      try {
        const signals = await collectSignals()
        const resp = await postJSON<{ ok: boolean; code?: string; msg: string }>(route.path, {
          token,
          signals,
        })
        setResult(resp)
      } catch (err) {
        setResult({ ok: false, msg: errorText(err) })
      } finally {
        setSubmitting(false)
      }
    },
    [route.path],
  )

  if (error) return <InvalidState message={error} />
  if (!data) return <Placeholder />
  return (
    <Box data-testid="appeal-verify">
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
        为防止滥用，本页会记录你的 IP 与浏览器特征，仅用于反垃圾审核，保留 {data.days} 天。
      </Box>

      <Section title="为什么被限制">
        {data.limits.length > 0 ? (
          <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
            {data.limits.map((l, i) => (
              <Box component="li" key={i} sx={{ mb: 1.25, fontSize: 14, lineHeight: 1.7 }}>
                {LIMIT_LABEL[l.type] ?? l.type}
                {l.chat_id ? ` · 群 ${l.chat_id}` : ''}
                {l.time ? ` · ${l.time}` : ''}
                {l.reason && <Sub>理由：{l.reason}</Sub>}
                {l.text && <Sub>原消息：{l.text}</Sub>}
              </Box>
            ))}
          </Box>
        ) : (
          <Typography sx={{ fontSize: 14 }}>
            本 bot 名下已查不到仍在生效的限制，可能已经解除。
          </Typography>
        )}
      </Section>

      <Section title="账号信息">
        <Typography sx={{ fontSize: 14 }}>
          用户 ID：<Mono>{data.account.uid}</Mono>
          {data.account.name && (
            <>
              <br />
              判定时的昵称：{data.account.name}
            </>
          )}
        </Typography>
        <Tip>
          这些是 bot 判定时记下的资料。广告号的昵称、用户名或简介里常写着推广、收益承诺或引流话术，
          即使某条消息看起来无害也会被整体判为广告号——把资料改干净再申诉，通过率会高很多。
        </Tip>
      </Section>

      <Section title="你在群里的发言（留底）">
        {data.messages.length > 0 ? (
          <Messages messages={data.messages} />
        ) : (
          <Typography sx={{ fontSize: 14 }}>
            没有查到你的发言留底（可能已过保留期，或你还没在本 bot 的群里发过言）。
          </Typography>
        )}
        <Tip>
          以上是 bot 留底的你的发言，最多显示最近 100 条、每条 300 字；留底保留 {data.days} 天。
        </Tip>
      </Section>

      <Section title="AI 复核结论">
        <Typography sx={{ fontSize: 14 }}>
          {data.ai.label}
          {data.ai.conf > 0 && `（置信度 ${Math.round(data.ai.conf * 100)}%`}
          {data.ai.conf > 0 && data.ai.model ? ` · ${data.ai.model}）` : data.ai.conf > 0 ? '）' : ''}
          {data.ai.conf <= 0 && data.ai.model ? `（${data.ai.model}）` : ''}
        </Typography>
        {data.ai.reason && <Sub>{data.ai.reason}</Sub>}
        {data.ai.statement && <Sub>你的申诉理由：{data.ai.statement}</Sub>}
      </Section>

      <Section title="人机验证">
        <Typography sx={{ fontSize: 14, mb: 1.5 }}>
          完成下方的人机验证后，会给你一个<b>解禁码</b>，把它交给群管理员即可解除限制。
        </Typography>
        {data.sitekey ? (
          <TurnstileWidget sitekey={data.sitekey} cdata={data.cdata} onToken={submit} />
        ) : (
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
            本页未配置人机验证，请联系群管理员处理。
          </Typography>
        )}
        <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 1 }}>
          验证组件加载不出来时，请用系统浏览器打开本页。
        </Typography>
        <ResultArea submitting={submitting} result={result} />
      </Section>
    </Box>
  )
}

/** TurnstileWidget 显式渲染；token 到达即回调提交。 */
function TurnstileWidget({
  sitekey,
  cdata,
  onToken,
}: {
  sitekey: string
  cdata: string
  onToken: (token: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    let cancelled = false
    let widgetID: string | null = null
    void loadTurnstile().then((ts) => {
      if (cancelled || !ts || !ref.current) return
      widgetID = ts.render(ref.current, {
        sitekey,
        action: 'appeal',
        cdata,
        callback: onToken,
      })
    })
    return () => {
      cancelled = true
      if (widgetID) window.turnstile?.remove(widgetID)
    }
  }, [sitekey, cdata, onToken])
  return <Box ref={ref} data-testid="turnstile" sx={{ my: 1 }} />
}

function ResultArea({
  submitting,
  result,
}: {
  submitting: boolean
  result: { ok: boolean; code?: string; msg: string } | null
}) {
  if (submitting)
    return (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 1.5 }}>
        <CircularProgress size={16} />
        <Typography sx={{ fontSize: 14 }}>正在验证…</Typography>
      </Box>
    )
  if (!result) return null
  if (result.ok && result.code)
    return (
      <Box sx={{ mt: 1.5 }}>
        <Typography sx={{ fontSize: 14 }}>
          解禁码：
          <Box
            component="span"
            sx={{
              fontFamily: 'ui-monospace, Menlo, monospace',
              fontSize: 18,
              letterSpacing: 1,
              px: 1,
              py: 0.5,
              bgcolor: 'success.main',
              color: '#fff',
              borderRadius: 1,
            }}
          >
            {result.code}
          </Box>
        </Typography>
        <Typography sx={{ fontSize: 14, mt: 0.5 }}>{result.msg}</Typography>
      </Box>
    )
  return (
    <Typography sx={{ fontSize: 14, color: 'error.main', mt: 1.5 }}>{result.msg}</Typography>
  )
}

/** Messages 把发言留底按群分组（接口按群顺序扁平返回）。 */
function Messages({ messages }: { messages: AppealData['messages'] }) {
  const groups = useMemo(() => {
    const out: { title: string; items: AppealData['messages'] }[] = []
    for (const m of messages) {
      const last = out[out.length - 1]
      if (last && last.title === m.title) last.items.push(m)
      else out.push({ title: m.title, items: [m] })
    }
    return out
  }, [messages])
  return (
    <>
      {groups.map((g, i) => (
        <Box key={i} sx={{ mb: 1 }}>
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>群 {g.title}</Typography>
          <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
            {g.items.map((m, j) => (
              <Box
                component="li"
                key={j}
                sx={{ mb: 1, fontSize: 13.5, lineHeight: 1.6, wordBreak: 'break-word' }}
              >
                <Mono>{m.time}</Mono> {m.text}
              </Box>
            ))}
          </Box>
        </Box>
      ))}
    </>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Box sx={cardSx}>
      <Typography sx={sectionSx}>{title}</Typography>
      {children}
    </Box>
  )
}

function Sub({ children }: { children: React.ReactNode }) {
  return (
    <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 0.5, wordBreak: 'break-word' }}>
      {children}
    </Typography>
  )
}

function Tip({ children }: { children: React.ReactNode }) {
  return (
    <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 1, lineHeight: 1.7 }}>
      {children}
    </Typography>
  )
}

function Mono({ children }: { children: React.ReactNode }) {
  return <Box component="span" sx={{ fontFamily: 'ui-monospace, Menlo, monospace' }}>{children}</Box>
}

function Placeholder() {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
      <CircularProgress size={22} />
    </Box>
  )
}
