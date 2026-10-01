import { CssBaseline, Typography } from '@mui/material'

// Task 0 只证明 MUI 构建链可用；T1 会在这里接入主题、导航与页面骨架。
export default function App() {
  return (
    <>
      <CssBaseline />
      <Typography variant="h5" component="h1" sx={{ p: 2 }}>
        门神配置
      </Typography>
    </>
  )
}
