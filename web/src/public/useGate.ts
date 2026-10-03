// useGate 是查看类页面的通用两段式：GET 拿门槛凭据，点击后 POST 凭据换内容。
//
// 原文查看页与申诉详情页共用；内容接口只有在凭据验签且未过期时才返回。
import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from './api'
import type { Gate, WebRoute } from './api'
import { errorText } from './InvalidState'

export function useGate<T>(route: WebRoute) {
  const [gate, setGate] = useState<Gate | null>(null)
  const [view, setView] = useState<T | null>(null)
  const [error, setError] = useState<string>('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    let alive = true
    getJSON<{ gate: Gate }>(route.path + '?json=1')
      .then((r) => {
        if (alive) setGate(r.gate)
      })
      .catch((err: unknown) => {
        if (alive) setError(errorText(err))
      })
    return () => {
      alive = false
    }
  }, [route.path])

  const reveal = useCallback(async () => {
    if (!gate) return
    setLoading(true)
    try {
      const r = await postJSON<{ view: T }>(route.path, { e: gate.exp, k: gate.k })
      setView(r.view)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setLoading(false)
    }
  }, [gate, route.path])

  return { gate, view, error, loading, reveal }
}
