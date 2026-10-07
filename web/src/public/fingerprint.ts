// 浏览器特征采集。
//
// 采集结果原样 POST 给服务端（web_checks.signals），硬信号在服务端复核：
// 客户端能伪造这些值，所以只作为 Turnstile 之外的补充证据。
//
// JSON 字段名与 Go 侧 webSignals 的 tag 一一对应（snake_case）。

export interface WebSignals {
  ua: string
  platform: string
  timezone: string
  languages: string[]
  webdriver: boolean
  automation: string[]
  doc_props: string[]
  screen: { w: number; h: number; depth: number; dpr: number }
  hardware_concurrency: number
  device_memory: number
  max_touch_points: number
  canvas_hash: string
  webgl_vendor: string
  webgl_renderer: string
  audio_hash: number
  fonts: string[]
  plugins: number
  notification_permission: string
  permission_query: string
}

const AUTOMATION_GLOBALS = [
  'callPhantom',
  '_phantom',
  '__nightmare',
  'domAutomation',
  'domAutomationController',
  '_selenium',
  '__webdriver_evaluate',
  '__selenium_unwrapped',
  '__fxdriver_unwrapped',
]

const FONT_PROBES = [
  'Arial',
  'Courier New',
  'Georgia',
  'Times New Roman',
  'Verdana',
  'Tahoma',
  'Trebuchet MS',
  'Impact',
  'Comic Sans MS',
  'Segoe UI',
  'Calibri',
  'Cambria',
  'Consolas',
  'Helvetica',
  'Roboto',
  'Ubuntu',
  'Noto Sans',
  'PingFang SC',
  'Microsoft YaHei',
  'SimSun',
  'SimHei',
  'WenQuanYi Micro Hei',
  'Apple Color Emoji',
  'Menlo',
  'Monaco',
  'Courier',
  'Futura',
  'Gill Sans',
  'Optima',
  'Palatino',
]

async function sha256hex(str: string): Promise<string> {
  if (!window.crypto?.subtle) return ''
  try {
    const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(str))
    return Array.from(new Uint8Array(buf))
      .map((b) => ('00' + b.toString(16)).slice(-2))
      .join('')
  } catch {
    return ''
  }
}

/** collectSignals 采集全部特征；任何一项失败都不影响整份结果。 */
export async function collectSignals(): Promise<WebSignals> {
  const s: WebSignals = {
    ua: navigator.userAgent,
    platform: navigator.platform,
    timezone: '',
    languages: Array.from(navigator.languages ?? []),
    webdriver: navigator.webdriver === true,
    automation: [],
    doc_props: [],
    screen: {
      w: screen.width,
      h: screen.height,
      depth: screen.colorDepth,
      dpr: window.devicePixelRatio || 1,
    },
    hardware_concurrency: navigator.hardwareConcurrency || 0,
    device_memory: (navigator as Navigator & { deviceMemory?: number }).deviceMemory || 0,
    max_touch_points: navigator.maxTouchPoints || 0,
    canvas_hash: '',
    webgl_vendor: '',
    webgl_renderer: '',
    audio_hash: 0,
    fonts: [],
    plugins: navigator.plugins ? navigator.plugins.length : 0,
    notification_permission: typeof Notification !== 'undefined' ? Notification.permission : '',
    permission_query: '',
  }

  try {
    s.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    // 忽略
  }
  for (const k of AUTOMATION_GLOBALS) {
    if (k in window) s.automation.push(k)
  }
  for (const k in document) {
    if (k.startsWith('$cdc_') || k.startsWith('$wdc_')) s.doc_props.push(k)
  }

  try {
    const c = document.createElement('canvas')
    c.width = 200
    c.height = 40
    const x = c.getContext('2d')
    if (x) {
      x.textBaseline = 'top'
      x.font = '14px Arial'
      x.fillText('menshen-fp', 2, 2)
      s.canvas_hash = c.toDataURL()
    }
  } catch {
    // 忽略
  }
  try {
    const g = document.createElement('canvas').getContext('webgl')
    const dbg = g?.getExtension('WEBGL_debug_renderer_info')
    if (g && dbg) {
      s.webgl_vendor = String(g.getParameter(dbg.UNMASKED_VENDOR_WEBGL) ?? '')
      s.webgl_renderer = String(g.getParameter(dbg.UNMASKED_RENDERER_WEBGL) ?? '')
    }
  } catch {
    // 忽略
  }
  try {
    const cv = document.createElement('canvas')
    const ctx = cv.getContext('2d')
    if (ctx) {
      const probe = 'mmmmmmmmmmlli'
      const base = 'monospace'
      ctx.font = '72px ' + base
      const bw = ctx.measureText(probe).width
      for (const f of FONT_PROBES) {
        ctx.font = '72px "' + f + '",' + base
        if (ctx.measureText(probe).width !== bw) s.fonts.push(f)
      }
    }
  } catch {
    // 忽略
  }

  // 通知权限查询是异步的；给一个很短的等待，拿不到就按空串继续。
  try {
    if (navigator.permissions?.query) {
      const p = await Promise.race([
        navigator.permissions.query({ name: 'notifications' as PermissionName }),
        new Promise<null>((resolve) => setTimeout(() => resolve(null), 300)),
      ])
      if (p) s.permission_query = p.state
    }
  } catch {
    // 忽略
  }

  s.audio_hash = await audioHash()
  const h = await sha256hex(s.canvas_hash)
  s.canvas_hash = h || 'raw'
  return s
}

/** audioHash 用离线音频上下文算一个浮点指纹；不支持时返回 0。 */
function audioHash(): Promise<number> {
  return new Promise((resolve) => {
    try {
      const AC =
        window.OfflineAudioContext ||
        (window as unknown as { webkitOfflineAudioContext?: typeof OfflineAudioContext })
          .webkitOfflineAudioContext
      if (!AC) {
        resolve(0)
        return
      }
      const ctx = new AC(1, 44100, 44100)
      const osc = ctx.createOscillator()
      osc.type = 'triangle'
      osc.frequency.value = 1000
      const comp = ctx.createDynamicsCompressor()
      osc.connect(comp)
      comp.connect(ctx.destination)
      osc.start(0)
      ctx.startRendering()
      ctx.oncomplete = (ev) => {
        const b = ev.renderedBuffer.getChannelData(0)
        let sum = 0
        for (let i = 0; i < b.length; i++) sum += Math.abs(b[i])
        resolve(Math.round(sum * 1000000) / 1000000)
      }
    } catch {
      resolve(0)
    }
  })
}
