// TurnstileWidget：申诉验证页与申诉验证测试台共用的 Turnstile 组件。
//
// 与服务端 verifyTurnstile 的约定必须逐字对齐：
//   - action 固定 'appeal'；
//   - cData 是官方显式渲染的参数名（驼峰）；写成 cdata 会被组件静默忽略、
//     令牌里不带 cdata，服务端 cdata 校验失败。
//
// refresh-expired 用 manual：默认 auto 会在 5 分钟后自动重发令牌并再次
// 触发提交，用户无操作也会消耗失败次数（满 5 次申诉自动结案）。
// 失败后的重试由页面驱动：resetKey 变化时 reset 重新挑战，重试节奏由
// 用户主动点击控制，而非组件自动循环。
import { Box } from '@mui/material'
import { useEffect, useRef } from 'react'
import { loadTurnstile } from './turnstile'

export function TurnstileWidget({
  sitekey,
  cdata,
  resetKey,
  onToken,
  onError,
}: {
  sitekey: string
  cdata: string
  /** resetKey 变化时重置组件：上一枚令牌已作废，需要重新挑战。 */
  resetKey: number
  onToken: (token: string) => void
  /** 组件层失败：加载失败 / 挑战出错 / 令牌过期。 */
  onError: (msg: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const idRef = useRef<string | null>(null)

  useEffect(() => {
    let cancelled = false
    void loadTurnstile().then((ts) => {
      if (cancelled || !ts || !ref.current) return
      idRef.current = ts.render(ref.current, {
        sitekey,
        action: 'appeal',
        cData: cdata,
        'refresh-expired': 'manual',
        callback: onToken,
        'error-callback': (code) =>
          onError(`验证组件出错${code ? `（${code}）` : ''}，请重试。`),
        'expired-callback': () => onError('验证已过期，请点击重试。'),
      })
    })
    return () => {
      cancelled = true
      if (idRef.current) window.turnstile?.remove(idRef.current)
      idRef.current = null
    }
  }, [sitekey, cdata, onToken, onError])

  useEffect(() => {
    if (resetKey > 0 && idRef.current) window.turnstile?.reset(idRef.current)
  }, [resetKey])

  return <Box ref={ref} data-testid="turnstile" sx={{ my: 1 }} />
}
