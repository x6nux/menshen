// 全局设置：
// - 迁移不变量 1：三个总开关提交 set 时 value 必须是字符串 '1'/'0'，成功后刷新；
// - 智能控件：controlKind（toggle → Switch）、时长预设按 min/max 过滤；
// - 默认模型快选、时区、附加链接、形态摘要与修正文本；
// - 次管访问按 403 处理。
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { State } from '../api/types'
import { mockState } from '../mocks/fixtures'
import { renderPage } from '../test/renderPage'
import { server, startTestServer } from '../test/server'
import { SettingsPage } from './SettingsPage'

startTestServer()

function useState(state: State) {
  server.use(http.post('*/miniapp/api/state', () => HttpResponse.json(state)))
}

function capturePost(op: string, bodies: Record<string, unknown>[]) {
  server.use(
    http.post(`*/miniapp/api/${op}`, async ({ request }) => {
      bodies.push((await request.json()) as Record<string, unknown>)
      return HttpResponse.json({ ok: true })
    }),
  )
}

async function renderSettings() {
  renderPage(<SettingsPage />)
  expect(await screen.findByText('总开关')).toBeInTheDocument()
}

describe('SettingsPage 总开关（迁移不变量：字符串 1/0 + 成功后刷新）', () => {
  it('三个开关提交字符串值，成功后失效刷新 state', async () => {
    const bodies: Record<string, unknown>[] = []
    let stateCalls = 0
    server.use(
      http.post('*/miniapp/api/state', () => {
        stateCalls++
        return HttpResponse.json(mockState)
      }),
      http.post('*/miniapp/api/set', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json({ ok: true })
      }),
    )
    await renderSettings()
    const before = stateCalls

    fireEvent.click(screen.getByRole('switch', { name: '反广告总开关' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ scope: 'global', key: 'antiad_enabled', value: '0' })
    expect(typeof bodies[0].value).toBe('string')
    await waitFor(() => expect(stateCalls).toBeGreaterThan(before))

    fireEvent.click(screen.getByRole('switch', { name: '告警抄送主管理员' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ scope: 'global', key: 'alert_copy_main', value: '1' })
    expect(typeof bodies[1].value).toBe('string')

    fireEvent.click(screen.getByRole('switch', { name: '联合封禁' }))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toEqual({ scope: 'global', key: 'gban_enabled', value: '0' })
    expect(typeof bodies[2].value).toBe('string')
  })

  it('开关乐观翻转，失败回滚并 toast 服务端文案', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.post('*/miniapp/api/set', async () => {
        await gate
        return HttpResponse.json({ error: '保存失败' }, { status: 500 })
      }),
    )
    await renderSettings()

    const sw = screen.getByRole('switch', { name: '反广告总开关' })
    expect(sw).toBeChecked()
    fireEvent.click(sw)
    // 乐观：服务端响应被 gate 拦住时 UI 已翻转
    await waitFor(() =>
      expect(screen.getByRole('switch', { name: '反广告总开关' })).not.toBeChecked(),
    )
    act(() => release())
    // 失败回滚并提示服务端文案
    await waitFor(() => expect(screen.getByRole('switch', { name: '反广告总开关' })).toBeChecked())
    expect(await screen.findByText('保存失败')).toBeInTheDocument()
  })
})

describe('SettingsPage 主题折叠卡（智能控件）', () => {
  it('卡头显示已设置项数；toggle 是 Switch，number 是按钮值 + 换算文案', async () => {
    await renderSettings()

    expect(screen.getByText('已设置 2 项')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /处置与分档/ }))
    // min=0,max=1 → Switch（不是文本输入）
    expect(screen.getByRole('switch', { name: '禁言改为封禁' })).toBeInTheDocument()
    // 时长类换算文案
    expect(screen.getByText('1440 分钟（= 1 天）')).toBeInTheDocument()
  })

  it('「已设置 N 项」只数显式写过的键（global 里的默认值不算）', async () => {
    await renderSettings()
    // global 是生产形状：铺满了代码默认值（antiad_so_trust=95、
    // antiad_hedge_minutes=5…）；settings_set 只列 fixture 显式配置的键。
    expect(screen.getByText('已设置 2 项')).toBeInTheDocument()
    expect(screen.getAllByText('已设置 0 项')).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: /判定与模型/ }))
    // 默认值行照常展示，但不计入「已设置」
    expect(screen.getByText('采信线：systemone 置信度')).toBeInTheDocument()
    expect(screen.getByText('并发模式持续（分钟）')).toBeInTheDocument()
  })

  it('时长类抽屉的预设按 min/max 过滤，越界值被拦截', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    await renderSettings()

    fireEvent.click(screen.getByRole('button', { name: /判定与模型/ }))
    const row = screen.getByText('并发模式持续（分钟）').closest('li')
    expect(row).not.toBeNull()
    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: '编辑' }))

    expect(await screen.findByText('设置：并发模式持续（分钟）')).toBeInTheDocument()
    // min=1, max=1440：0（永久）与 7 天档位必须被过滤
    expect(screen.getByText('1 小时')).toBeInTheDocument()
    expect(screen.getByText('1 天')).toBeInTheDocument()
    expect(screen.queryByText('永久')).not.toBeInTheDocument()
    expect(screen.queryByText('7 天')).not.toBeInTheDocument()

    const input = screen.getByLabelText('并发模式持续（分钟）')
    fireEvent.change(input, { target: { value: '2000' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('不能大于 1440')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)

    fireEvent.change(input, { target: { value: '120' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ scope: 'global', key: 'antiad_hedge_minutes', value: '120' })
  })
})

describe('SettingsPage 特殊卡', () => {
  it('默认模型：快选 chip 写入逗号列表', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    await renderSettings()

    fireEvent.click(screen.getByRole('button', { name: /判定模型（systemone）/ }))
    fireEvent.click(await screen.findByText('demo/gpt-5-mini'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      scope: 'global',
      key: 'antiad_so_models',
      value: 'demo/gpt-5-mini',
    })
  })

  it('规则发现模型：行内回显全局值，抽屉展示配置提示', async () => {
    useState({
      ...structuredClone(mockState),
      global: { ...mockState.global, antiad_rule_model: 'demo/gpt-5-mini' },
    })
    await renderSettings()

    const row = screen.getByRole('button', { name: /规则发现模型（AI 必封规则）/ })
    expect(within(row).getByText('demo/gpt-5-mini')).toBeInTheDocument()
    expect(screen.getByText('留空 = 复判模型列表里第一个 OpenAI 兼容模型')).toBeInTheDocument()

    fireEvent.click(row)
    expect(
      await screen.findByText('形如 上游名/模型ID；留空 = 复判模型列表里第一个 OpenAI 兼容模型'),
    ).toBeInTheDocument()
  })

  it('规则发现模型：抽屉快选 chip，保存写 set scope=global', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    await renderSettings()

    fireEvent.click(screen.getByRole('button', { name: /规则发现模型（AI 必封规则）/ }))
    fireEvent.click(await screen.findByText('demo/gpt-5-mini'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      scope: 'global',
      key: 'antiad_rule_model',
      value: 'demo/gpt-5-mini',
    })
  })

  it('展示时区：常用时区 chip + 自定义输入', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    await renderSettings()

    fireEvent.click(screen.getByRole('button', { name: /展示时区/ }))
    fireEvent.click(await screen.findByText('Europe/London'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ scope: 'global', key: 'tz_name', value: 'Europe/London' })

    // 自定义 IANA（等时区抽屉退场后再点开）
    fireEvent.click(await screen.findByRole('button', { name: /展示时区/ }))
    fireEvent.change(await screen.findByLabelText('自定义 IANA 时区'), {
      target: { value: 'Asia/Tokyo' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ scope: 'global', key: 'tz_name', value: 'Asia/Tokyo' })
  })

  it('群内提示附加链接：文本写 set scope=global', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('set', bodies)
    await renderSettings()

    fireEvent.click(screen.getByText('附加文本'))
    fireEvent.change(await screen.findByLabelText('附加文本'), {
      target: { value: '② 使用指南 (https://t.me/guide)' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({
      scope: 'global',
      key: 'antiad_group_footer',
      value: '② 使用指南 (https://t.me/guide)',
    })
  })

  it('形态摘要：抽屉保存 + 立即重新总结；修正文本 save_fix', async () => {
    const bodies: Record<string, unknown>[] = []
    capturePost('digest', bodies)
    await renderSettings()

    fireEvent.click(screen.getByText('编辑摘要'))
    fireEvent.change(await screen.findByLabelText('摘要正文'), {
      target: { value: '（mock）新的摘要' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存摘要' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ action: 'save', value: '（mock）新的摘要' })

    fireEvent.click(await screen.findByRole('button', { name: '立即重新总结' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({ action: 'run' })

    fireEvent.click(screen.getByText('编辑修正文本'))
    fireEvent.change(await screen.findByLabelText('修正文本'), {
      target: { value: '兼职招募一律按诈骗归类' },
    })
    fireEvent.click(screen.getByRole('button', { name: '保存修正文本' }))
    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2]).toEqual({ action: 'save_fix', value: '兼职招募一律按诈骗归类' })
  })
})

describe('SettingsPage 权限', () => {
  it('次级管理员访问按 403 处理', async () => {
    useState({ ...structuredClone(mockState), me: { uid: 200, main: false } })
    renderPage(<SettingsPage />)
    expect(await screen.findByText('没有权限')).toBeInTheDocument()
    expect(screen.queryByText('总开关')).not.toBeInTheDocument()
  })
})
