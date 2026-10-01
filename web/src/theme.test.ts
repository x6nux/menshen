import { describe, expect, it } from 'vitest'
import { buildMiniTheme, SUCCESS_COLOR } from './theme'

/** styleOverrides.root 在类型上是 CSSObject 或函数，测试里统一当对象读。 */
function rootOf(theme: ReturnType<typeof buildMiniTheme>, component: 'MuiButton' | 'MuiListItemButton' | 'MuiList') {
  return theme.components?.[component]?.styleOverrides?.root as unknown as Record<string, unknown>
}

describe('buildMiniTheme', () => {
  it('无 themeParams 时用浅色兜底', () => {
    const theme = buildMiniTheme()
    expect(theme.palette.mode).toBe('light')
    expect(theme.palette.background.default).toBe('#F5F6F7')
    expect(theme.palette.background.paper).toBe('#FFFFFF')
    expect(theme.palette.text.primary).toBe('#1A1A1A')
    expect(theme.palette.text.secondary).toBe('#8A8A8E')
    expect(theme.palette.primary.main).toBe('#1677FF')
    expect(theme.palette.primary.contrastText).toBe('#FFFFFF')
    expect(theme.palette.error.main).toBe('#FA5151')
    expect(theme.palette.success.main).toBe(SUCCESS_COLOR)
    expect(theme.shape.borderRadius).toBe(12)
  })

  it('themeParams 覆盖对应 palette 槽位，缺项仍走兜底', () => {
    const theme = buildMiniTheme({
      themeParams: {
        bg_color: '#101010',
        secondary_bg_color: '#202020',
        text_color: '#EEEEEE',
        hint_color: '#999999',
        button_color: '#ABCDEF',
        button_text_color: '#000000',
        destructive_text_color: '#FF0000',
      },
    })
    expect(theme.palette.background.default).toBe('#101010')
    expect(theme.palette.background.paper).toBe('#202020')
    expect(theme.palette.text.primary).toBe('#EEEEEE')
    expect(theme.palette.text.secondary).toBe('#999999')
    expect(theme.palette.primary.main).toBe('#ABCDEF')
    expect(theme.palette.primary.contrastText).toBe('#000000')
    expect(theme.palette.error.main).toBe('#FF0000')
    expect(theme.palette.success.main).toBe(SUCCESS_COLOR)
  })

  it('深色模式跟随 colorScheme', () => {
    const light = buildMiniTheme({ colorScheme: 'light' })
    const dark = buildMiniTheme({ colorScheme: 'dark' })
    expect(light.palette.mode).toBe('light')
    expect(dark.palette.mode).toBe('dark')
    // 深色缺省兜底不能还是浅色的白底
    expect(dark.palette.background.default).not.toBe(light.palette.background.default)
    expect(dark.palette.background.default).toBe('#17212B')
    // 即使深色，themeParams 仍优先
    const themed = buildMiniTheme({ colorScheme: 'dark', themeParams: { bg_color: '#000001' } })
    expect(themed.palette.background.default).toBe('#000001')
  })

  it('关键控件 override 存在', () => {
    const theme = buildMiniTheme()
    expect(rootOf(theme, 'MuiButton')).toMatchObject({
      textTransform: 'none',
      borderRadius: 10,
      minHeight: 44,
    })
    expect(rootOf(theme, 'MuiListItemButton')).toMatchObject({ minHeight: 52 })
    expect(rootOf(theme, 'MuiList')).toMatchObject({ paddingTop: 0, paddingBottom: 0 })
  })
})
