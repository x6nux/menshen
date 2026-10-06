// 人机验证演示页（_w/demo）：同一页展示并校验四家验证方式。
//
// 仅测试用（服务端 captcha_demo=1 才可用）。只显示各家的 site key；
// 校验走服务端与入群验证同一条路径，所以这里通过即代表那条路也通。
import { Box, CircularProgress, Typography } from '@mui/material'
import { useCallback, useEffect, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api'
import type { CaptchaDemoData, CaptchaDemoProvider, WebRoute } from '../api'
import { mountCaptcha } from '../captcha'
import { collectSignals } from '../fingerprint'
import { errorText, InvalidState } from '../InvalidState'

const PROVIDER_LABEL: Record<string, string> = {
  turnstile: 'Cloudflare Turnstile',
  hcaptcha: 'hCaptcha',
  cap: 'Cap（内置）',
}

export function CaptchaDemoPage({ route }: { route: WebRoute }) {
  const [data, setData] = useState<CaptchaDemoData | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    getJSON<CaptchaDemoData>(route.path + '?json=1')
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

  if (error) return <InvalidState message={error} />
  if (!data) return <Placeholder />
  if (data.providers.length === 0)
    return <InvalidState message="演示页未配置任何验证方式（服务端 captcha_demo_keys 为空）。" />

  return (
    <Box data-testid="captcha-demo">
      <Typography sx={{ fontSize: 20, fontWeight: 700, mb: 0.5 }}>
        人机验证测试台
      </Typography>
      <Typography sx={{ fontSize: 13, color: 'text.secondary', mb: 2 }}>
        逐个完成下方验证，通过后服务端会向对应提供方核对并返回结果。
      </Typography>
      {data.providers.map((p) => (
        <ProviderCard key={p.provider} route={route} p={p} />
      ))}
    </Box>
  )
}

function ProviderCard({ route, p }: { route: WebRoute; p: CaptchaDemoProvider }) {
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const doneRef = useRef(false)

  const submit = useCallback(
    async (token: string) => {
      if (doneRef.current) return
      doneRef.current = true
      setSubmitting(true)
      try {
        const signals = await collectSignals()
        const resp = await postJSON<{ ok: boolean; msg: string }>(route.path, {
          provider: p.provider,
          token,
          signals,
        })
        setResult({ ok: resp.ok, msg: resp.msg })
      } catch (err) {
        setResult({ ok: false, msg: errorText(err) })
      } finally {
        setSubmitting(false)
      }
    },
    [route.path, p.provider],
  )

  return (
    <Box sx={{ bgcolor: 'background.paper', borderRadius: 2, p: 2, mb: 2 }}>
      <Typography sx={{ fontSize: 15, fontWeight: 700 }}>
        {PROVIDER_LABEL[p.provider] ?? p.provider}
      </Typography>
      <CaptchaWidget p={p} onToken={submit} />
      <Typography sx={{ fontSize: 12, color: 'text.secondary', wordBreak: 'break-all' }}>
        site key：{p.sitekey}
        {p.endpoint ? ` · 端点：${p.endpoint}` : ''}
      </Typography>
      {submitting && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 1 }}>
          <CircularProgress size={14} />
          <Typography sx={{ fontSize: 13 }}>正在校验…</Typography>
        </Box>
      )}
      {result && (
        <Typography
          sx={{ fontSize: 13.5, mt: 1, color: result.ok ? 'success.main' : 'error.main' }}
        >
          {result.ok ? '✅ ' : '❌ '}
          {result.msg}
        </Typography>
      )}
    </Box>
  )
}

/** CaptchaWidget 按提供方挂载组件，卸载时清理。 */
function CaptchaWidget({
  p,
  onToken,
}: {
  p: CaptchaDemoProvider
  onToken: (token: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!ref.current) return
    return mountCaptcha(ref.current, {
      provider: p.provider,
      sitekey: p.sitekey,
      endpoint: p.endpoint,
      onToken,
    })
  }, [p.provider, p.sitekey, p.endpoint, onToken])
  return <Box ref={ref} data-testid={`captcha-${p.provider}`} sx={{ my: 1.5 }} />
}

function Placeholder() {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
      <CircularProgress size={22} />
    </Box>
  )
}
