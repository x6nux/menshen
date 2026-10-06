// 桌面端主题：跟随系统深浅色。调色沿用项目已有的 buildMiniTheme（同一套
// 品牌色与 Telegram 风格回退），只把触控尺寸约束放宽——桌面用鼠标，不需要
// 44px 按钮与 52px 列表行。
import { createTheme } from '@mui/material/styles'
import type { Theme } from '@mui/material/styles'
import { buildMiniTheme } from '../theme'

export function buildAdminTheme(dark: boolean): Theme {
  const base = buildMiniTheme({ colorScheme: dark ? 'dark' : 'light', themeParams: {} })
  return createTheme(base, {
    typography: { fontSize: 14 },
    components: {
      MuiButton: {
        styleOverrides: {
          root: { minHeight: 36, fontSize: 14, px: 1.75 },
        },
      },
      MuiListItemButton: {
        styleOverrides: { root: { minHeight: 40 } },
      },
      MuiTableCell: {
        styleOverrides: { root: { fontSize: 13.5, py: 1 } },
      },
      MuiTextField: {
        defaultProps: { size: 'small' },
      },
    },
  })
}

/** prefersDark 读系统深浅色偏好；没有 matchMedia（测试）时按浅色。 */
export function prefersDark(): boolean {
  if (typeof window === 'undefined' || !window.matchMedia) return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

/** subscribeSystemTheme 订阅系统深浅色变化，返回取消订阅函数。 */
export function subscribeSystemTheme(cb: (dark: boolean) => void): () => void {
  if (typeof window === 'undefined' || !window.matchMedia) return () => {}
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const handler = (event: MediaQueryListEvent) => cb(event.matches)
  mq.addEventListener('change', handler)
  return () => mq.removeEventListener('change', handler)
}
