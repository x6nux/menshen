// 页面内复用的小展示件：状态徽标、指标格、信息行与无限列表页脚。
// 纯展示/加载编排，无请求逻辑（请求留给各页的 query hook）。
import { Box, Button, Typography } from '@mui/material'
import { useEffect, useRef } from 'react'
import type { ReactNode } from 'react'
import { errorStatus } from '../api/client'
import type { Bot, Chat } from '../api/types'
import { botStatus, chatStatus } from '../lib/status'
import { Badge, ErrorState } from '../ui'

/** BotStatusBadge 渲染 运行中/未运行/已停用 徽标。 */
export function BotStatusBadge({ bot }: { bot: Pick<Bot, 'enabled' | 'live'> }) {
  const status = botStatus(bot)
  return <Badge tone={status.tone}>{status.label}</Badge>
}

/** ChatStatusBadge 渲染 判定中/演练/停用 徽标。 */
export function ChatStatusBadge({ chat }: { chat: Pick<Chat, 'enabled' | 'dryrun'> }) {
  const status = chatStatus(chat)
  return <Badge tone={status.tone}>{status.label}</Badge>
}

/** Metric 是概览 2×2 指标格里的一格。 */
export function Metric({ label, value }: { label: ReactNode; value: ReactNode }) {
  return (
    <Box>
      <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.4 }}>
        {label}
      </Typography>
      <Typography sx={{ fontSize: 20, fontWeight: 600, lineHeight: 1.5 }}>{value}</Typography>
    </Box>
  )
}

/** InfoRow 是详情页信息卡里的一行：左标签（灰色 13px）+ 右内容（可换行）。 */
export function InfoRow({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 2, px: 2, py: 0.75 }}>
      <Typography
        component="span"
        sx={{ flex: '0 0 auto', width: 76, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}
      >
        {label}
      </Typography>
      <Box sx={{ flex: 1, minWidth: 0, fontSize: 14, lineHeight: 1.6, wordBreak: 'break-word' }}>
        {children}
      </Box>
    </Box>
  )
}

export interface InfiniteFooterProps {
  hasNextPage: boolean
  isFetchingNextPage: boolean
  /** 加载下一页失败（首屏失败由页面自己渲染 ErrorState）。 */
  isFetchNextPageError: boolean
  error: unknown
  onLoadMore: () => void
  onRetry: () => void
}

/**
 * InfiniteFooter 是无限列表的页脚：加载中… / 加载更多 / 没有更多了。
 * 有 IntersectionObserver 时滚动到底自动触发；没有（jsdom）时保留按钮兜底，
 * 测试与弱环境都能手动加载下一页。onLoadMore 用 ref 保证 observer 不频繁重建。
 */
export function InfiniteFooter({
  hasNextPage,
  isFetchingNextPage,
  isFetchNextPageError,
  error,
  onLoadMore,
  onRetry,
}: InfiniteFooterProps) {
  const loadRef = useRef(onLoadMore)
  useEffect(() => {
    loadRef.current = onLoadMore
  }, [onLoadMore])

  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !hasNextPage || isFetchingNextPage) return
    if (typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) loadRef.current()
      },
      { rootMargin: '160px' },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasNextPage, isFetchingNextPage])

  if (isFetchNextPageError) {
    return <ErrorState status={errorStatus(error)} onRetry={onRetry} />
  }

  return (
    <Box ref={sentinelRef} data-testid="list-footer" sx={{ py: 1.5, textAlign: 'center' }}>
      {isFetchingNextPage ? (
        <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>加载中…</Typography>
      ) : hasNextPage ? (
        <Button size="small" onClick={onLoadMore}>
          加载更多
        </Button>
      ) : (
        <Typography sx={{ fontSize: 13, color: 'text.disabled' }}>没有更多了</Typography>
      )}
    </Box>
  )
}
