// 申诉验证测试台（_w/apdemo）：用申诉页同一条校验路径做真机自测。
//
// 与人机验证测试台（_w/demo）同一套约定：仅 captcha_demo=1 时可用；
// 页面只含 site key（公开信息），校验走服务端与申诉页同一条
// verifyTurnstile——config 的 Turnstile 密钥、action / cdata / hostname
// 全部照查，所以这里通过即代表申诉那条路也通。申诉页只回一句
// 「验证未通过」，密钥、域名白名单或 cdata 绑定出了问题，具体原因
// 全靠这页显示。
import { Box, Button, CircularProgress, Typography } from '@mui/material'
import { useCallback, useEffect, useRef, useState } from 'react'
import { getJSON, postJSON } from '../api'
import type { AppealDemoData, WebRoute } from '../api'
import { collectSignals } from '../fingerprint'
import { errorText, InvalidState } from '../InvalidState'
import { TurnstileWidget } from '../TurnstileWidget'

export function AppealDemoPage({ route }: { route: WebRoute }) {
  const [data, setData] = useState<AppealDemoData | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    getJSON<AppealDemoData>(route.path + '?json=1')
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
  if (!data.sitekey)
    return (
      <InvalidState message="服务端未配置申诉验证的 Turnstile 密钥（config.yaml 的 turnstile_site_key / turnstile_secret）。" />
    )

  return (
    <Box data-testid="appeal-demo">
      <Typography sx={{ fontSize: 20, fontWeight: 700, mb: 0.5 }}>
        申诉验证测试台
      </Typography>
      <Typography sx={{ fontSize: 13, color: 'text.secondary', mb: 2 }}>
        与用户申诉页同一条校验路径（action=appeal、cdata 绑定、hostname 核对），
        通过即代表申诉验证也通；失败原因会显示在下方。
      </Typography>
      <Box sx={{ bgcolor: 'background.paper', borderRadius: 2, p: 2 }}>
        <Typography sx={{ fontSize: 15, fontWeight: 700 }}>Cloudflare Turnstile</Typography>
        <DemoCard route={route} sitekey={data.sitekey} cdata={data.cdata} />
      </Box>
    </Box>
  )
}

function DemoCard({
  route,
  sitekey,
  cdata,
}: {
  route: WebRoute
  sitekey: string
  cdata: string
}) {
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [resetKey, setResetKey] = useState(0)
  const busyRef = useRef(false)

  const submit = useCallback(
    async (token: string) => {
      if (busyRef.current) return
      busyRef.current = true
      setSubmitting(true)
      try {
        const signals = await collectSignals()
        const resp = await postJSON<{ ok: boolean; msg: string }>(route.path, {
          token,
          signals,
        })
        setResult(resp)
      } catch (err) {
        setResult({ ok: false, msg: errorText(err) })
      } finally {
        busyRef.current = false
        setSubmitting(false)
      }
    },
    [route.path],
  )

  const onToken = useCallback((token: string) => void submit(token), [submit])
  const onError = useCallback((msg: string) => {
    setResult((prev) => (prev?.ok ? prev : { ok: false, msg }))
  }, [])
  const retry = useCallback(() => {
    setResult(null)
    setResetKey((k) => k + 1)
  }, [])

  return (
    <>
      <TurnstileWidget
        sitekey={sitekey}
        cdata={cdata}
        resetKey={resetKey}
        onToken={onToken}
        onError={onError}
      />
      <Typography sx={{ fontSize: 12, color: 'text.secondary', wordBreak: 'break-all' }}>
        site key：{sitekey} · cdata：{cdata}
      </Typography>
      {submitting && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 1 }}>
          <CircularProgress size={14} />
          <Typography sx={{ fontSize: 13 }}>正在校验…</Typography>
        </Box>
      )}
      {result && (
        <Box sx={{ mt: 1 }}>
          <Typography
            sx={{ fontSize: 13.5, color: result.ok ? 'success.main' : 'error.main' }}
          >
            {result.ok ? '✅ ' : '❌ '}
            {result.msg}
          </Typography>
          {!result.ok && (
            <Button size="small" variant="outlined" sx={{ mt: 1 }} onClick={retry}>
              重试验证
            </Button>
          )}
        </Box>
      )}
    </>
  )
}

function Placeholder() {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
      <CircularProgress size={22} />
    </Box>
  )
}
