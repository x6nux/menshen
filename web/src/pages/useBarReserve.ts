// 底部固定操作栏高度实测：内容区按它预留 padding，避免被操作栏遮住。
// 单独成文件是因为 hooks 与组件混在一个导出文件里会破坏 Fast Refresh。
import { useCallback, useEffect, useState } from 'react'

/**
 * useBarReserve 实测底部固定操作栏高度：resize 时更新，取实测值与兜底值的
 * 较大者。返回的 barRef 是回调 ref——操作栏常在数据加载后才挂载，回调 ref
 * 能在挂载/卸载时正确重绑 ResizeObserver；jsdom 等无 RO 环境直接用兜底值。
 */
export function useBarReserve(fallback: number) {
  const [barEl, setBarEl] = useState<HTMLDivElement | null>(null)
  const [measured, setMeasured] = useState(0)

  useEffect(() => {
    if (barEl === null || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver((entries) => {
      const height = entries[0]?.contentRect.height ?? 0
      if (height > 0) setMeasured(height)
    })
    observer.observe(barEl)
    return () => observer.disconnect()
  }, [barEl])

  const barRef = useCallback((node: HTMLDivElement | null) => setBarEl(node), [])
  return { barRef, reserved: Math.max(measured, fallback) }
}
