// AI 必封规则（主管理员）：规则列表、发现 Agent、新建/测试/启停/强制/删除。
import { useState } from 'react'
import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import { useRuleAgent, useRules, useMiniState } from '../../api/hooks'
import { useRulesMutation } from '../../api/mutations'
import { fmtTS } from '../../lib/format'
import { SearchField } from '../../ui'
import { CardBlock, ConfirmButton, DataTable, InfoList, MonoText, PageHeader, StatCard, StatusBadge, Toolbar } from '../components'
import type { Column } from '../components'
import { useRunFeedback } from '../feedback'
import { useAdminNav } from '../nav'
import type { Rule, RuleSaveResp, RuleTest } from '../../api/types'

/** canEnforce 强制开关的服务端门：启用、测过且零误封才允许打开。 */
function canEnforce(rule: Rule): boolean {
  return rule.enabled && rule.last_tested_at > 0 && rule.last_fp === 0 && rule.last_undone === 0
}

export function RulesView() {
  const state = useMiniState(true)
  const nav = useAdminNav()
  const run = useRunFeedback()
  const main = state.data?.me.main ?? false
  const rules = useRules(main)
  const agent = useRuleAgent(main)
  const mut = useRulesMutation()

  const [q, setQ] = useState('')
  const [filter, setFilter] = useState('all')
  const [sort, setSort] = useState('default')
  const [saveOpen, setSaveOpen] = useState(false)
  const [testOpen, setTestOpen] = useState(false)

  if (!main) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>
          AI 必封规则只对主管理员开放。
        </Typography>
      </CardBlock>
    )
  }

  const all = rules.data?.rules ?? []
  const rows = sortRules(
    all.filter((r) => matchesFilter(r, filter)).filter((r) => `${r.name} ${r.pattern}`.includes(q.trim())),
    sort,
  )

  const columns: Column<Rule>[] = [
    { key: 'name', header: '名称', width: 180, render: (row) => row.name },
    { key: 'pattern', header: '正则', render: (row) => <MonoText>{row.pattern}</MonoText> },
    { key: 'category', header: '类型', width: 110, render: (row) => row.category || '—' },
    {
      key: 'flags',
      header: '状态',
      width: 140,
      render: (row) => (
        <Box sx={{ display: 'flex', gap: 0.5 }}>
          <StatusBadge info={row.enabled ? { label: '启用', tone: 'ok' } : { label: '候选', tone: 'neutral' }} />
          {row.enforce && <StatusBadge info={{ label: '强制', tone: 'warn' }} />}
        </Box>
      ),
    },
    { key: 'hits', header: '命中', width: 80, align: 'right', render: (row) => row.hits },
    { key: 'tp', header: 'TP', width: 70, align: 'right', render: (row) => row.last_tp },
    { key: 'fp', header: 'FP', width: 70, align: 'right', render: (row) => row.last_fp },
    {
      key: 'coverage',
      header: '覆盖率',
      width: 90,
      align: 'right',
      render: (row) => (row.last_ads_total > 0 ? `${Math.round((row.last_tp / row.last_ads_total) * 100)}%` : '—'),
    },
    { key: 'tested', header: '最近测试', width: 140, render: (row) => (row.last_tested_at ? fmtTS(row.last_tested_at) : '未测试') },
    {
      key: 'actions',
      header: '',
      width: 200,
      render: (row) => (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }} onClick={(e) => e.stopPropagation()}>
          <Switch
            size="small"
            checked={row.enabled}
            disabled={mut.isPending}
            onChange={(e) => run(mut.mutateAsync({ action: 'toggle', id: row.id, enabled: e.target.checked }), '已更新')}
          />
          <Switch
            size="small"
            color="warning"
            checked={row.enforce}
            disabled={mut.isPending || (!row.enforce && !canEnforce(row))}
            onChange={(e) => run(mut.mutateAsync({ action: 'enforce', id: row.id, enforce: e.target.checked }), '已更新')}
          />
          <ConfirmButton size="small" color="error" variant="text" title="删除规则" description={`确认删除「${row.name}」？`} confirmLabel="删除" danger onConfirm={() => run(mut.mutateAsync({ action: 'remove', id: row.id }), '已删除')}>
            删除
          </ConfirmButton>
        </Box>
      ),
    },
  ]

  const a = agent.data?.agent

  return (
    <>
      <PageHeader
        title="AI 必封规则"
        subtitle="命中即处置或作为判定证据；强制开关需零误封才能打开"
        actions={
          <>
            <Button variant="outlined" onClick={() => setTestOpen(true)}>
              测试正则
            </Button>
            <Button variant="contained" onClick={() => setSaveOpen(true)}>
              ＋ 新建规则
            </Button>
          </>
        }
      />

      <CardBlock
        title="规则发现"
        actions={
          a?.running ? (
            <Button size="small" variant="outlined" color="error" disabled={mut.isPending} onClick={() => run(mut.mutateAsync({ action: 'agent_stop' }), '已停止')}>
              停止
            </Button>
          ) : (
            <Button size="small" variant="contained" disabled={mut.isPending} onClick={() => run(mut.mutateAsync({ action: 'agent_start' }), '已启动')}>
              开始发现
            </Button>
          )
        }
      >
        {a?.running ? (
          <Typography sx={{ fontSize: 13.5 }}>
            运行中（已 {a.steps_count} 步）
            {a.target_log_id > 0 ? ` · 针对记录 #${a.target_log_id}` : ' · 全库模式'}
            {a.started_at ? ` · 开始于 ${fmtTS(a.started_at)}` : ''}
          </Typography>
        ) : (
          <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.8 }}>
            {a?.result || a?.error || '未在运行。启动后 Agent 会从历史封禁流水里探索规则，一轮可能持续几十分钟。'}
          </Typography>
        )}
      </CardBlock>

      <Toolbar>
        <SearchField value={q} onChange={setQ} placeholder="搜索名称 / 正则" sx={{ maxWidth: 300 }} />
        <FormControl size="small" sx={{ minWidth: 140 }}>
          <InputLabel>状态</InputLabel>
          <Select label="状态" value={filter} onChange={(e) => setFilter(e.target.value)}>
            <MenuItem value="all">全部</MenuItem>
            <MenuItem value="enabled">启用</MenuItem>
            <MenuItem value="candidate">候选</MenuItem>
            <MenuItem value="enforce">强制</MenuItem>
          </Select>
        </FormControl>
        <FormControl size="small" sx={{ minWidth: 160 }}>
          <InputLabel>排序</InputLabel>
          <Select label="排序" value={sort} onChange={(e) => setSort(e.target.value)}>
            <MenuItem value="default">默认</MenuItem>
            <MenuItem value="coverage">覆盖率</MenuItem>
            <MenuItem value="tp">命中广告</MenuItem>
            <MenuItem value="hits">规则命中</MenuItem>
            <MenuItem value="recent">最近创建</MenuItem>
          </Select>
        </FormControl>
      </Toolbar>

      <DataTable
        loading={rules.isPending}
        rows={rows}
        rowKey={(row) => row.id}
        onRowClick={(row) => nav.go({ k: 'rule', id: row.id })}
        empty={<Typography sx={{ fontSize: 13, color: 'text.secondary' }}>没有匹配的规则。</Typography>}
        columns={columns}
      />

      <SaveRuleDialog open={saveOpen} onClose={() => setSaveOpen(false)} onSaved={(resp) => run(Promise.resolve(resp), '已创建')} />
      <TestPatternDialog open={testOpen} onClose={() => setTestOpen(false)} />
    </>
  )
}

function matchesFilter(rule: Rule, filter: string): boolean {
  if (filter === 'enabled') return rule.enabled
  if (filter === 'candidate') return !rule.enabled
  if (filter === 'enforce') return rule.enforce
  return true
}

function sortRules(rows: Rule[], sort: string): Rule[] {
  const copy = [...rows]
  switch (sort) {
    case 'coverage':
      return copy.sort((x, y) => coverage(y) - coverage(x))
    case 'tp':
      return copy.sort((x, y) => y.last_tp - x.last_tp)
    case 'hits':
      return copy.sort((x, y) => y.hits - x.hits)
    case 'recent':
      return copy.sort((x, y) => y.created_at - x.created_at)
    default:
      return copy
  }
}

function coverage(rule: Rule): number {
  return rule.last_ads_total > 0 ? rule.last_tp / rule.last_ads_total : 0
}

function SaveRuleDialog({
  open,
  onClose,
  onSaved,
}: {
  open: boolean
  onClose: () => void
  onSaved: (resp: RuleSaveResp) => void
}) {
  const mut = useRulesMutation<RuleSaveResp>()
  const [name, setName] = useState('')
  const [pattern, setPattern] = useState('')
  const [category, setCategory] = useState('')
  const [note, setNote] = useState('')

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle sx={{ fontSize: 17 }}>新建必封规则</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'grid', gap: 2, pt: 0.5 }}>
          <TextField label="名称" value={name} onChange={(e) => setName(e.target.value)} fullWidth />
          <TextField label="正则" value={pattern} onChange={(e) => setPattern(e.target.value)} helperText="保存时会自动跑一轮全库测试；出现误封样本将拒绝创建" multiline minRows={2} fullWidth />
          <TextField label="类型（ad_kind）" value={category} onChange={(e) => setCategory(e.target.value)} fullWidth />
          <TextField label="备注" value={note} onChange={(e) => setNote(e.target.value)} fullWidth />
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button
          variant="contained"
          disabled={pattern.trim() === '' || name.trim() === '' || mut.isPending}
          onClick={() => {
            mut.mutate(
              { action: 'save', id: 0, name: name.trim(), pattern: pattern.trim(), category: category.trim(), note: note.trim() },
              {
                onSuccess: (resp) => {
                  onSaved(resp)
                  onClose()
                },
              },
            )
          }}
        >
          保存并测试
        </Button>
      </DialogActions>
    </Dialog>
  )
}

function TestPatternDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const mut = useRulesMutation<{ test: RuleTest }>()
  const [pattern, setPattern] = useState('')
  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle sx={{ fontSize: 17 }}>测试正则</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'grid', gap: 2, pt: 0.5 }}>
          <Box sx={{ display: 'flex', gap: 1 }}>
            <TextField label="正则" value={pattern} onChange={(e) => setPattern(e.target.value)} fullWidth />
            <Button variant="contained" disabled={pattern.trim() === '' || mut.isPending} onClick={() => mut.mutate({ action: 'test', pattern: pattern.trim() })}>
              测试
            </Button>
          </Box>
          {mut.data && <TestSummary test={mut.data.test} />}
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>关闭</Button>
      </DialogActions>
    </Dialog>
  )
}

/** TestSummary 全库测试结果：命中分类统计 + 覆盖率 + 按类型细分。 */
export function TestSummary({ test }: { test: RuleTest }) {
  return (
    <Box>
      <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(130px,1fr))', gap: 1.5, mb: 1.5 }}>
        <StatCard label="扫描" value={test.scanned} />
        <StatCard label="命中" value={test.matched} />
        <StatCard label="TP（广告）" value={test.tp} />
        <StatCard label="FP（误封）" value={test.fp} />
        <StatCard label="已撤销" value={test.undone} />
        <StatCard label="覆盖率" value={`${Math.round(test.coverage * 100)}%`} />
      </Box>
      {test.kinds.length > 0 && (
        <Box sx={{ mb: 1.5 }}>
          {test.kinds.map((k) => (
            <Typography key={k.kind} sx={{ fontSize: 12.5, color: 'text.secondary' }}>
              {k.kind}：{k.matched} / {k.total}
            </Typography>
          ))}
        </Box>
      )}
      {test.fp_samples.length > 0 && (
        <Typography sx={{ fontSize: 13, color: 'error.main', mb: 1 }}>
          出现误封样本，规则不能创建/强制。
        </Typography>
      )}
      {test.fp_samples.slice(0, 5).map((s) => (
        <Typography key={s.id} sx={{ fontSize: 12.5, color: 'text.secondary', mb: 0.5 }}>
          #{s.id} {s.text}
        </Typography>
      ))}
    </Box>
  )
}

export function RuleDetailView({ id }: { id: number }) {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const main = state.data?.me.main ?? false
  const rules = useRules(main)
  const mut = useRulesMutation<{ test: RuleTest }>()

  if (!main) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>AI 必封规则只对主管理员开放。</Typography>
      </CardBlock>
    )
  }
  const rule = (rules.data?.rules ?? []).find((r) => r.id === id)
  if (!rule) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>规则不存在。</Typography>
      </CardBlock>
    )
  }

  return (
    <>
      <PageHeader
        title={rule.name}
        subtitle={`规则 #${rule.id} · ${rule.source}`}
        actions={
          <Box sx={{ display: 'flex', gap: 0.5 }}>
            <StatusBadge info={rule.enabled ? { label: '启用', tone: 'ok' } : { label: '候选', tone: 'neutral' }} />
            {rule.enforce && <StatusBadge info={{ label: '强制', tone: 'warn' }} />}
          </Box>
        }
      />

      <CardBlock title="正则">
        <MonoText>{rule.pattern}</MonoText>
      </CardBlock>

      <CardBlock
        title="开关"
        actions={
          <Box sx={{ display: 'flex', gap: 2 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
              <Typography sx={{ fontSize: 13 }}>启用</Typography>
              <Switch size="small" checked={rule.enabled} disabled={mut.isPending} onChange={(e) => run(mut.mutateAsync({ action: 'toggle', id, enabled: e.target.checked }), '已更新')} />
            </Box>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
              <Typography sx={{ fontSize: 13 }}>强制</Typography>
              <Switch
                size="small"
                color="warning"
                checked={rule.enforce}
                disabled={mut.isPending || (!rule.enforce && !canEnforce(rule))}
                onChange={(e) => run(mut.mutateAsync({ action: 'enforce', id, enforce: e.target.checked }), '已更新')}
              />
            </Box>
          </Box>
        }
      >
        <Typography sx={{ fontSize: 12.5, color: 'text.secondary', lineHeight: 1.8 }}>
          强制需启用、测试过且零误封；未强制时命中作为证据交复判定案。
        </Typography>
      </CardBlock>

      <CardBlock title="最近测试">
        <InfoList
          items={[
            { label: '测试时间', value: rule.last_tested_at ? fmtTS(rule.last_tested_at) : '未测试' },
            { label: '扫描', value: rule.last_scanned },
            { label: 'TP / FP / 撤销', value: `${rule.last_tp} / ${rule.last_fp} / ${rule.last_undone}` },
            { label: '规则命中', value: rule.hits },
            { label: '最近命中', value: rule.last_matched ? fmtTS(rule.last_matched) : '从未' },
          ]}
        />
        <Box sx={{ mt: 2 }}>
          <Button variant="outlined" disabled={mut.isPending} onClick={() => mut.mutate({ action: 'test', id })}>
            重新跑全库测试
          </Button>
        </Box>
        {mut.data && (
          <Box sx={{ mt: 2 }}>
            <TestSummary test={mut.data.test} />
          </Box>
        )}
      </CardBlock>

      <CardBlock title="其他">
        <InfoList
          items={[
            { label: '类型', value: rule.category || '—' },
            { label: '备注', value: rule.note || '—' },
            { label: '创建时间', value: fmtTS(rule.created_at) },
          ]}
        />
        <Box sx={{ mt: 2 }}>
          <ConfirmButton variant="outlined" color="error" danger title="删除规则" description={`确认删除「${rule.name}」？`} confirmLabel="删除" onConfirm={() => run(mut.mutateAsync({ action: 'remove', id }), '已删除')}>
            删除规则
          </ConfirmButton>
        </Box>
      </CardBlock>
    </>
  )
}
