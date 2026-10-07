// ErrorBoundary 兜住渲染期异常：白屏是最坏的失败形态，至少给出原因与刷新入口。
//
// 只捕获渲染/生命周期异常（模块加载失败、事件回调里的异常不在范围内）。
import { Box, Button, Typography } from '@mui/material'
import { Component } from 'react'
import type { ErrorInfo, ReactNode } from 'react'

interface ErrorBoundaryProps {
  children: ReactNode
  /** title 是出错卡片的标题，默认为“页面出错了”。 */
  title?: string
}

interface ErrorBoundaryState {
  error: Error | null
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('页面渲染失败', error, info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <Box
          data-testid="error-boundary"
          sx={{ maxWidth: 520, mx: 'auto', mt: 8, p: 3, bgcolor: 'background.paper', borderRadius: 2 }}
        >
          <Typography sx={{ fontSize: 17, fontWeight: 700, mb: 1 }}>
            {this.props.title ?? '页面出错了'}
          </Typography>
          <Typography sx={{ fontSize: 13.5, color: 'text.secondary', wordBreak: 'break-word' }}>
            {this.state.error.message || String(this.state.error)}
          </Typography>
          <Button sx={{ mt: 2 }} variant="contained" onClick={() => window.location.reload()}>
            刷新重试
          </Button>
        </Box>
      )
    }
    return this.props.children
  }
}
