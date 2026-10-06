// oxlint-disable react/set-state-in-effect -- 草稿态需要跟随服务端返回的值重置（保存后刷新）。
// 上游详情：改名、渠道配置、启停、连通性测试、删除。
import { useEffect, useState } from 'react'
import {
  Box,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import { useUpstreamMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { CardBlock, ConfirmButton, InfoList, PageHeader, StatusBadge } from '../components'
import { useRunFeedback } from '../feedback'
import { KINDS, kindCaps } from './upstreamKinds'
import type { UpstreamKind, UpstreamTestResp } from '../../api/types'

export function UpstreamDetailView({ id }: { id: number }) {
  const state = useMiniState(true)
  const run = useRunFeedback()
  const mut = useUpstreamMutation<UpstreamTestResp>()

  if (!state.data) return null
  const up = (state.data.upstreams ?? []).find((u) => u.id === id)
  if (!up) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>上游不存在。</Typography>
      </CardBlock>
    )
  }

  return (
    <>
      <PageHeader
        title={up.name}
        subtitle={`渠道 ${KINDS.find((k) => k.value === up.kind)?.label ?? up.kind}`}
        actions={<StatusBadge info={up.status ? { label: '启用', tone: 'ok' } : { label: '停用', tone: 'neutral' }} />}
      />

      <CardBlock
        title="启停"
        actions={
          <Switch
            checked={up.status}
            disabled={mut.isPending}
            onChange={(e) =>
              run(mut.mutateAsync({ action: 'update', id: up.id, status: e.target.checked }), e.target.checked ? '已启用' : '已停用')
            }
          />
        }
      >
        <Typography sx={{ fontSize: 13, color: 'text.secondary', lineHeight: 1.8 }}>
          停用后该上游不参与选路，其名下模型的判定请求会失败。
        </Typography>
      </CardBlock>

      <ConfigCard up={{ id: up.id, name: up.name, kind: up.kind, base_url: up.base_url, supports_chat: up.supports_chat, supports_systemone: up.supports_systemone }} />

      <CardBlock title="连通性测试">
        <Box sx={{ display: 'flex', gap: 1.5, alignItems: 'center', flexWrap: 'wrap' }}>
          <Button
            variant="outlined"
            disabled={mut.isPending}
            onClick={() => {
              mut.mutate({ action: 'test', id: up.id })
            }}
          >
            测试
          </Button>
          {mut.data &&
            (mut.data.ok ? (
              <Typography sx={{ fontSize: 13, color: 'success.main' }}>
                连通正常{mut.data.latency_ms !== undefined ? ` · ${mut.data.latency_ms} ms` : ''}
                {mut.data.model ? ` · ${mut.data.model}` : ''}
              </Typography>
            ) : (
              <Typography sx={{ fontSize: 13, color: 'error.main' }}>{mut.data.error || '测试失败'}</Typography>
            ))}
        </Box>
      </CardBlock>

      <CardBlock title="其他">
        <InfoList
          items={[
            { label: 'id', value: up.id },
            { label: 'api_key', value: up.api_key },
            { label: '权重', value: up.weight },
          ]}
        />
        <Box sx={{ mt: 2 }}>
          <ConfirmButton
            variant="outlined"
            color="error"
            danger
            title="删除上游"
            description="名下有模型时禁止删除——会留下一批绑定的上游不存在的死引用。"
            confirmLabel="删除"
            disabled={mut.isPending}
            onConfirm={() => run(mut.mutateAsync({ action: 'remove', id: up.id }), '已删除')}
          >
            删除上游
          </ConfirmButton>
        </Box>
      </CardBlock>
    </>
  )
}

function ConfigCard({
  up,
}: {
  up: {
    id: number
    name: string
    kind: UpstreamKind
    base_url: string
    supports_chat: boolean
    supports_systemone: boolean
  }
}) {
  const run = useRunFeedback()
  const mut = useUpstreamMutation()
  const [name, setName] = useState(up.name)
  const [kind, setKind] = useState<UpstreamKind>(up.kind)
  const [baseURL, setBaseURL] = useState(up.base_url)
  const [apiKey, setApiKey] = useState('')
  useEffect(() => {
    setName(up.name)
    setKind(up.kind)
    setBaseURL(up.base_url)
    setApiKey('')
  }, [up.id, up.name, up.kind, up.base_url])

  const dirty = name !== up.name || kind !== up.kind || baseURL !== up.base_url || apiKey !== ''

  return (
    <CardBlock title="配置">
      <Box sx={{ display: 'grid', gap: 2, maxWidth: 560 }}>
        <TextField label="名称" value={name} onChange={(e) => setName(e.target.value)} helperText="改名会连带改写模型名与绑定，谨慎操作" fullWidth />
        <FormControl size="small" fullWidth>
          <InputLabel>渠道类型</InputLabel>
          <Select label="渠道类型" value={kind} onChange={(e) => setKind(e.target.value as UpstreamKind)}>
            {KINDS.map((k) => (
              <MenuItem key={k.value} value={k.value}>
                {k.label}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <TextField label="base_url" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} helperText="留空用渠道默认端点" fullWidth />
        <TextField
          label="api_key"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          helperText="留空表示不修改；当前为掩码显示"
          fullWidth
        />
        <Box>
          <Button
            variant="contained"
            disabled={!dirty || mut.isPending}
            onClick={() => {
              const caps = kindCaps(kind)
              run(
                mut.mutateAsync({
                  action: 'update',
                  id: up.id,
                  kind,
                  name,
                  supports_chat: caps.chat,
                  supports_systemone: caps.systemone,
                  ...(baseURL.trim() !== '' ? { base_url: baseURL.trim() } : {}),
                  ...(apiKey.trim() !== '' ? { api_key: apiKey.trim() } : {}),
                }),
                '已保存',
              )
            }}
          >
            保存配置
          </Button>
        </Box>
      </Box>
    </CardBlock>
  )
}
