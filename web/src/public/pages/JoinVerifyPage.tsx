// 入群验证页（_w/jv/<id>/<sig>）：新人进群后的自助门槛。
//
// 页面加载时取回提供方与 sitekey，渲染对应的验证码组件；拿到 token 后
// 连同浏览器特征提交，服务端向提供方核对通过即解除禁言。
import { Box, CircularProgress, Typography } from '@mui/material'
import { useCallback, useEffect, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api'
import type { JoinVerifyData, WebRoute } from '../api'
import { mountCaptcha } from '../captcha'
import { collectSignals } from '../fingerprint'
import { errorText, InvalidState } from '../InvalidState'

const PROVIDER_LABEL: Record<string, string> = {
  turnstile: 'Cloudflare Turnstile',
  recaptcha: 'Google reCAPTCHA',
  hcaptcha: 'hCaptcha',
  cap: 'Cap',
}

export function JoinVerifyPage({ route }: { route: WebRoute }) {
  const [data, setData] = useState<JoinVerifyData | null>(null)
  const [error, setError] = useState('')
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const doneRef = useRef(false)

  useEffect(() => {
    let alive = true
    getJSON<JoinVerifyData>(route.path + '?json=1')
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
      // 组件可能在重试时重复回调；一次提交已经有结论就不再重复提交。
      if (doneRef.current) return
      doneRef.current = true
      setSubmitting(true)
      try {
        const signals = await collectSignals()
        const resp = await postJSON<{ ok: boolean; msg: string }>(route.path, {
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
    [route.path],
  )

  if (error) return <InvalidState message={error} />
  if (!data) return <Placeholder />
  if (data.status !== 'pending')
    return <InvalidState message="这张验证已经完成或已失效，请重新加入群聊。" />
  if (!data.provider || !data.sitekey)
    return <InvalidState message="本群未配置人机验证，请联系群管理员。" />

  return (
    <Box data-testid="join-verify">
      <Box sx={{ bgcolor: 'background.paper', borderRadius: 2, p: 2, mb: 2 }}>
        <Typography sx={{ fontSize: 18, fontWeight: 700, mb: 1 }}>
          {data.chat ? `加入「${data.chat}」` : '入群验证'}
        </Typography>
        <Typography sx={{ fontSize: 14, lineHeight: 1.8 }}>
          为防广告机器人，请先完成人机验证。通过后即可在群里发言；
          {data.minutes > 0
            ? `请在 ${data.minutes} 分钟内完成，否则会被移出群聊（可重新加入）。`
            : '未通过验证前无法发言。'}
        </Typography>
      </Box>

      <Box sx={{ bgcolor: 'background.paper', borderRadius: 2, p: 2 }}>
        <Typography sx={{ fontSize: 15, fontWeight: 700, mb: 1.5 }}>
          人机验证 · {PROVIDER_LABEL[data.provider] ?? data.provider}
        </Typography>
        <CaptchaWidget data={data} onToken={submit} />
        <Typography sx={{ fontSize: 13, color: 'text.secondary', mt: 1 }}>
          验证组件加载不出来时，请用系统浏览器打开本页。
        </Typography>
        <ResultArea submitting={submitting} result={result} />
      </Box>

      <Typography sx={{ fontSize: 12, color: 'text.secondary', mt: 2, lineHeight: 1.7 }}>
        本页会记录你的 IP 与浏览器特征，仅用于反垃圾审核。
      </Typography>
    </Box>
  )
}

/** CaptchaWidget 按提供方挂载验证码组件，卸载时清理。 */
function CaptchaWidget({
  data,
  onToken,
}: {
  data: JoinVerifyData
  onToken: (token: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!ref.current) return
    return mountCaptcha(ref.current, {
      provider: data.provider,
      sitekey: data.sitekey,
      endpoint: data.endpoint,
      onToken,
    })
  }, [data.provider, data.sitekey, data.endpoint, onToken])
  return <Box ref={ref} data-testid="captcha" sx={{ my: 1 }} />
}

function ResultArea({
  submitting,
  result,
}: {
  submitting: boolean
  result: { ok: boolean; msg: string } | null
}) {
  if (submitting)
    return (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 1.5 }}>
        <CircularProgress size={16} />
        <Typography sx={{ fontSize: 14 }}>正在验证…</Typography>
      </Box>
    )
  if (!result) return null
  return (
    <Typography
      sx={{ fontSize: 14, mt: 1.5, color: result.ok ? 'success.main' : 'error.main' }}
    >
      {result.msg}
    </Typography>
  )
}

function Placeholder() {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
      <CircularProgress size={22} />
    </Box>
  )
}
