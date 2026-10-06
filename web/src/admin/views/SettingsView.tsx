// oxlint-disable react/set-state-in-effect -- 草稿态需要跟随服务端返回的值重置（保存后刷新）。
// 全局设置（主管理员）：总开关、默认模型、分组参数、时区、页脚、人机验证、摘要。
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
import { useDigestMutation, useSetMutation } from '../../api/mutations'
import { useMiniState } from '../../api/hooks'
import { CAPTCHA_DEMO_PATH, controlKind, specHint, specUnit, specValueText, validateSpecValue } from '../../lib/settings'
import { openLink } from '../../telegram'
import { CardBlock, PageHeader } from '../components'
import { useRunFeedback } from '../feedback'
import type { Spec } from '../../api/types'

const MASTER_TOGGLES: { key: string; label: string; hint: string }[] = [
  { key: 'antiad_enabled', label: '反广告总开关', hint: '关闭后所有群都不判定。' },
  { key: 'gban_enabled', label: '联合封禁', hint: '关闭后联封名单不生效。' },
  { key: 'alert_copy_main', label: '私聊告警抄送', hint: '把命中与失败告警发给全部主管理员。' },
  { key: 'antiad_rule_auto', label: '规则发现自动运行', hint: '每小时最多自动跑一轮，只在新广告未被覆盖时才启动。' },
  { key: 'captcha_demo', label: '人机验证测试台', hint: '在「我的」露出测试台入口。' },
]

const MODEL_KEYS: { key: string; label: string; hint: string }[] = [
  { key: 'antiad_so_models', label: '初判模型（systemone）', hint: '逗号分隔的有序列表，重试按顺序轮换。' },
  { key: 'antiad_llm_models', label: '复判模型（llm）', hint: '逗号分隔的有序列表。' },
  { key: 'antiad_vision_model', label: '识图模型', hint: '形如 上游名/模型ID；不启用识图可留空。' },
  { key: 'antiad_rule_model', label: '规则发现模型', hint: '留空用回退模型。' },
]

const CAPTCHA_PROVIDERS = [
  { value: '', label: '未启用' },
  { value: 'turnstile', label: 'Turnstile' },
  { value: 'hcaptcha', label: 'hCaptcha' },
  { value: 'cap', label: '内置 Cap' },
]

export function SettingsView() {
  const state = useMiniState(true)
  if (!state.data) return null
  if (!state.data.me.main || !state.data.global) {
    return (
      <CardBlock>
        <Typography sx={{ fontSize: 14, color: 'text.secondary' }}>全局设置只对主管理员开放。</Typography>
      </CardBlock>
    )
  }
  const { global, sections, specs, digest, digest_fix, tz_name, settings_set } = state.data
  return (
    <SettingsBody
      global={global}
      sections={sections}
      specs={specs}
      digest={digest ?? ''}
      digestFix={digest_fix ?? ''}
      tz={tz_name ?? global.tz_name ?? ''}
      settingsSet={settings_set ?? []}
    />
  )
}

function SettingsBody({
  global,
  sections,
  specs,
  digest,
  digestFix,
  tz,
  settingsSet,
}: {
  global: Record<string, string>
  sections: { name: string; specs: Spec[] }[]
  specs: Spec[]
  digest: string
  digestFix: string
  tz: string
  settingsSet: string[]
}) {
  const setMut = useSetMutation()
  const digestMut = useDigestMutation()
  const run = useRunFeedback()
  const set = (key: string, value: string, okText = '已保存') =>
    run(setMut.mutateAsync({ scope: 'global', key, value }), okText)

  const specByKey = new Map(specs.map((s) => [s.key, s]))

  return (
    <>
      <PageHeader title="全局设置" subtitle="默认模型、护栏参数与人机验证；只对主管理员可见" />

      <CardBlock title="总开关">
        <Box sx={{ display: 'grid', gap: 1 }}>
          {MASTER_TOGGLES.filter((t) => global[t.key] !== undefined).map((t) => (
            <Box key={t.key} sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              <Box sx={{ flex: 1 }}>
                <Typography sx={{ fontSize: 13.5 }}>{t.label}</Typography>
                <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{t.hint}</Typography>
              </Box>
              <Switch
                checked={global[t.key] === '1'}
                disabled={setMut.isPending}
                onChange={(e) => set(t.key, e.target.checked ? '1' : '0', '已更新')}
              />
            </Box>
          ))}
        </Box>
      </CardBlock>

      <CardBlock title="默认模型">
        <Box sx={{ display: 'grid', gap: 2 }}>
          {MODEL_KEYS.map((m) => (
            <SettingText key={m.key} label={m.label} hint={m.hint} value={global[m.key] ?? ''} onSave={(v) => set(m.key, v)} />
          ))}
        </Box>
      </CardBlock>

      <CardBlock title="展示与落地">
        <Box sx={{ display: 'grid', gap: 2 }}>
          <SettingText label="展示时区（IANA）" hint="留空回退 Asia/Shanghai。" value={tz} onSave={(v) => set('tz_name', v.trim() || 'Asia/Shanghai')} />
          {specByKey.has('antiad_group_footer') && (
            <SettingText
              label="群内页脚"
              hint="群内处置消息附带的说明文案。"
              value={global.antiad_group_footer ?? ''}
              onSave={(v) => set('antiad_group_footer', v)}
            />
          )}
        </Box>
      </CardBlock>

      <CardBlock
        title="人机验证"
        actions={
          global.captcha_demo === '1' ? (
            <Button size="small" variant="text" onClick={() => openLink(CAPTCHA_DEMO_PATH)}>
              打开测试台
            </Button>
          ) : undefined
        }
      >
        <Box sx={{ display: 'grid', gap: 2, maxWidth: 560 }}>
          <FormControl size="small" fullWidth>
            <InputLabel>提供方</InputLabel>
            <Select
              label="提供方"
              value={global.captcha_provider ?? ''}
              disabled={setMut.isPending}
              onChange={(e) => set('captcha_provider', e.target.value, '已更新')}
            >
              {CAPTCHA_PROVIDERS.map((p) => (
                <MenuItem key={p.value} value={p.value}>
                  {p.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <SettingText label="Site key" hint="前端公钥。" value={global.captcha_site_key ?? ''} onSave={(v) => set('captcha_site_key', v)} />
          <SettingText
            label="Secret"
            hint={settingsSet.includes('captcha_secret') ? '已设置；留空保存表示清空。' : '服务端密钥，尚未设置。'}
            value=""
            placeholder="留空不修改"
            onSave={(v) => (v === '' ? undefined : set('captcha_secret', v))}
          />
          <SettingText label="测试台密钥组" hint="演示页可一次展示多家：provider=key 逗号分隔。" value={global.captcha_demo_keys ?? ''} onSave={(v) => set('captcha_demo_keys', v)} />
        </Box>
      </CardBlock>

      {sections.map((section) => (
        <CardBlock
          key={section.name}
          title={`${section.name}（已设置 ${section.specs.filter((s) => settingsSet.includes(s.key)).length} 项）`}
        >
          <Box sx={{ display: 'grid', gap: 1.5 }}>
            {section.specs.map((spec) => (
              <SpecControl key={spec.key} spec={spec} value={global[spec.key] ?? ''} disabled={setMut.isPending} onSave={(v) => set(spec.key, v)} />
            ))}
          </Box>
        </CardBlock>
      ))}

      <CardBlock title="摘要与修复文案">
        <Box sx={{ display: 'grid', gap: 2 }}>
          <MultilineSetting
            label="形态摘要"
            value={digest}
            onSave={(v) => run(digestMut.mutateAsync({ action: 'save', value: v }), '已保存')}
          />
          <MultilineSetting
            label="修复建议文案"
            value={digestFix}
            onSave={(v) => run(digestMut.mutateAsync({ action: 'save_fix', value: v }), '已保存')}
          />
          <Box>
            <Button variant="outlined" disabled={digestMut.isPending} onClick={() => run(digestMut.mutateAsync({ action: 'run' }), '已开始重跑')}>
              立即重跑摘要
            </Button>
          </Box>
        </Box>
      </CardBlock>
    </>
  )
}

function SettingText({
  label,
  hint,
  value,
  placeholder,
  onSave,
}: {
  label: string
  hint: string
  value: string
  placeholder?: string
  onSave: (v: string) => void
}) {
  const [draft, setDraft] = useState(value)
  useEffect(() => setDraft(value), [value])
  return (
    <Box>
      <TextField
        label={label}
        value={draft}
        placeholder={placeholder}
        onChange={(e) => setDraft(e.target.value)}
        helperText={hint}
        fullWidth
      />
      <Button size="small" variant="text" sx={{ mt: 0.5 }} disabled={draft === value} onClick={() => onSave(draft)}>
        保存
      </Button>
    </Box>
  )
}

function MultilineSetting({
  label,
  value,
  onSave,
}: {
  label: string
  value: string
  onSave: (v: string) => void
}) {
  const [draft, setDraft] = useState(value)
  useEffect(() => setDraft(value), [value])
  return (
    <Box>
      <TextField label={label} value={draft} onChange={(e) => setDraft(e.target.value)} multiline minRows={4} fullWidth />
      <Button size="small" variant="text" sx={{ mt: 0.5 }} disabled={draft === value} onClick={() => onSave(draft)}>
        保存
      </Button>
    </Box>
  )
}

function SpecControl({
  spec,
  value,
  disabled,
  onSave,
}: {
  spec: Spec
  value: string
  disabled: boolean
  onSave: (v: string) => void
}) {
  const [draft, setDraft] = useState(value)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => setDraft(value), [value])
  const kind = controlKind(spec)

  if (kind === 'toggle') {
    return (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
        <Box sx={{ flex: 1 }}>
          <Typography sx={{ fontSize: 13.5 }}>{spec.label}</Typography>
          <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{specHint(spec)}</Typography>
        </Box>
        <Switch
          checked={draft === '1'}
          disabled={disabled}
          onChange={(e) => onSave(e.target.checked ? '1' : '0')}
        />
      </Box>
    )
  }

  const commit = () => {
    const err = validateSpecValue(spec, draft)
    if (err) {
      setError(err)
      return
    }
    if (draft.trim() === '') {
      setError('全局设置不能留空')
      return
    }
    setError(null)
    if (draft.trim() === value) return
    onSave(draft.trim())
  }

  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
      <Box sx={{ flex: 1 }}>
        <Typography sx={{ fontSize: 13.5 }}>{spec.label}</Typography>
        <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{specHint(spec)}</Typography>
      </Box>
      <TextField
        value={draft}
        disabled={disabled}
        error={error !== null}
        helperText={error ?? (draft === '' ? '' : specValueText(draft, spec))}
        sx={{ width: 220 }}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit()
        }}
        slotProps={{ input: { endAdornment: specUnit(spec) ? <Typography sx={{ fontSize: 12, color: 'text.secondary' }}>{specUnit(spec)}</Typography> : undefined } }}
      />
    </Box>
  )
}
