// 记录页（计划 4.4/4.5）：顶部 Segmented[判定记录|申诉]，申诉标签带未结角标。
// - 判定记录：服务端搜索（300ms 防抖 + 回车立即）、筛选 chips、无限滚动（每页 20）；
// - 申诉：未结（默认）/全部 chips、无限滚动；
// - 导航意图 'appeals'（概览待办进入）只消费一次，之后往返不再强制切段。
import { Box, Chip, Typography } from '@mui/material'
import { useEffect, useRef, useState } from 'react'
import { errorStatus } from '../api/client'
import { useInfiniteAppeals, useInfiniteLogs, useMiniState } from '../api/hooks'
import type { AppealRow, LogRow } from '../api/types'
import { actionLabel, fmtTS } from '../lib/format'
import { appealStatusInfo, appealSummary, verdictInfo } from '../lib/status'
import { useNav } from '../nav'
import {
  Badge,
  EmptyState,
  ErrorState,
  ListRow,
  SearchField,
  Segmented,
  Skeletons,
} from '../ui'
import { InfiniteFooter } from './shared'

type Section = 'logs' | 'appeals'

const LOG_FILTERS: { value: string; label: string }[] = [
  { value: 'deleted', label: '已删除' },
  { value: 'all', label: '全部' },
  { value: 'ad', label: '命中' },
  { value: 'clean', label: '正常' },
  { value: 'skipped', label: '跳过' },
]

const APPEAL_FILTERS: { value: string; label: string }[] = [
  { value: 'open', label: '未结' },
  { value: 'all', label: '全部' },
]

const MONO = 'ui-monospace, Menlo, monospace'
const SEARCH_DEBOUNCE_MS = 300

export function RecordsPage() {
  const nav = useNav()
  const { intent, clearIntent } = nav
  const state = useMiniState(true)

  const [section, setSection] = useState<Section>(() => (intent === 'appeals' ? 'appeals' : 'logs'))
  const [handledIntent, setHandledIntent] = useState<string | null>(null)
  const [logFilter, setLogFilter] = useState('deleted')
  const [appealFilter, setAppealFilter] = useState('open')
  const [q, setQ] = useState('')
  const [serverQ, setServerQ] = useState('')
  const debounceRef = useRef<number | null>(null)

  // 消费导航意图（React「渲染期调整状态」模式）：概览的「未结申诉」进入时默认申诉
  // 分段，随后清掉意图——往返不再恢复。意图的清除放在 effect 里（改的是 NavProvider
  // 的状态，渲染期改别的组件会触发 React 警告）。
  if (intent !== null && intent !== handledIntent) {
    setHandledIntent(intent)
    if (intent === 'appeals') setSection('appeals')
  }
  useEffect(() => {
    if (intent !== null) clearIntent()
  }, [intent, clearIntent])

  // 卸载时清掉未触发的防抖，避免对已卸载组件 setState。
  useEffect(
    () => () => {
      if (debounceRef.current !== null) window.clearTimeout(debounceRef.current)
    },
    [],
  )

  const logs = useInfiniteLogs({ filter: logFilter, q: serverQ, enabled: section === 'logs' })
  const appeals = useInfiniteAppeals({ filter: appealFilter, enabled: section === 'appeals' })

  const changeQ = (next: string) => {
    setQ(next)
    if (debounceRef.current !== null) window.clearTimeout(debounceRef.current)
    debounceRef.current = window.setTimeout(() => {
      debounceRef.current = null
      setServerQ(next)
    }, SEARCH_DEBOUNCE_MS)
  }

  const submitQ = () => {
    if (debounceRef.current !== null) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    setServerQ(q)
  }

  const openAppeals = state.data?.todo.open_appeals ?? 0
  const logRows = logs.data?.pages.flatMap((page) => page.logs) ?? []
  const appealRows = appeals.data?.pages.flatMap((page) => page.appeals) ?? []

  const sectionOptions = [
    { value: 'logs', label: '判定记录' },
    {
      value: 'appeals',
      label: (
        <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>
          申诉
          {openAppeals > 0 && (
            <Box
              component="span"
              data-testid="appeals-count"
              sx={{
                minWidth: 16,
                height: 16,
                px: '4px',
                borderRadius: '8px',
                bgcolor: 'error.main',
                color: 'error.contrastText',
                fontSize: 10,
                fontWeight: 600,
                lineHeight: '16px',
                textAlign: 'center',
              }}
            >
              {openAppeals}
            </Box>
          )}
        </Box>
      ),
    },
  ]

  return (
    <Box data-testid="records-page">
      <Box sx={{ mb: 1 }}>
        <Segmented
          value={section}
          onChange={(next) => setSection(next as Section)}
          options={sectionOptions}
          ariaLabel="记录分段"
        />
      </Box>

      {section === 'logs' ? (
        <>
          <SearchField
            value={q}
            onChange={changeQ}
            onSubmit={submitQ}
            placeholder="搜原文 / 理由 / uid / 群号"
            ariaLabel="搜索记录"
          />
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 1, mb: 0.5 }}>
            {LOG_FILTERS.map((item) => (
              <Chip
                key={item.value}
                size="small"
                label={item.label}
                color={logFilter === item.value ? 'primary' : 'default'}
                variant={logFilter === item.value ? 'filled' : 'outlined'}
                onClick={() => setLogFilter(item.value)}
              />
            ))}
          </Box>
          {logs.isError && logs.data === undefined ? (
            <ErrorState status={errorStatus(logs.error)} onRetry={() => void logs.refetch()} />
          ) : logs.isPending ? (
            <Skeletons rows={3} />
          ) : logRows.length === 0 ? (
            <EmptyState title="没有记录" description="换个筛选条件或搜索词试试。" />
          ) : (
            <>
              <Box sx={{ mt: 0.5 }}>
                {logRows.map((log) => (
                  <LogListRow
                    key={log.id}
                    log={log}
                    onOpen={() => nav.push({ k: 'log', id: log.id })}
                    onUser={() => nav.push({ k: 'user', id: log.user_id })}
                  />
                ))}
              </Box>
              <InfiniteFooter
                hasNextPage={logs.hasNextPage}
                isFetchingNextPage={logs.isFetchingNextPage}
                isFetchNextPageError={logs.isFetchNextPageError}
                error={logs.error}
                onLoadMore={() => void logs.fetchNextPage()}
                onRetry={() => void logs.fetchNextPage()}
              />
            </>
          )}
        </>
      ) : (
        <>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mb: 0.5 }}>
            {APPEAL_FILTERS.map((item) => (
              <Chip
                key={item.value}
                size="small"
                label={item.label}
                color={appealFilter === item.value ? 'primary' : 'default'}
                variant={appealFilter === item.value ? 'filled' : 'outlined'}
                onClick={() => setAppealFilter(item.value)}
              />
            ))}
          </Box>
          {appeals.isError && appeals.data === undefined ? (
            <ErrorState status={errorStatus(appeals.error)} onRetry={() => void appeals.refetch()} />
          ) : appeals.isPending ? (
            <Skeletons rows={3} />
          ) : appealRows.length === 0 ? (
            <EmptyState title="没有申诉" description="当前筛选下没有需要处理的申诉单。" />
          ) : (
            <>
              <Box sx={{ mt: 0.5 }}>
                {appealRows.map((appeal) => (
                  <AppealListRow
                    key={appeal.id}
                    appeal={appeal}
                    onOpen={() => nav.push({ k: 'appeal', id: appeal.id })}
                  />
                ))}
              </Box>
              <InfiniteFooter
                hasNextPage={appeals.hasNextPage}
                isFetchingNextPage={appeals.isFetchingNextPage}
                isFetchNextPageError={appeals.isFetchNextPageError}
                error={appeals.error}
                onLoadMore={() => void appeals.fetchNextPage()}
                onRetry={() => void appeals.fetchNextPage()}
              />
            </>
          )}
        </>
      )}
    </Box>
  )
}

/** LogListRow：`#id · 时间` + 判定/处置徽标 + uid 链接 + 群号/置信度/开销 + 原文 50 字。 */
function LogListRow({
  log,
  onOpen,
  onUser,
}: {
  log: LogRow
  onOpen: () => void
  onUser: () => void
}) {
  const verdict = verdictInfo(log.verdict)
  return (
    <ListRow
      primary={
        <Box component="span" sx={{ display: 'flex', alignItems: 'center', gap: 0.5, flexWrap: 'wrap' }}>
          <Box component="span" sx={{ fontFamily: MONO, fontSize: 14 }}>
            #{log.id} · {fmtTS(log.created_at)}
          </Box>
          <Badge tone={verdict.tone}>{verdict.label}</Badge>
          <Badge>{actionLabel(log.action)}</Badge>
        </Box>
      }
      secondary={
        <Box component="span" sx={{ display: 'block' }}>
          <Box
            component="span"
            role="link"
            tabIndex={0}
            data-testid={`log-user-${log.id}`}
            onClick={(event) => {
              event.stopPropagation()
              onUser()
            }}
            sx={{ color: 'primary.main' }}
          >
            uid {log.user_id}（资料）
          </Box>
          {` · 群 ${log.chat_id} · ${Math.round(log.confidence * 100)}% · ${log.cost_text}`}
          {log.text !== '' && (
            <Box component="span" sx={{ display: 'block' }}>
              {log.text.slice(0, 50)}
            </Box>
          )}
        </Box>
      }
      chevron
      onClick={onOpen}
    />
  )
}

/** AppealListRow：`#id · uid` + 状态徽标 + AI 结论摘要。 */
function AppealListRow({ appeal, onOpen }: { appeal: AppealRow; onOpen: () => void }) {
  const status = appealStatusInfo(appeal.status)
  return (
    <ListRow
      primary={
        <Box component="span" sx={{ display: 'flex', alignItems: 'center', gap: 0.5, flexWrap: 'wrap' }}>
          <Box component="span" sx={{ fontFamily: MONO, fontSize: 14 }}>
            #{appeal.id} · uid {appeal.user_id}
          </Box>
          <Badge tone={status.tone}>{status.label}</Badge>
        </Box>
      }
      secondary={
        <Typography component="span" sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.4 }}>
          {appealSummary(appeal.ai_result, appeal.ai_reason)}
        </Typography>
      }
      chevron
      onClick={onOpen}
    />
  )
}
