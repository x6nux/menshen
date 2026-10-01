import { ThemeProvider } from '@mui/material/styles'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { buildMiniTheme } from '../theme'
import { Badge } from './Badge'
import type { BadgeTone } from './Badge'

describe('Badge', () => {
  it('渲染文案并带语义色调', () => {
    render(<Badge tone="ok">运行中</Badge>)
    expect(screen.getByText('运行中')).toHaveAttribute('data-tone', 'ok')
  })

  it('支持 no/warn/neutral 三种标记', () => {
    render(
      <>
        <Badge tone="no">未运行</Badge>
        <Badge tone="warn">演练</Badge>
        <Badge>主 bot</Badge>
      </>,
    )
    expect(screen.getByText('未运行')).toHaveAttribute('data-tone', 'no')
    expect(screen.getByText('演练')).toHaveAttribute('data-tone', 'warn')
    expect(screen.getByText('主 bot')).toHaveAttribute('data-tone', 'neutral')
  })
})

// ---- 对比度回归（旧实现用 success/error/warning.main 当 11px 文字，
// 在 16% 同色淡底上只有 1.8-2.7:1）。这里按 WCAG 相对亮度实算，不只看 class。

type RGBA = [number, number, number, number]

function parseColor(value: string): RGBA {
  // theme 里的颜色是 hex（如 #232E3C），computed style 是 rgb()/rgba()。
  const hex = value.match(/^#([0-9a-f]{6})$/i)
  if (hex) {
    const n = Number.parseInt(hex[1], 16)
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1]
  }
  const match = value.match(/rgba?\(([^)]+)\)/)
  if (!match) throw new Error(`无法解析颜色：${value}`)
  const parts = match[1].split(',').map((part) => Number(part.trim()))
  return [parts[0], parts[1], parts[2], parts[3] ?? 1]
}

function relativeLuminance([r, g, b]: RGBA): number {
  const linear = [r, g, b].map((channel) => {
    const c = channel / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2]
}

/** 把半透明前景合成到不透明底色上（同色淡底是 rgba，需要先合成）。 */
function composite(fg: RGBA, bg: RGBA): RGBA {
  const alpha = fg[3]
  return [
    fg[0] * alpha + bg[0] * (1 - alpha),
    fg[1] * alpha + bg[1] * (1 - alpha),
    fg[2] * alpha + bg[2] * (1 - alpha),
    1,
  ]
}

function contrastRatio(fg: RGBA, bg: RGBA): number {
  const fgLum = relativeLuminance(fg)
  const bgLum = relativeLuminance(bg)
  const [hi, lo] = fgLum > bgLum ? [fgLum, bgLum] : [bgLum, fgLum]
  return (hi + 0.05) / (lo + 0.05)
}

const TONES = ['ok', 'no', 'warn'] as const

const LIGHT_TEXT: Record<(typeof TONES)[number], string> = {
  ok: 'rgb(6, 122, 62)',
  no: 'rgb(198, 40, 40)',
  warn: 'rgb(122, 91, 0)',
}

function renderTone(tone: BadgeTone, label: string, dark: boolean) {
  const theme = buildMiniTheme(dark ? { colorScheme: 'dark' } : {})
  render(
    <ThemeProvider theme={theme}>
      <Badge tone={tone}>{label}</Badge>
    </ThemeProvider>,
  )
  const el = screen.getByText(label)
  const style = getComputedStyle(el)
  // 淡底合成到卡片纸面（浅色下最亮、深色下比页面底更亮，取更不利的一侧）
  const paper = parseColor(theme.palette.background.paper)
  return {
    contrast: contrastRatio(parseColor(style.color), composite(parseColor(style.backgroundColor), paper)),
    color: style.color,
  }
}

describe('Badge 对比度（11px 小字 AA 4.5:1）', () => {
  it('浅色主题：固定深色文字，实测对比度 ≥4.5', () => {
    TONES.forEach((tone) => {
      const label = `light-${tone}`
      const { contrast, color } = renderTone(tone, label, false)
      expect(color).toBe(LIGHT_TEXT[tone])
      expect(contrast, `${tone} 对比度 ${contrast.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
    })
  })

  it('深色主题：固定亮色文字，实测对比度 ≥4.5', () => {
    TONES.forEach((tone) => {
      const label = `dark-${tone}`
      const { contrast } = renderTone(tone, label, true)
      expect(contrast, `${tone} 对比度 ${contrast.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
    })
  })
})
