// Telegram themeParams → MUI theme（实施计划 3.1/3.4）。
// 无 SDK 或缺少某个颜色时用国内风浅色兜底；深色跟随 colorScheme 实时重建。
import { createTheme } from '@mui/material/styles'
import type { Theme } from '@mui/material/styles'

/** Telegram WebApp themeParams 的子集（未知键保留在索引签名里）。 */
export interface MiniThemeParams {
  bg_color?: string
  secondary_bg_color?: string
  text_color?: string
  hint_color?: string
  link_color?: string
  button_color?: string
  button_text_color?: string
  destructive_text_color?: string
  [key: string]: string | undefined
}

export interface BuildMiniThemeOptions {
  themeParams?: MiniThemeParams | null
  /** Telegram 的 colorScheme：'dark' 走深色板，其余（含空）走浅色板。 */
  colorScheme?: string | null
}

interface FallbackPalette {
  bg: string
  paper: string
  text: string
  hint: string
  button: string
  buttonText: string
  destructive: string
}

const LIGHT_FALLBACK: FallbackPalette = {
  bg: '#F5F6F7',
  paper: '#FFFFFF',
  text: '#1A1A1A',
  // 次要文字要满足正文 AA（白底对比度 ≈5.3:1）；#8A8A8E 只有约 3.5:1。
  hint: '#6B6B70',
  button: '#1677FF',
  buttonText: '#FFFFFF',
  destructive: '#FA5151',
}

// 深色兜底取 Telegram 桌面端深色主题的近似值，避免没带 themeParams 时白底黑字。
const DARK_FALLBACK: FallbackPalette = {
  bg: '#17212B',
  paper: '#232E3C',
  text: '#FFFFFF',
  hint: '#708499',
  button: '#5288C1',
  buttonText: '#FFFFFF',
  destructive: '#FF6B6B',
}

/** 成功/运行中（3.1 的语义色，Telegram 主题里没有对应项）。 */
export const SUCCESS_COLOR = '#07C160'
export const WARNING_COLOR = '#FF9F0A'

/** buildMiniTheme 按主题参数构造 MUI theme；缺省即浅色回退。 */
export function buildMiniTheme(options: BuildMiniThemeOptions = {}): Theme {
  const dark = options.colorScheme === 'dark'
  const fb = dark ? DARK_FALLBACK : LIGHT_FALLBACK
  const tp = options.themeParams ?? {}
  const pick = (key: keyof MiniThemeParams, fallback: string): string => tp[key] || fallback

  return createTheme({
    palette: {
      mode: dark ? 'dark' : 'light',
      background: {
        default: pick('bg_color', fb.bg),
        paper: pick('secondary_bg_color', fb.paper),
      },
      text: {
        primary: pick('text_color', fb.text),
        secondary: pick('hint_color', fb.hint),
      },
      primary: {
        main: pick('button_color', fb.button),
        contrastText: pick('button_text_color', fb.buttonText),
      },
      error: { main: pick('destructive_text_color', fb.destructive) },
      success: { main: SUCCESS_COLOR },
      warning: { main: WARNING_COLOR },
    },
    shape: { borderRadius: 12 },
    typography: {
      fontSize: 15,
      button: { fontSize: 15 },
    },
    components: {
      // 按钮：国内 App 风格——不转大写、圆角 10、触控高度 ≥44（3.1）。
      MuiButton: {
        defaultProps: { disableElevation: true },
        styleOverrides: {
          root: {
            textTransform: 'none',
            borderRadius: 10,
            minHeight: 44,
          },
        },
      },
      // 列表紧凑：行高只加在可点击行（ListItemButton）上。
      // 不覆盖 MuiListItem：MenuItem、Select 选项内部也用 ListItem，
      // 统一压行高会误伤菜单与下拉的高度。
      MuiList: {
        styleOverrides: {
          root: { paddingTop: 0, paddingBottom: 0 },
        },
      },
      MuiListItemButton: {
        styleOverrides: {
          root: { minHeight: 52 },
        },
      },
    },
  })
}
