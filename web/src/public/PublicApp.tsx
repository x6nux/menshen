// 公开网页外壳：按 _w 路径分发到申诉验证 / 原文查看 / 申诉详情。
//
// 主题跟随系统深浅色；不依赖 Telegram SDK，供浏览器直接访问。
import { Box, Container, CssBaseline, Typography } from '@mui/material'
import { ThemeProvider } from '@mui/material/styles'
import { useMemo } from 'react'
import { buildMiniTheme } from '../theme'
import { ErrorBoundary } from '../ui'
import { parseRoute } from './api'
import { InvalidState } from './InvalidState'
import { AppealDemoPage } from './pages/AppealDemoPage'
import { AppealVerifyPage } from './pages/AppealVerifyPage'
import { AppealViewPage } from './pages/AppealViewPage'
import { CaptchaDemoPage } from './pages/CaptchaDemoPage'
import { JoinVerifyPage } from './pages/JoinVerifyPage'
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

  // 公开页容器宽度按页面性质选：验证页是表单（窄）；查看页有表格（中）；
  // 申诉详情桌面端要两/三列（宽）。
  const containerWidth: 'sm' | 'lg' | 'xl' =
    route?.kind === 'apv' ? 'xl' : route?.kind === 'v' ? 'lg' : 'sm'

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <Container maxWidth={containerWidth} sx={{ py: 3 }}>
        <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1, mb: 2 }}>
          <Typography sx={{ fontSize: 22, fontWeight: 700 }}>门神</Typography>
          <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>
            反广告申诉与记录查看
          </Typography>
        </Box>
        {route === null ? (
          <InvalidState message="链接无效或已被替换。" />
        ) : route.kind === 'ap' ? (
          <ErrorBoundary>
            <AppealVerifyPage route={route} />
          </ErrorBoundary>
        ) : route.kind === 'jv' ? (
          <ErrorBoundary>
            <JoinVerifyPage route={route} />
          </ErrorBoundary>
        ) : route.kind === 'demo' ? (
          <ErrorBoundary>
            <CaptchaDemoPage route={route} />
          </ErrorBoundary>
        ) : route.kind === 'apdemo' ? (
          <ErrorBoundary>
            <AppealDemoPage route={route} />
          </ErrorBoundary>
        ) : route.kind === 'v' ? (
          <ErrorBoundary>
            <LogViewPage route={route} />
          </ErrorBoundary>
        ) : (
          <ErrorBoundary>
            <AppealViewPage route={route} />
          </ErrorBoundary>
        )}
      </Container>
    </ThemeProvider>
  )
}
