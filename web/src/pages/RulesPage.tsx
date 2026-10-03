// AI 必封规则（仅主管理员；服务端仍是唯一裁决，次管进页面只看到 403）：
// - Agent 卡：启动/停止 AI 从历史封禁里发现候选规则；运行中每 2 秒轮询步骤，
//   运行结束自动刷新规则列表（Agent 可能刚写入了新候选）；
// - 列表：名称 + 分类/正则副行、启用/候选与强制徽标、最近测试 TP/FP 摘要；
//   右上「＋ 手动新增」抽屉（保存由服务端强制跑全库测试）；
// - 详情：全库误封测试（FP/已撤销样本红色高亮，TP 样本可折叠）、启用/强制
//   开关（乐观更新；强制受服务端防误封门约束：未测试或 fp/undone>0 时禁用）、
//   删除确认（写明规则名与后果）。
import {
  Box,
  Button,
  Chip,
  TextField,
  Typography,
} from '@mui/material'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { errorStatus } from '../api/client'
import { miniQueryKeys, useMiniState, useRuleAgent, useRules } from '../api/hooks'
import { useOptimisticRulesMutation, useRulesMutation } from '../api/mutations'
import type {
  Rule,
  RuleAgent,
  RuleAgentStartResp,
  RuleKindStat,
  RuleSample,
  RuleSaveResp,
  RuleTest,
} from '../api/types'
import { actionLabel, displayTz, fmtTS, verdictLabel } from '../lib/format'
import { useNav } from '../nav'
import {
  Badge,
  EmptyState,
  ErrorState,
  FormDrawer,
  ListRow,
  SectionCard,
  Skeletons,
  SwitchRow,
  useConfirm,
  useToast,
} from '../ui'
import { InfoRow } from './shared'

const MONO = 'ui-monospace, Menlo, monospace'

/** 规则列表的筛选与排序。 */
type RuleFilter = 'all' | 'enabled' | 'candidate' | 'enforce'
type RuleSort = 'default' | 'coverage' | 'tp' | 'hits' | 'recent'

const RULE_FILTERS: { key: RuleFilter; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'enabled', label: '启用' },
  { key: 'candidate', label: '候选' },
  { key: 'enforce', label: '强制' },
]

const RULE_SORTS: { key: RuleSort; label: string }[] = [
  { key: 'default', label: '默认' },
  { key: 'coverage', label: '覆盖率' },
  { key: 'tp', label: '命中广告' },
  { key: 'hits', label: '规则命中' },
  { key: 'recent', label: '最近创建' },
]

/** ruleCoverageValue 覆盖率排序值；未测试或分母为 0 记 -1 排最后。 */
function ruleCoverageValue(rule: Rule): number {
  if (rule.last_ads_total <= 0) return -1
  return rule.last_tp / rule.last_ads_total
}

/** pct 覆盖率显示：一位小数；分母缺失或为 0 时 null（调用方决定「—」或省略）。 */
function pct(matched: number, total: number): string | null {
  if (!Number.isFinite(total) || total <= 0) return null
  return `${((matched / total) * 100).toFixed(1)}%`
}

/** KindCoverageList 是「按类型覆盖」小节：实时测试与落库数据共用。 */
function KindCoverageList({ kinds }: { kinds: RuleKindStat[] }) {
  if (!kinds || kinds.length === 0) return null
  return (
    <Box sx={{ mt: 1.5 }}>
      <Typography sx={{ fontSize: 13, fontWeight: 600, color: 'text.secondary' }}>
        按类型覆盖
      </Typography>
      {kinds.map((k) => (
        <Box
          key={k.kind || '（空）'}
          sx={{ display: 'flex', gap: 1, mt: 0.5, fontSize: 13, alignItems: 'baseline' }}
        >
          <Box component="span" sx={{ flex: 1, fontFamily: MONO, wordBreak: 'break-all' }}>
            {k.kind || '（空）'}
          </Box>
          <Box component="span" sx={{ color: 'text.secondary', whiteSpace: 'nowrap' }}>
            {k.matched}/{k.total}
          </Box>
          <Box
            component="span"
            sx={{
              minWidth: 48,
              textAlign: 'right',
              whiteSpace: 'nowrap',
              color: k.matched === 0 ? 'text.disabled' : 'text.primary',
            }}
          >
            {pct(k.matched, k.total) ?? '—'}
          </Box>
        </Box>
      ))}
    </Box>
  )
}

/** agentStatus 把 Agent 运行态归成状态徽标（空闲/运行中/已完成/失败）。 */
function agentStatus(agent: RuleAgent | undefined): {
  label: string
  tone: 'ok' | 'no' | 'warn' | 'neutral'
} {
  if (agent?.running) return { label: '运行中', tone: 'warn' }
  if (agent?.error) return { label: '失败', tone: 'no' }
  if (agent && (agent.result !== '' || agent.finished_at > 0)) {
    return { label: '已完成', tone: 'ok' }
  }
  return { label: '空闲', tone: 'neutral' }
}

/** sourceLabel 规则来源文案；未知来源原样显示。 */
function sourceLabel(source: string): string {
  if (source === 'ai') return 'AI 发现'
  return source || '未知'
}

/** sourceText 详情页需要的「来源 + 创建时间」。 */
function sourceText(rule: Rule, tz?: string): string {
  return `来源 ${sourceLabel(rule.source)} · 创建于 ${fmtTS(rule.created_at, tz)}`
}

/** enforceHint 强制开关的灰字说明：可开启时说效果，被防误封门拦住时说原因。 */
function enforceHint(rule: Rule): string {
  if (!rule.enabled) return '先启用规则，再开启强制'
  if (rule.last_tested_at === 0) return '还没跑过全库测试：先测试确认没有误封，再开启强制'
  if (rule.last_fp > 0 || rule.last_undone > 0) {
    return `全库测试有疑似误封（FP ${rule.last_fp} · 已撤销 ${rule.last_undone}）：先修净误封，再开启强制`
  }
  return '开启后命中即最高档处置（零 AI 成本）；不开则命中作为强证据送 AI 复核'
}

/** RuleBadges 列表/详情共用的启用状态与强制徽标。 */
function RuleBadges({ rule }: { rule: Rule }) {
  return (
    <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}>
      <Badge tone={rule.enabled ? 'ok' : 'neutral'}>{rule.enabled ? '启用' : '候选'}</Badge>
      {rule.enforce && <Badge tone="warn">强制</Badge>}
    </Box>
  )
}

/** RuleSecondary 列表副行：分类灰字 + 等宽正则（单行截断）。 */
function RuleSecondary({ rule }: { rule: Rule }) {
  return (
    <Box component="span" sx={{ display: 'block' }}>
      {rule.category !== '' && (
        <Box component="span" sx={{ mr: 0.75 }}>
          {rule.category}
        </Box>
      )}
      <Box
        component="span"
        sx={{
          display: 'inline-block',
          maxWidth: '100%',
          verticalAlign: 'bottom',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          fontFamily: MONO,
          fontSize: 12,
        }}
      >
        {rule.pattern}
      </Box>
    </Box>
  )
}

/** TpFpValue 列表右侧的最近测试摘要：从未测试时明确写「未测试」。 */
function TpFpValue({ rule }: { rule: Rule }) {
  if (rule.last_tested_at === 0) {
    return (
      <Box component="span" sx={{ fontSize: 12 }}>
        未测试
      </Box>
    )
  }
  const cov = pct(rule.last_tp, rule.last_ads_total)
  return (
    <Box
      component="span"
      data-testid={`rule-tpfp-${rule.id}`}
      sx={{ fontSize: 12, whiteSpace: 'nowrap' }}
    >
      TP {rule.last_tp} ·{' '}
      <Box component="span" sx={{ color: rule.last_fp > 0 ? 'error.main' : 'inherit' }}>
        FP {rule.last_fp}
      </Box>
      {cov !== null && <> · 覆盖 {cov}</>}
    </Box>
  )
}

/** AgentCard 是规则发现 Agent 的状态卡：只显示运行状态、启停与最终结果。 */
function AgentCard() {
  const queryClient = useQueryClient()
  const toast = useToast()
  const agentQuery = useRuleAgent(true)
  const startMut = useRulesMutation<RuleAgentStartResp>()
  const stopMut = useRulesMutation<{ ok: boolean; stopped: boolean }>()
  const wasRunning = useRef(false)

  const agent = agentQuery.data?.agent
  const running = agent?.running ?? false
  const stepsCount = agent?.steps_count ?? 0
  const targetLogId = agent?.target_log_id ?? 0
  const status = agentQuery.isError
    ? { label: '失败', tone: 'no' as const }
    : agentStatus(agent)
  const ended =
    agent !== undefined &&
    (agent.error !== '' || agent.result !== '' || agent.created_rule_id > 0)

  // 运行 → 结束的跳变：Agent 可能刚写入了新规则，刷新规则列表。
  useEffect(() => {
    if (wasRunning.current && !running) {
      void queryClient.invalidateQueries({ queryKey: miniQueryKeys.rules })
    }
    wasRunning.current = running
  }, [running, queryClient])

  function start() {
    startMut.mutate(
      { action: 'agent_start' },
      {
        // 竞态防护：挂载期的 agent_status 可能还在途，等它回来时若直接
        // setQueryData 会被旧响应覆盖（running 变回 false、轮询停掉、状态
        // 不再刷新）。先取消在途请求，再写启动态，最后失效重取确认真值。
        onSuccess: async (resp) => {
          if (resp.agent) {
            await queryClient.cancelQueries({ queryKey: miniQueryKeys.rulesAgent })
            queryClient.setQueryData(miniQueryKeys.rulesAgent, { agent: resp.agent })
            await queryClient.invalidateQueries({ queryKey: miniQueryKeys.rulesAgent })
          }
          toast('已开始规则发现，完成后会给出新规则')
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  function stop() {
    stopMut.mutate(
      { action: 'agent_stop' },
      {
        onSuccess: (resp) =>
          toast(resp.stopped ? '已请求停止，当前步骤结束后收尾' : '当前没有在运行的发现任务'),
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <SectionCard title="AI 规则发现">
      <Box sx={{ px: 2, py: 1.5 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <Badge tone={status.tone}>{status.label}</Badge>
          {agentQuery.isError ? (
            <>
              <Typography
                sx={{ flex: 1, minWidth: 0, fontSize: 13, color: 'error.main', lineHeight: 1.5 }}
              >
                状态读取失败
              </Typography>
              <Button size="small" onClick={() => void agentQuery.refetch()}>
                重试
              </Button>
            </>
          ) : (
            <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.5 }}>
              {running
                ? targetLogId > 0
                  ? `正在针对记录 #${targetLogId} 总结必封规则…已执行 ${stepsCount} 步`
                  : `正在从历史封禁里总结必封规则…已执行 ${stepsCount} 步`
                : '让 AI 扫描历史封禁生成规则；无误封的会自动启用（仅作为判定证据）。'}
            </Typography>
          )}
        </Box>
        <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
          <Button
            variant="contained"
            size="small"
            loading={startMut.isPending}
            disabled={running || startMut.isPending || stopMut.isPending}
            onClick={start}
          >
            开始发现
          </Button>
          <Button
            variant="outlined"
            size="small"
            color="error"
            loading={stopMut.isPending}
            disabled={!running || stopMut.isPending || startMut.isPending}
            onClick={stop}
          >
            停止
          </Button>
        </Box>
      </Box>

      {!running && ended && agent !== undefined && (
        <Box sx={{ px: 2, py: 1.5, borderTop: '1px solid', borderColor: 'divider' }}>
          {agent.error !== '' ? (
            <Typography sx={{ fontSize: 13, color: 'error.main', lineHeight: 1.6 }}>
              失败：{agent.error}
            </Typography>
          ) : (
            agent.result !== '' && (
              <Typography sx={{ fontSize: 13, lineHeight: 1.6 }}>{agent.result}</Typography>
            )
          )}
          {(agent.created_rule_ids ?? []).length > 0 && (
            <Typography sx={{ mt: 0.5, fontSize: 13, color: 'text.secondary' }}>
              本轮创建规则：{(agent.created_rule_ids ?? []).map((id) => `#${id}`).join('、')}
              ，可在下方列表查看与测试
            </Typography>
          )}
        </Box>
      )}
    </SectionCard>
  )
}

export function RulesPage() {
  const nav = useNav()
  const toast = useToast()
  const state = useMiniState(true)
  const main = state.data?.me.main === true
  // rules op 只对主管理员开放：次管不发请求，直接落到 403。
  const rulesQuery = useRules(main)
  const addMut = useRulesMutation<RuleSaveResp>()
  const testMut = useRulesMutation<{ test: RuleTest }>()

  const [addOpen, setAddOpen] = useState(false)
  const [name, setName] = useState('')
  const [pattern, setPattern] = useState('')
  const [category, setCategory] = useState('')
  const [note, setNote] = useState('')
  const [testOpen, setTestOpen] = useState(false)
  const [testPattern, setTestPattern] = useState('')
  // testResult 绑定提交时的 pattern：请求在途时改了输入，迟到的响应不能当
  // 当前输入的结果展示（否则会用旧统计误导「用此正则新建规则」）。
  const [testResult, setTestResult] = useState<{ test: RuleTest; pattern: string } | null>(null)
  const [filter, setFilter] = useState<RuleFilter>('all')
  const [sort, setSort] = useState<RuleSort>('default')
  const [search, setSearch] = useState('')

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!main) return <ErrorState status={403} />

  const tz = displayTz(state.data)
  const rules = rulesQuery.data?.rules ?? []
  const query = search.trim().toLowerCase()
  const filteredRules = rules
    .filter((r) => {
      if (filter === 'enabled' && !r.enabled) return false
      if (filter === 'candidate' && r.enabled) return false
      if (filter === 'enforce' && !r.enforce) return false
      return (
        query === '' ||
        r.name.toLowerCase().includes(query) ||
        r.pattern.toLowerCase().includes(query)
      )
    })
    .sort((a, b) => {
      switch (sort) {
        case 'coverage':
          return ruleCoverageValue(b) - ruleCoverageValue(a) || a.id - b.id
        case 'tp':
          return b.last_tp - a.last_tp || a.id - b.id
        case 'hits':
          return b.hits - a.hits || a.id - b.id
        case 'recent':
          return b.created_at - a.created_at || a.id - b.id
        default:
          return a.id - b.id
      }
    })

  function openAdd() {
    setName('')
    setPattern('')
    setCategory('')
    setNote('')
    setAddOpen(true)
  }

  function submitAdd() {
    addMut.mutate(
      {
        action: 'save',
        id: 0,
        name: name.trim(),
        pattern: pattern.trim(),
        category: category.trim(),
        note: note.trim(),
      },
      {
        onSuccess: (resp) => {
          toast(`已保存；本次测试命中 ${resp.test?.matched ?? 0} 条`)
          setAddOpen(false)
        },
        // 400（正则非法/匹配空文本等）保持抽屉与输入，只 toast 服务端文案。
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="rules-page">
      <AgentCard />

      {rulesQuery.isError ? (
        <ErrorState
          status={errorStatus(rulesQuery.error)}
          onRetry={() => void rulesQuery.refetch()}
        />
      ) : rulesQuery.isPending ? (
        <Skeletons rows={3} />
      ) : (
        <>
          <Box sx={{ display: 'flex', alignItems: 'center', mb: 0.75, pl: 1 }}>
            <Typography
              component="h2"
              sx={{ flex: 1, fontSize: 13, fontWeight: 500, color: 'text.secondary' }}
            >
              规则（{filteredRules.length}
              {filteredRules.length !== rules.length ? `/${rules.length}` : ''}）
            </Typography>
            <Button
              size="small"
              onClick={() => {
                setTestPattern('')
                setTestResult(null)
                setTestOpen(true)
              }}
              sx={{ minHeight: 30, px: 1, fontSize: 13 }}
            >
              测试正则
            </Button>
            <Button size="small" onClick={openAdd} sx={{ minHeight: 30, px: 1, fontSize: 13 }}>
              ＋ 手动新增
            </Button>
          </Box>
          <Box sx={{ px: 1, mb: 0.75, display: 'flex', flexDirection: 'column', gap: 0.75 }}>
            <TextField
              fullWidth
              size="small"
              placeholder="搜索名称或正则"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, alignItems: 'center' }}>
              {RULE_FILTERS.map((f) => (
                <Chip
                  key={f.key}
                  size="small"
                  label={f.label}
                  color={filter === f.key ? 'primary' : 'default'}
                  variant={filter === f.key ? 'filled' : 'outlined'}
                  onClick={() => setFilter(f.key)}
                />
              ))}
            </Box>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, alignItems: 'center' }}>
              <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>排序</Typography>
              {RULE_SORTS.map((s) => (
                <Chip
                  key={s.key}
                  size="small"
                  label={s.label}
                  color={sort === s.key ? 'primary' : 'default'}
                  variant={sort === s.key ? 'filled' : 'outlined'}
                  onClick={() => setSort(s.key)}
                />
              ))}
            </Box>
          </Box>
          <SectionCard>
            {rules.length === 0 ? (
              <EmptyState
                title="还没有规则"
                description="点「开始发现」让 AI 从历史封禁里生成规则，也可以手动新增后自己测试。"
              />
            ) : filteredRules.length === 0 ? (
              <EmptyState title="没有符合条件的规则" description="换个筛选条件或清空搜索再试。" />
            ) : (
              filteredRules.map((rule) => (
                <ListRow
                  key={rule.id}
                  primary={rule.name}
                  secondary={<RuleSecondary rule={rule} />}
                  badge={<RuleBadges rule={rule} />}
                  value={<TpFpValue rule={rule} />}
                  chevron
                  onClick={() => nav.push({ k: 'rule', id: rule.id })}
                />
              ))
            )}
          </SectionCard>
        </>
      )}

      <FormDrawer
        open={testOpen}
        onClose={() => setTestOpen(false)}
        title="测试正则"
        pending={testMut.isPending}
        submitText="开始测试"
        submitDisabled={testPattern.trim() === ''}
        onSubmit={() => {
          const submitted = testPattern.trim()
          testMut.mutate(
            { action: 'test', pattern: submitted },
            {
              onSuccess: (resp) => setTestResult({ test: resp.test, pattern: submitted }),
              onError: (err) => toast(err.message),
            },
          )
        }}
      >
        <TextField
          fullWidth
          multiline
          minRows={2}
          size="small"
          label="正则（RE2）"
          placeholder="如 兼职.{0,6}(日结|垫付|押金)"
          value={testPattern}
          onChange={(event) => {
            setTestPattern(event.target.value)
            // 改了正则，旧结果不再对应当前输入，立即作废。
            setTestResult(null)
          }}
          sx={{ '& textarea': { fontFamily: MONO, fontSize: 13 } }}
        />
        <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.6 }}>
          只试跑不落库；保存时服务端仍会跑一次同样的全库测试。
        </Typography>
        {testResult !== null && testResult.pattern === testPattern.trim() && (
          <>
            <RuleTestPanel test={testResult.test} tz={tz} />
            <Button
              fullWidth
              variant="outlined"
              sx={{ mt: 1.5 }}
              onClick={() => {
                setTestOpen(false)
                setName('')
                setPattern(testPattern.trim())
                setCategory('')
                setNote('')
                setAddOpen(true)
              }}
            >
              用此正则新建规则
            </Button>
          </>
        )}
      </FormDrawer>

      <FormDrawer
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="手动新增规则"
        pending={addMut.isPending}
        submitText="保存"
        submitDisabled={name.trim() === '' || pattern.trim() === ''}
        onSubmit={submitAdd}
      >
        <TextField
          fullWidth
          size="small"
          label="名称"
          placeholder="如 兼职押金话术"
          value={name}
          onChange={(event) => setName(event.target.value)}
          slotProps={{ htmlInput: { maxLength: 60 } }}
        />
        <TextField
          fullWidth
          multiline
          minRows={2}
          size="small"
          label="正则（RE2）"
          placeholder="如 兼职.{0,6}(日结|垫付|押金)"
          value={pattern}
          onChange={(event) => setPattern(event.target.value)}
          sx={{ mt: 1.5, '& textarea': { fontFamily: MONO, fontSize: 13 } }}
        />
        <TextField
          fullWidth
          size="small"
          label="分类（可选）"
          placeholder="如 scam"
          value={category}
          onChange={(event) => setCategory(event.target.value)}
          slotProps={{ htmlInput: { maxLength: 20 } }}
          sx={{ mt: 1.5 }}
        />
        <TextField
          fullWidth
          multiline
          minRows={2}
          size="small"
          label="备注（可选）"
          value={note}
          onChange={(event) => setNote(event.target.value)}
          sx={{ mt: 1.5 }}
        />
        <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.6 }}>
          保存时服务端会自动跑一次全库测试；不能编译或会匹配空文本的正则会被拒绝。
        </Typography>
      </FormDrawer>
    </Box>
  )
}

/** TestStat 是全库测试结果里的一个数字格。 */
function TestStat({
  label,
  value,
  tone = 'plain',
  testId,
}: {
  label: string
  value: number | string
  tone?: 'plain' | 'ok' | 'no'
  testId: string
}) {
  const color = tone === 'ok' ? 'success.main' : tone === 'no' ? 'error.main' : 'text.primary'
  return (
    <Box sx={{ bgcolor: 'action.hover', borderRadius: '8px', px: 1, py: 0.75 }}>
      <Typography sx={{ fontSize: 11, color: 'text.secondary', lineHeight: 1.5 }}>
        {label}
      </Typography>
      <Typography
        data-testid={testId}
        sx={{ fontSize: 18, fontWeight: 600, color, lineHeight: 1.4 }}
      >
        {value}
      </Typography>
    </Box>
  )
}

/** SampleRow 是一条命中样本：verdict/action 徽标 + 时间 + chat/user + 正文摘要。 */
function SampleRow({
  sample,
  flag,
  testId,
  tz,
}: {
  sample: RuleSample
  /** 非空时是误封类样本（红框 + 前置标记徽标）。 */
  flag: string
  testId: string
  tz?: string
}) {
  const flagged = flag !== ''
  return (
    <Box
      data-testid={testId}
      sx={{
        mt: 0.75,
        p: 1,
        borderRadius: '8px',
        bgcolor: flagged ? 'rgba(198, 40, 40, 0.07)' : 'action.hover',
        borderLeft: '3px solid',
        borderColor: flagged ? 'error.main' : 'transparent',
      }}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, flexWrap: 'wrap' }}>
        {flagged && <Badge tone="no">{flag}</Badge>}
        <Badge tone={sample.verdict === 'ad' ? 'ok' : 'neutral'}>
          {verdictLabel(sample.verdict)}
        </Badge>
        <Badge tone="neutral">{actionLabel(sample.action)}</Badge>
        <Typography sx={{ ml: 'auto', fontSize: 12, color: 'text.secondary' }}>
          {fmtTS(sample.created_at, tz)}
        </Typography>
      </Box>
      <Typography sx={{ mt: 0.5, fontSize: 12, color: 'text.secondary' }}>
        chat {sample.chat_id} · uid {sample.user_id}
      </Typography>
      <Typography sx={{ mt: 0.25, fontSize: 13, lineHeight: 1.6, wordBreak: 'break-word' }}>
        {sample.text || '（空文本）'}
      </Typography>
    </Box>
  )
}

/** SampleList 是一组样本：误封类红标题，正文逐条渲染。 */
function SampleList({
  title,
  samples,
  flag,
  testId,
  tz,
}: {
  title: string
  samples: RuleSample[]
  flag: string
  testId: string
  tz?: string
}) {
  return (
    <Box sx={{ mt: 1.5 }}>
      <Typography
        sx={{ fontSize: 13, fontWeight: 600, color: flag === '' ? 'text.secondary' : 'error.main' }}
      >
        {title}
      </Typography>
      {samples.map((sample) => (
        <SampleRow key={sample.id} sample={sample} flag={flag} testId={testId} tz={tz} />
      ))}
    </Box>
  )
}

/** RuleTestPanel 渲染一轮全库测试：七个数字 + FP/已撤销红色样本 + 可折叠 TP 样本。 */
function RuleTestPanel({ test, tz }: { test: RuleTest; tz?: string }) {
  const [tpOpen, setTpOpen] = useState(false)
  return (
    <Box
      data-testid="rule-test-result"
      sx={{ px: 2, py: 1.5, borderTop: '1px solid', borderColor: 'divider' }}
    >
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 1 }}>
        <TestStat testId="rule-test-scanned" label="扫描" value={test.scanned} />
        <TestStat testId="rule-test-matched" label="命中" value={test.matched} />
        <TestStat
          testId="rule-test-coverage"
          label="覆盖率"
          value={pct(test.tp, test.ads_total) ?? '—'}
        />
        <TestStat
          testId="rule-test-tp"
          label="TP 确认广告"
          value={test.tp}
          tone={test.tp > 0 ? 'ok' : 'plain'}
        />
        <TestStat
          testId="rule-test-fp"
          label="FP 疑似误封"
          value={test.fp}
          tone={test.fp > 0 ? 'no' : 'plain'}
        />
        <TestStat
          testId="rule-test-undone"
          label="已撤销"
          value={test.undone}
          tone={test.undone > 0 ? 'no' : 'plain'}
        />
        <TestStat testId="rule-test-neutral" label="中性" value={test.neutral} />
      </Box>

      <KindCoverageList kinds={test.kinds} />

      {test.fp_samples.length > 0 && (
        <SampleList
          title={`误封样本（${test.fp_samples.length}）`}
          samples={test.fp_samples}
          flag="误封"
          testId="rule-sample-fp"
          tz={tz}
        />
      )}
      {test.undone_samples.length > 0 && (
        <SampleList
          title={`已撤销样本（${test.undone_samples.length}）`}
          samples={test.undone_samples}
          flag="已撤销"
          testId="rule-sample-undone"
          tz={tz}
        />
      )}
      {test.tp_samples.length > 0 && (
        <>
          <Button size="small" onClick={() => setTpOpen((open) => !open)} sx={{ mt: 1.5 }}>
            {tpOpen ? '收起 TP 样本' : `展开 TP 样本（${test.tp_samples.length}）`}
          </Button>
          {tpOpen && (
            <SampleList
              title="TP 样本"
              samples={test.tp_samples}
              flag=""
              testId="rule-sample-tp"
              tz={tz}
            />
          )}
        </>
      )}
      {test.matched === 0 && (
        <Typography sx={{ mt: 1, fontSize: 13, color: 'text.secondary' }}>
          这次测试没有命中任何历史记录。
        </Typography>
      )}
    </Box>
  )
}

export function RuleDetailPage({ id }: { id: number }) {
  const nav = useNav()
  const toast = useToast()
  const confirm = useConfirm()
  const state = useMiniState(true)
  const main = state.data?.me.main === true
  const rulesQuery = useRules(main)

  const toggleMut = useOptimisticRulesMutation()
  const enforceMut = useOptimisticRulesMutation()
  const testMut = useRulesMutation<{ test: RuleTest }>()
  const removeMut = useRulesMutation()

  const [test, setTest] = useState<RuleTest | null>(null)

  if (state.isPending) return <Skeletons rows={4} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }
  if (!main) return <ErrorState status={403} />
  if (rulesQuery.isPending) return <Skeletons rows={4} />
  if (rulesQuery.isError) {
    return (
      <ErrorState status={errorStatus(rulesQuery.error)} onRetry={() => void rulesQuery.refetch()} />
    )
  }

  const rule = (rulesQuery.data.rules ?? []).find((r) => r.id === id)
  if (!rule) {
    return <EmptyState title="规则不存在" description="它可能已被删除。" />
  }

  const tz = displayTz(state.data)
  // 防误封门与服务端一致：未启用 / 从未测试 / fp 或 undone>0 时禁止开启强制；
  // 已开启的强制始终允许关闭（关闭不受门限制）。
  const enforceBlocked = !rule.enabled || rule.last_tested_at === 0 || rule.last_fp > 0 || rule.last_undone > 0
  const enforceDisabled = enforceMut.isPending || (!rule.enforce && enforceBlocked)

  function toggleEnabled(next: boolean) {
    toggleMut.mutate(
      {
        body: { action: 'toggle', id, enabled: next },
        // 停用时服务端会一并清掉 enforce，乐观结果保持同一不变式。
        apply: (data) => ({
          ...data,
          rules: data.rules.map((r) =>
            r.id === id ? { ...r, enabled: next, enforce: next ? r.enforce : false } : r,
          ),
        }),
      },
      {
        onSuccess: () =>
          toast(next ? '已启用：命中作为强证据送 AI 复核（开启强制才直接处置）' : '已停用（强制一并关闭）'),
        onError: (err) => toast(err.message),
      },
    )
  }

  function toggleEnforce(next: boolean) {
    enforceMut.mutate(
      {
        body: { action: 'enforce', id, enforce: next },
        apply: (data) => ({
          ...data,
          rules: data.rules.map((r) => (r.id === id ? { ...r, enforce: next } : r)),
        }),
      },
      {
        onSuccess: () => toast(next ? '已开启强制：命中即最高档处置' : '已关闭强制'),
        onError: (err) => toast(err.message),
      },
    )
  }

  function runTest() {
    testMut.mutate(
      { action: 'test', id },
      {
        onSuccess: (resp) => {
          setTest(resp.test)
          toast(
            resp.test.fp > 0 || resp.test.undone > 0
              ? `测试完成：发现 ${resp.test.fp} 条疑似误封，先别开强制`
              : `测试完成：命中 ${resp.test.matched} 条，无误封`,
          )
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  async function removeRule() {
    const ok = await confirm({
      title: '删除该规则？',
      description: `将删除规则「${rule?.name}」并立即从判定快照移除，之后不再参与证据或强制处置。此操作不可恢复。`,
      confirmText: '删除',
      danger: true,
    })
    if (!ok) return
    removeMut.mutate(
      { action: 'remove', id },
      {
        onSuccess: (resp) => {
          toast(resp.note ?? '已删除')
          nav.pop()
        },
        onError: (err) => toast(err.message),
      },
    )
  }

  return (
    <Box data-testid="rule-detail-page">
      <SectionCard>
        <ListRow primary={rule.name} secondary={sourceText(rule, tz)} badge={<RuleBadges rule={rule} />} />
        <InfoRow label="正则">
          <Box
            component="code"
            sx={{ fontFamily: MONO, fontSize: 13, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}
          >
            {rule.pattern}
          </Box>
        </InfoRow>
        {rule.note !== '' && <InfoRow label="备注">{rule.note}</InfoRow>}
        {rule.category !== '' && <InfoRow label="分类">{rule.category}</InfoRow>}
        <InfoRow label="命中">
          {rule.hits} 次
          {rule.last_matched > 0 ? ` · 最近 ${fmtTS(rule.last_matched, tz)}` : ''}
        </InfoRow>
        <InfoRow label="最近测试">
          {rule.last_tested_at > 0
            ? `${fmtTS(rule.last_tested_at, tz)} · 扫描 ${rule.last_scanned} · TP ${rule.last_tp} · FP ${rule.last_fp}${
                rule.last_undone > 0 ? ` · 已撤销 ${rule.last_undone}` : ''
              } · 覆盖率 ${pct(rule.last_tp, rule.last_ads_total) ?? '—'}`
            : '从未测试'}
        </InfoRow>
        <KindCoverageList kinds={rule.last_kinds} />
      </SectionCard>

      <SectionCard>
        <SwitchRow
          primary="启用"
          secondary={
            rule.enabled
              ? '命中会作为证据注入判定 prompt；关闭时强制也一并关闭'
              : '候选规则不参与判定；启用后才可测试与开启强制'
          }
          checked={rule.enabled}
          disabled={toggleMut.isPending}
          onChange={toggleEnabled}
        />
        <SwitchRow
          primary="强制"
          secondary={rule.enforce ? '命中即最高档处置（零 AI 成本）' : enforceHint(rule)}
          checked={rule.enforce}
          disabled={enforceDisabled}
          onChange={toggleEnforce}
        />
      </SectionCard>

      <SectionCard title="全库误封测试">
        <Box sx={{ px: 2, py: 1.5 }}>
          <Button
            fullWidth
            variant="outlined"
            loading={testMut.isPending}
            disabled={testMut.isPending}
            onClick={runTest}
          >
            重新跑全库测试
          </Button>
          <Typography sx={{ mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}>
            在全部判定历史上试跑这条正则：FP（正常消息或已撤销的处罚）为 0
            且测试过，服务端才允许开启强制。
          </Typography>
        </Box>
        {test !== null && <RuleTestPanel test={test} tz={tz} />}
      </SectionCard>

      <SectionCard title="危险区">
        <ListRow
          primary="删除该规则"
          secondary="删除后立即从判定快照移除；此操作不可恢复"
          disabled={removeMut.isPending}
          onClick={() => void removeRule()}
          sx={{ color: 'error.main' }}
        />
      </SectionCard>
    </Box>
  )
}
