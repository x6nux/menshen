// InvalidState 是链接无效 / 已失效 / 读取失败的统一占位。
import { Box, Typography } from '@mui/material'

export function InvalidState({ message }: { message: string }) {
  return (
    <Box sx={{ bgcolor: 'background.paper', borderRadius: 2, p: 2.5, textAlign: 'center' }}>
      <Typography sx={{ fontSize: 16, lineHeight: 1.7 }}>{message}</Typography>
    </Box>
  )
}

/** errorText 把 unknown 错误收敛成带文案的错误（保留服务端中文提示）。 */
export function errorText(err: unknown): string {
  if (err instanceof Error && err.message) return err.message
  return '请求失败，请刷新重试。'
}
