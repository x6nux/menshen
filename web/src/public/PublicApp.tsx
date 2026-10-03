// 公开网页外壳：按 _w 路径分发到申诉验证 / 原文查看 / 申诉详情。
//
// 主题跟系统深浅色；不依赖任何 Telegram SDK——这一层就是给浏览器直开的。
import { Box, Container, CssBaseline, Typography } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import { useMemo } from 'react'
import { buildMiniTheme } from '../theme'
import { parseRoute } from './api'
import { InvalidState } from './InvalidState'
import { AppealVerifyPage } from './pages/AppealVerifyPage'
import { AppealViewPage } from './pages/AppealViewPage'
import { LogViewPage } from './pages/LogViewPage'

export function PublicApp() {
  const route = useMemo(() => parseRoute(location.pathname), [])
  const theme = useMemo(
    () =>
      buildMiniTheme({
        colorScheme:
          window.matchMedia?.('(prefers-color-scheme: dark)')?.matches === true
            ? 'dark'
            : 'light',
      }),
    [],
  )

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <Container maxWidth="sm" sx={{ py: 3 }}>
        <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
          <Typography sx={{ fontSize: 22, fontWeight: 700 }}>门神</Typography>
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
            反广告申诉与记录查看
          </Typography>
        </Box>
        {route === null ? (
          <InvalidState message="链接无效或已被替换。" />
        ) : route.kind === 'ap' ? (
          <AppealVerifyPage route={route} />
        ) : route.kind === 'v' ? (
          <LogViewPage route={route} />
        ) : (
          <AppealViewPage route={route} />
        )}
      </Container>
    </ThemeProvider>
  )
}
