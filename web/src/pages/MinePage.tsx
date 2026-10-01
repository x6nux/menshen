// 我的（计划 4.6）：身份卡 + 功能入口 + 页脚说明。
// 可见性按 2.1 矩阵收敛：名单管理全员可用；上游渠道 / 模型定价 / 全局设置
// 只有主管理员能看（服务端仍是唯一裁决，这里只是入口不出现）。
import { Box, Typography } from '@mui/material'
import { errorStatus } from '../api/client'
import { useMiniState } from '../api/hooks'
import { useNav } from '../nav'
import { ErrorState, ListRow, SectionCard, Skeletons } from '../ui'

export function MinePage() {
  const nav = useNav()
  const state = useMiniState(true)

  if (state.isPending) return <Skeletons rows={3} />
  if (state.isError) {
    return <ErrorState status={errorStatus(state.error)} onRetry={() => void state.refetch()} />
  }

  const { me } = state.data
  const role = me.main ? '主管理员' : '次级管理员'

  return (
    <Box data-testid="mine-page">
      <SectionCard>
        <Box sx={{ px: 2, py: 1.5 }}>
          <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1 }}>
            <Typography component="h2" sx={{ fontSize: 17, fontWeight: 600 }}>
              {role}
            </Typography>
            <Typography sx={{ fontSize: 13, color: 'text.secondary' }}>uid {me.uid}</Typography>
          </Box>
          <Typography sx={{ mt: 0.5, fontSize: 13, color: 'text.secondary', lineHeight: 1.6 }}>
            {me.main
              ? '可以管理全部机器人、群组、名单与全局设置。'
              : '可以管理自己名下的机器人、群组，以及联合封禁名单。'}
          </Typography>
        </Box>
      </SectionCard>

      <SectionCard title="管理">
        <ListRow
          primary="名单管理"
          secondary={me.main ? '白名单、资料放行、联合封禁与次级管理员' : '联合封禁名单'}
          chevron
          onClick={() => nav.push({ k: 'lists', section: me.main ? 'white' : 'gban' })}
        />
        {me.main && (
          <>
            <ListRow
              primary="上游渠道"
              secondary="API 渠道、密钥与能力开关"
              chevron
              onClick={() => nav.push({ k: 'upstreams' })}
            />
            <ListRow
              primary="模型定价"
              secondary="登记模型、单价与启用状态"
              chevron
              onClick={() => nav.push({ k: 'models' })}
            />
            <ListRow
              primary="全局设置"
              secondary="参数、默认模型、时区与形态摘要"
              chevron
              onClick={() => nav.push({ k: 'settings' })}
            />
          </>
        )}
      </SectionCard>

      <Typography
        sx={{ px: 1.5, mt: 1, fontSize: 12, color: 'text.secondary', lineHeight: 1.7 }}
      >
        本页仅管理员可见；接入新 bot 请在私聊面板操作。
      </Typography>
    </Box>
  )
}
