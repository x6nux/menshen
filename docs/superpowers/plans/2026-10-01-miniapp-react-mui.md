# Mini App React + MUI 重构实施计划

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Mini App 从内联 vanilla HTML 单文件重写为 React 19 + MUI 7 工程，并借这次重写做信息架构与操作链路的整体重构——交互按国内 App（微信/支付宝小程序）的布局与习惯，操作更少步、更直观、更安全。

**Architecture:** 前端成为独立的 `web/` 工程（Vite + TS），构建产物输出到 `internal/panel/webdist/`（gitignore，不提交）并由 `go:embed` 嵌入 Go 二进制（Docker/CI 构建时带 `-tags miniapp`，本地无产物时编译占位 stub）；`/miniapp` 由 Go 直接托管（index.html + hash 资源 + SPA 回退）；`/miniapp/api/*` 协议保持不变，仅小幅补充 `todo` 计数、`logs.total`、群组批量更新（bulk_update）与上游连通性测试四个能力。旧页面在重构期间保留在 `/miniapp/classic` 供对照，验收后删除。

**Tech Stack:** React 19 · TypeScript 5 · Vite 7 · MUI 7（@mui/material + @emotion）· @tanstack/react-query 5 · Vitest + Testing Library · MSW（仅开发/测试 mock）· Go 1.25 `embed`。

**分支策略:** 全部工作在 `feat/miniapp-react` 分支完成；`main` 在合并前始终由旧页面服务，合并点=验收通过。

---

## Chunk 0：背景、原则与现状盘点

### 0.1 为什么大改

现有 Mini App 是 936 行内联 HTML/JS（`internal/panel/miniapp_html.go`），9 个顶部 tab 平铺，存在这些操作痛点：

| 现状问题 | 证据 |
|---|---|
| 设置项是 30+ 个裸输入框，`onchange` 即时保存，清空=恢复全局，没有"已覆盖/跟随全局"视图 | `viewBotDetail` / `viewSettings` 的 `S. sections` 循环 |
| 布尔设置（0/1）也用文本输入框改 | `settingSpecs` 中 min=0/max=1 的项 |
| 加群/加上游/加模型是页内展开表单，不是标准表单体验 | `ADDCHAT`/`UPADD`/`MDADD` 开关 |
| 危险操作全用 `confirm()`，文案与后果在括号里挤成一团 | `onclick="if(confirm(...` |
| 记录/申诉翻页是"上一页/下一页"按钮，无总数、无加载更多 | `pageLogs` / `pageAppeals` |
| 名单三处搜索框各自实现，新增表单堆在列表下方 | `filterRows` / `viewGbanOwn` / `viewWhite` |
| 无待办/首页工作台：未结申诉、演练群、停用 bot 都要自己翻 | `viewOverview` 只有 4 个数字 |
| 无批量操作：每个群单独进详情改 | `miniChat` 仅单条 action |

### 0.2 重构原则（"简便优先"）

1. **常用操作 ≤2 步**：高频动作（开关、改处罚、处理申诉）在列表或详情一步完成。
2. **默认收起，按需展开**：参数只显示偏离全局的覆盖项；设置按主题折叠。
3. **能自动的不要手填**：布尔→开关，数值→步进器/预设档位，模型→从已登记模型选择。
4. **危险操作分级确认**：不可逆操作用底部 ActionSheet，写明对象与后果；可撤销的给撤销而不是弹窗。
5. **每个写操作都有三件套**：提交中禁用、成功可见反馈、失败保留输入。
6. **协议不动、权限不动**：判定链路、鉴权（initData HMAC）、主/次管权限模型原样；只补三个小接口。
7. **不引入路由/状态框架**：自研 100 行导航栈 + TanStack Query，保持依赖最小。

### 0.3 非目标

- 不做多语言（仅中文）、不做 PWA/离线、不做 URL 深层分享（链接受 initData 绑定，不可分享）。
- 不改判定、计费、申诉状态机；不新增管理角色。
- 不为轮询模式提供网页（现状即如此：`public_url` 为空时无 HTTP 服务）。
- 首版不做下拉刷新（列为 P1.1），先提供列表自动刷新与按钮刷新。

---

## Chunk 1：技术方案与工程基建

### 1.1 仓库布局

```
web/                                   # 新增：前端工程
├── package.json / package-lock.json
├── vite.config.ts                     # base=/miniapp/，outDir=../internal/panel/webdist
├── tsconfig.json / tsconfig.app.json / tsconfig.node.json
├── eslint.config.js
├── index.html                         # <div id="root"> + viewport-fit=cover
├── src/
│   ├── main.tsx / App.tsx
│   ├── theme.ts                       # Telegram themeParams → MUI theme
│   ├── telegram.ts                    # SDK 加载、BackButton、themeChanged
│   ├── nav.tsx                        # 导航栈（tab + stack）
│   ├── api/
│   │   ├── client.ts                  # fetch 封装、错误分流
│   │   ├── types.ts                   # state/log/appeal/user 类型
│   │   ├── hooks.ts                   # query hooks
│   │   └── mutations.ts               # mutation hooks + 失效规则
│   ├── ui/                            # 通用组件（见 3.4）
│   ├── lib/
│   │   ├── format.ts                  # fmtTS/verdict/action/kind/apStatus/mute/punish
│   │   ├── settings.ts                # settingOf/gph/控件类型推断
│   │   └── filters.ts                 # 名单本地过滤
│   ├── mocks/                         # MSW handlers + fixtures（dev/测试）
│   └── pages/                         # 见 2.1 页面地图
internal/panel/
├── miniapp_embed.go                   # 新增：go:embed all:webdist + 托管
├── miniapp.go                         # 修改：Handler 分支 + todo/total/bulk
├── miniapp_html.go                    # 保留到验收（/miniapp/classic），之后删除
└── webdist/                           # 构建产物（gitignore；Docker/CI 构建，见 1.3/1.4）
```

### 1.2 技术决策（含备选）

| 决策 | 选择 | 理由 / 备选 |
|---|---|---|
| 构建产物 | **不提交产物；Docker/CI 构建**（已确认） | Go 侧 build tag 双实现：`-tags miniapp` 时 `go:embed all:webdist`；不带 tag 时用占位 stub——仓库干净，本地 `go build` / `go test` 无需 Node，部署路径由 Docker/CI 保证。本地真机调试先 `npm --prefix web run build`。 |
| 路由 | 自研导航栈（tab + page stack） | 页面只有两级+用户页，无需 react-router；与 Telegram BackButton 绑定简单。 |
| 数据层 | TanStack Query | 缓存失效、pending、乐观更新开箱即用，替代现在手写的 `S.LD` 缓存。 |
| 表单 | MUI + 底部 Drawer | 国内 App 主流；比页内展开表单好点按、键盘适配简单。 |
| 危险确认 | 底部 ActionSheet（Drawer anchor=bottom） | 替代 `confirm()`；红色确认 + 灰色取消。 |
| 主题 | Telegram `themeParams` 映射到 MUI palette，跟随 `colorScheme` | 与 TG 客户端深浅色一致；无 SDK 时回退国内风浅色。 |
| 打包 | 单一 entry，`manualChunks` 分 vendor | MUI + React 约 180–250KB gzip，Mini App 可接受。 |

### 1.3 构建管线

- `vite.config.ts` 关键配置：

```ts
export default defineConfig({
  base: '/miniapp/',
  plugins: [react()],
  build: {
    outDir: '../internal/panel/webdist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: { output: { manualChunks: { vendor: ['react', 'react-dom', '@mui/material', '@emotion/react', '@emotion/styled'] } } },
  },
  server: { proxy: { '/miniapp/api': 'http://127.0.0.1:8081' } },
});
```

- `web/package.json` scripts：`dev` / `build`（`tsc -b && vite build`）/ `lint` / `typecheck` / `test`（`vitest run`）/ `test:watch`。Node ≥ 22。
- Go 侧在 `miniapp_embed.go` 放 `//go:generate sh -c "cd ../../web && npm run build"`，方便本地刷新产物（产物目录已 gitignore）。
- **Dockerfile**：新增 web 构建阶段，Go 构建前覆盖产物并带 tag 编译：

```dockerfile
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build          # 输出 /src/../internal/panel/webdist = /internal/panel/webdist
# …build 阶段：
COPY . .
COPY --from=web /internal/panel/webdist ./internal/panel/webdist
# 带 tag 才嵌入前端；不带 tag 的构建只编译出占位 stub（见 1.4）
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags miniapp …
```

- **CI（.github/workflows/docker.yml check job）** 增加：`actions/setup-node@v4`（node 22，cache: npm，cache-dependency-path: web/package-lock.json）→ `npm ci --prefix web` → `npm --prefix web run typecheck && npm --prefix web run build`（Task 1 起追加 `lint && test`；Task 0 只有空工程，vitest 无测试文件会非零退出，不要提前加 test）→ 再跑 Go 检查：`gofmt`、`go vet ./...`、`go test -tags miniapp ./...`（此时 webdist 已构建，覆盖 embed 与托管测试）、`go build ./...`（不带 tag，验证占位路径可编译）。**没有产物新鲜度检查**（产物不入库）。
- `.gitignore` 增加 `web/node_modules/`、`web/.vite/`、`web/coverage/`、**`internal/panel/webdist/`**（产物不入库）。
- **`.dockerignore` 同步增加** `web/node_modules`、`web/.vite`、`web/coverage`：否则本地跑过 `npm i` 后，`COPY web/ ./` 会用宿主机（macOS）的 node_modules 覆盖容器内 `npm ci` 的 linux 依赖，rollup/esbuild 平台不符直接构建失败；`COPY . .` 也会白传几百 MB（`docker-compose.yml` 的 `build: .` 同样中招）。

### 1.4 Go 侧托管（`miniapp_embed.go`）

```go
// internal/panel/miniapp_embed.go（仅 -tags miniapp 编译）
//go:build miniapp

//go:embed all:webdist
var miniAppDist embed.FS

// internal/panel/miniapp_stub.go（默认编译，仓库无产物也能 build/test）
//go:build !miniapp
// miniAppDistFS 返回 (nil,false) → Handler 渲染「前端未构建」占位页（503），
// 页面文案直接给出构建方式：npm --prefix web run build 或 docker build。
```

- 用 `fs.Sub(miniAppDist, "webdist")` + `http.FileServerFS`（Go 1.22+）处理 assets；index 用 `fs.ReadFile` 读入内存（体积小）。
- 路由（MiniAppHandler 内）：`GET /miniapp` → index（no-store）；`GET /miniapp/classic` → 旧 miniAppHTML（验收前保留）；`GET /miniapp/assets/*` → 仅 `fs.Stat` 命中且为普通文件才 200（max-age=31536000, immutable；目录/缺失一律 404，禁止目录列举）；`GET /miniapp/<其他>` → SPA 回退 index；`/miniapp/api` 与 `/miniapp/api/*` → 非 POST 405、未知 op 404，绝不落入 SPA 回退。
- 保留 `Cache-Control: no-store` 于 index 与全部 API（鉴权数据）。
- 无 Telegram SDK 或 SDK 被墙时：`telegram.ts` 复刻现有 3 秒轮询逻辑，超时后渲染引导页（"请通过 Telegram 菜单按钮打开"）。

### 1.5 API 协议（保持不变的部分）

- 端点：`POST /miniapp/api/{op}`，JSON body；HTTP 头 `X-Tg-Init-Data`、`X-Bot-Id`（取 `?bot=`，缺省 `"0"`）。
- 错误：非 2xx + `{"error": "..."}`；401→身份失效/非 TG 打开，403→非管理员，400→参数问题。
- 现有 op 与前端页面对应（全部保留）：`state` `set` `bot` `chat` `upstream` `model` `admin` `gban` `gbanown` `whitelist` `digest` `user` `logs` `log` `logact` `appeals` `appeal` `appealact`。
- 前端错误分流：401 → 全屏引导/失效提示；403 → 无权限页；网络/5xx → 错误卡 + 重试；400 → toast 服务端 `error` 文案。

### 1.6 后端小改（本次允许的接口变更）

> 1-3 随 Task 2/3 落地（本文档的小改范围）；第 4 项属 Task 6（已确认进首版）。

1. **`state.todo`**（工作台待办）：`{"open_appeals": n, "dryrun_chats": n, "disabled_bots": n}`。
   - open_appeals：`COUNT(*) FROM appeals` + 权限 clause + `status IN (` + `store.AppealOpenStatusesSQL` + `)`（复用 `internal/store/db.go:505` 的常量，禁止手抄状态列表）。
   - dryrun_chats / disabled_bots：遍历 `BotsOwnedBy` + `ChatsOf`，在现有 `miniStats` 里顺带统计。
2. **列表 `total`（只有 `logs` 缺）**：`logs` 响应增加 `"total": n`（同 WHERE 的 `COUNT(*)`）。`appeals` 早已返回 `total`（`miniapp.go:1567`），`user` 也已返回 `total` + `shown`（当前筛选下的总数，`miniapp.go:1306-1322`）——前者直接复用，后者用于用户页 `hasMore`，不另造计数。
3. **`chat` op 新增 `bulk_update`**：`{"action":"bulk_update","bot_id":N,"chat_ids":[...],"fields":{...}}`。
   - **`bot_id` 必填且一次只处理一个 bot 的群**：先走与单条 update 相同的 `miniCanManageBot`（`miniapp.go:707-712` 的路径），杜绝主管理员跨 bot 误更新同 chat_id 的行；上限 100 个 chat_id，超出 400；返回 `{"note":"已更新 12 个群"}`。
   - `fields` 只认 `enabled/dryrun/group_alert/punish`，**未出现的键 = 不改**（不要用 `""` 当哨兵，`enabled:false` 是合法值）；先把单条 update 的字段收集逻辑（`miniapp.go:754-794`）抽成共享函数（命名如 `updateChatConf`）供 bulk 复用，避免逻辑分叉。

---

## Chunk 2：信息架构（国内 App 模式）

### 2.1 页面地图（5 个底部 Tab）

```
┌ 概览(工作台)    机器人          群组            记录            我的
│ 待办卡          列表→详情       列表(搜索/批量)→详情   分段[判定记录|申诉]   身份卡
│ 近24h 指标      启用/归属       启用/演练/群内展示     列表→详情           名单管理 →
│ 最近命中        模型/参数       处罚方式/实际执行      用户资料页          上游/模型/设置(主)
│ 机器人状态      →其群组(N)      补全入群时间                            关于/权限说明
└
```

| 旧视图 | 新位置 | 说明 |
|---|---|---|
| overview | Tab 概览 | 升级为工作台：待办 + 指标 + 最近命中 + 机器人状态 |
| bots / bot 详情 | Tab 机器人 | 参数只显示覆盖项 + 底部抽屉编辑 |
| chats / chat 详情 | Tab 群组 | 新增搜索、批量操作、按 bot 过滤 |
| logs / log 详情 / user | Tab 记录 → 分段「判定记录」 | 无限滚动 + 共 N 条 |
| appeals / appeal 详情 | Tab 记录 → 分段「申诉」（带未结角标） | 状态徽标 + 底部操作栏 |
| lists（专属/全局/管理员/白名单） | Tab 我的 → 名单管理（分段：白名单/资料放行/联封/管理员） | 统一搜索 + 底部「+」抽屉 |
| upstreams | Tab 我的 → 上游渠道（仅主） | 列表 + 抽屉表单 |
| models | Tab 我的 → 模型定价（仅主） | 搜索 + 抽屉表单 + 模型快选 |
| settings | Tab 我的 → 全局设置（仅主） | 分主题折叠 + 智能控件 + 摘要/修正文本 |

**次级管理员可见性**（与现状一致，前端只做可见性收敛，服务端仍是唯一裁决）：

| 入口 | 主管理员 | 次级管理员 |
|---|---|---|
| 概览 / 机器人 / 群组 / 记录（含申诉） | ✅ | ✅（仅自己名下 bot） |
| 我的 → 名单 → 联合封禁（专属组+全局组） | ✅ | ✅ |
| 我的 → 名单 → 白名单 / 资料放行 / 次级管理员 | ✅ | ❌（旧版即如此，`miniapp_html.go:501-502`） |
| 我的 → 上游渠道 / 模型定价 / 全局设置 | ✅ | ❌ |

（次管导出白名单是可行的服务端能力——`miniWhitelist` 对 `bot_id!=0` 走 `miniCanManageBot`、`bot_id=0` 仅主管理——但本次保持旧版可见性，作为后续可选增强，不在首版范围。）

### 2.2 导航模型

```ts
type TabKey = 'overview'|'bots'|'chats'|'records'|'mine';
type Page =
  | { k:'bot'; id:number } | { k:'chat'; botId:number; chatId:number }
  | { k:'log'; id:number } | { k:'user'; id:number } | { k:'appeal'; id:number }
  | { k:'lists'; section?:string } | { k:'upstreams' } | { k:'upstream'; id:number }
  | { k:'models' } | { k:'model'; name:string } | { k:'settings' };
type Nav = { tab: TabKey; stack: Page[] };
```

- `push(page)` / `pop()`；切 Tab 清空 stack。
- 二级页显示 AppBar 返回箭头（页内）与 Telegram `BackButton`（双绑，与现状一致）。
- 记录页的分段（判定/申诉）与筛选、群组页搜索词保留在页面内存状态，导航往返不丢。

---

## Chunk 3：设计规范（国内 App 布局）

### 3.1 Design tokens

| Token | 值（浅色回退，优先 Telegram themeParams） |
|---|---|
| 页面背景 | `bg_color` → `#F5F6F7` |
| 卡片 | `secondary_bg_color` → `#FFFFFF`，圆角 **12**，无边框无阴影，卡间距 12，内边距 16 |
| 主色 | `button_color` → `#1677FF` |
| 文字 | `text_color` → `#1A1A1A`；次要 `hint_color` → `#8A8A8E`；链接 `link_color` |
| 分割线 | `rgba(0,0,0,.08)`（深色 `rgba(255,255,255,.12)`），列表内缩进 16 |
| 成功/运行中 | `#07C160`；警告 `#FF9F0A`；危险 `#FA5151`（深色 `#FF6B6B`） |
| 字阶 | 导航标题 17/600 · 列表主文案 16 · 正文 15 · 次要 13 · 提示 12 · 徽标 11/600 |
| 间距 | 4 的倍数：4/8/12/16/20/24；页面左右 16 |
| 触控 | 最小 44×44；列表行高 ≥52 |
| 圆角 | 卡片 12 · 按钮/输入 10 · 抽屉顶 16 · 徽标 6 · Chips 胶囊 |
| 安全区 | `viewport-fit=cover`；TabBar 与底部操作栏加 `env(safe-area-inset-bottom)` |
| 深色 | 跟随 `colorScheme`；切换 `themeChanged` 事件实时重建主题 |

### 3.2 布局骨架

**一级页（列表/概览）**：居中标题 AppBar（44px，无阴影，背景=页面底色）→ 内容卡片区（16 左右边距）→ 底部 TabBar（56px + 安全区，选中主色、未选中 `#8A8A8E`，图标 24 + 文字 10）。

**二级页（详情）**：AppBar 左侧 `‹` + 居中标题 → 内容；底部固定操作栏（仅记录/申诉详情）承载主要动作。

**列表行**（通用 `ListRow`）：左主文案（16px）+ 可选副行（13px 灰：chat_id、bot 名、时间等）；右侧：徽标/值/开关 + `›` 箭头（灰）。整行可点，按下态 `rgba(0,0,0,.05)`。

**卡片分组**：组标题 13px 灰、左缩进 16，位于卡片上方 8px（如「近 24 小时」「判定与模型」）。

**搜索框**：卡片风格圆角输入（`#F2F2F2` 底、`Search` 图标、13-15px），支持即时本地过滤（名单）或服务端搜索（记录：300ms 防抖 + 回车）。

**分段控件**：`Segmented`（ToggleButtonGroup 样式或横向 chips），用于 记录[判定|申诉]、名单[白名单|资料放行|联封|管理员]。

**草稿示意（概览）**：

```
┌──────────────────────────────┐
│            门神              │   ← 居中标题
│  主管理员 · uid 12345        │   ← 副标题 13px 灰
├──────────────────────────────┤
│ 待办                         │
│  未结申诉 3              ›    │
│  演练中的群 2            ›    │
│  停用的机器人 1          ›    │
├──────────────────────────────┤
│ 近 24 小时                   │
│  送检 1,284   命中 12        │   ← 2×2 指标
│  开销 $0.42   生效群 8       │
├──────────────────────────────┤
│ 最近命中             全部 ›  │
│  #9812 广告 删除+禁言 2 分钟前│
├──────────────────────────────┤
│ 机器人                       │
│  门神小助手   ● 运行中    ›   │
└──────────────────────────────┘
[概览] [机器人] [群组] [记录] [我的]
```

### 3.3 交互模式（统一实现，禁止逐个页面自造）

| 场景 | 规格 |
|---|---|
| 开关类写操作 | 乐观更新（立即翻转）+ mutation；失败回滚 + toast；pending 时禁用 |
| 表单类写操作 | 底部 Drawer：标题 + 输入 + 全宽主按钮；提交中按钮 loading 且不可关；失败 toast、抽屉不关、输入保留；成功 toast（服务端 `note` 优先）+ 关闭 + 刷新 |
| 危险操作 | ActionSheet：标题（动作）+ 说明（对象与后果，沿用旧文案）+ 红色确认行 + 灰色取消行；不可逆操作不乐观更新 |
| 列表加载 | 首屏骨架 3 行；无限加载用 `useInfiniteQuery`（页 20），底部「加载中…/没有更多了」；错误卡 + 重试 |
| 空态 | 图标 + 一句文案 +（可选）主按钮（如「去接入机器人」→ 说明只能私聊 /start） |
| 轻提示 | 全局单例 Toast：屏幕中下、黑 75% 圆角 8、2.2s；成功与失败都提示 |
| 错误分流 | 401 全屏引导 · 403 无权限页 · 4xx toast 服务端文案 · 5xx/网络 错误卡+重试 |
| 返回 | 二级页 `‹` 与 Telegram BackButton 同步；顶层隐藏 BackButton |
| 键盘 | 输入行 `inputMode`（chat_id/uid/价格用数字键盘）；Drawer 内规避底部安全区 |

### 3.4 MUI 组件映射

| 需求 | 组件 |
|---|---|
| 导航栏 | `AppBar` + `Toolbar` + `IconButton(ArrowBackIosNew)` |
| 底部 Tab | `BottomNavigation` + `BottomNavigationAction` |
| 卡片/分组 | `Card` + `CardContent` + `ListSubheader` 或自绘 `SectionCard` |
| 列表行 | `List` / `ListItemButton` / `ListItemText` / `ListItemSecondaryAction` + `KeyboardArrowRight` |
| 开关 | `Switch`（size=small） |
| 输入 | `TextField`（size=small, `inputMode`）/ `Select` / `Autocomplete`（bot/模型选择） |
| 按钮 | `Button` contained/outlined/text；危险 `color="error"`；全宽底按钮 `fullWidth` |
| 筛选 | `Chip`（fn 可点） |
| 分段 | `ToggleButtonGroup` 或 `Tabs scrollable` |
| 抽屉表单/操作面板 | `Drawer anchor="bottom"`（`ActionSheet`/`FormDrawer` 两个封装） |
| 提示 | `Snackbar`（全局 Toast 封装） |
| 骨架 | `Skeleton` |
| 折叠 | `Collapse` + `ExpandMore`（设置分组） |
| 徽标 | 自绘 `Badge` 语义色（`ok`/`no`/`warn`） |
| 图标 | `@mui/icons-material` 按需引入 |

### 3.5 设置项智能控件（核心简化）

后端 `state.specs/sections` 已含 `key/label/hint/min/max/group`，前端推断控件：

```ts
// 注意：state.specs/sections 只由 settingSpecs 构造；三个总开关、时区、附加链接、
// 模型列表等「特殊卡」不在 specs 里，由页面单独渲染，不要在此推断。
function controlKind(sp: Spec): 'toggle'|'number'|'select' {
  if (sp.min === 0 && sp.max === 1) return 'toggle';       // 布尔
  if (sp.max && sp.max > 1) return 'number';               // 有界整数
  return 'number';                                          // 无界整数（0=关）
}
```

- **时长类**（`*_minutes`）：预设档位 Chips + 自定义数字，**档位必须按 `spec.min/max` 过滤**——`antiad_mute_minutes` 无上限才有「永久(0)/7 天」，而 `antiad_hedge_minutes`、`antiad_upstream_alert_minutes`、`antiad_alert_every` 是 `min=1, max=1440`，超界档位提交必 400。展示"= 1 天"的换算文案；加越界档位过滤单测。
- **毫秒类**（`*_ms`）：步进 100ms，展示推荐区间。
- **0/1 开关类**：Switch。
- **bot 覆盖视图**：键集合 = `specs.filter(sp => sp.group==='antiad' || sp.group==='both')` 且值存在于 `bot_settings[botID]`，两者取交集——`bot_settings` 里还有 `antiad_exempt_users`（JSON 数组）与 `antiad_alert_last_id`（内部游标）这类**不在 specs 的键，一律不渲染**（走 `set` 必然 400）。每行显示「已覆盖 · 生效值」，按钮恢复全局（写空串）；「＋ 添加参数覆盖」抽屉只从上述过滤后的 specs 里选。模型覆盖不在参数列表里，留在「模型卡」走 `bot action=models`。
- **全局设置视图**：6 个主题折叠卡（处置与分档/判定与模型/进群冷判定/通知与展示/护栏与成本/学习与名单），卡头显示「已设置 N 项」摘要；逐项编辑走行内控件或抽屉，不再有裸输入框。

---

## Chunk 4：逐页功能与操作规格

### 4.1 概览（工作台）

- 标题「门神」+ 身份副标题（角色 · uid）。
- 待办卡（`state.todo`）：未结申诉 → 记录/申诉分段；演练中的群 → 群组页并预置「演练」筛选；停用的机器人 → 机器人页。
- 近 24h 指标卡 2×2：送检 / 命中 / 折算开销 / 生效群。
- 最近命中：前端调 `logs {page:1, verdict:"ad"}` 取前 3 条 → 点入详情；「全部 ›」→ 记录页。
- 机器人卡：每行 label + 运行中/未运行/已停用徽标 → 机器人详情。

### 4.2 机器人

- 列表：搜索（本地，label/username/bot_id）；行右侧状态徽标；空态文案说明接入需私聊主 bot。
- 详情：
  - 启用 Switch（乐观）。
  - 标识行：username/bot_id/归属；主管理员「改派」→ 底部抽屉（`owner_opts`）。
  - **`is_main` 例外**：显示「主 bot」徽标；归属只读；不显示改派与「移除该 bot」入口（服务端也会 400，`miniapp.go:636-660`）。
  - 模型卡（主）：判定/复判模型以 Chips 展示；编辑抽屉内为多行输入 + 已登记模型快选（来自 `state.models`），保存走 `bot action=models`。次管显示只读说明。
  - 参数卡：仅显示覆盖项 + 「添加参数覆盖」；行内按 3.5 智能控件编辑，单行保存（`set {scope:"bot"}`）；恢复全局=清空。
  - 群组入口：「管理其群组（N）」→ 群组页预置该 bot 过滤。
  - 危险区：移除该 bot（ActionSheet，文案沿用现有后果说明）。

### 4.3 群组

- 列表：搜索（title/chat_id/bot）；行：标题 + `chat_id · bot label` 副行 + 状态徽标（判定中/演练/停用）+ `›`。
- 右上「管理」进入批量模式（勾选行）→ 底部操作条：启用/停用/开启演练/关闭演练/处罚方式（底部抽屉选择）→ `chat {action:"bulk_update"}`；成功后退出批量并刷新；部分失败要能定位（后端逐条权限校验，返回成功数；前端如失败给出错误文案）。
- 右上「＋」：添加群抽屉（bot 选择 + chat_id 数字输入 + 「默认演练」说明）。
- 详情：
  - 状态卡：启用 / 演练 / 群内展示 三个 Switch。
  - 处罚卡：处罚方式 Select（跟随 bot / 当前禁言档 / 封禁出群）+「实际执行」结果行 + 防错提示（沿用「要改成永久禁言…」）。
  - 工具卡：补全历史入群时间（按钮 + 说明 + 成功后 toast 服务端 note）。
  - 危险区：移除该群。
- 群组页进入时若带 bot 过滤，顶部显示可清除的过滤条。

### 4.4 记录（判定记录）

- 顶部搜索框（防抖 300ms，回车立即；服务端 `q` 搜原文/理由/uid/群号）。
- 筛选 Chips：已删除（默认）/ 全部 / 命中 / 正常 / 跳过。
- 列表无限滚动，行内容：`#id · 时间`、verdict 徽标、处置徽标、uid 链接、群号、置信度、开销、原文摘要 50 字；点击进详情。
- 详情：信息卡（时间/群+标题/用户链接/判定/处置/开销/理由）+ 原文卡（`view_url` 按钮）+ 底部固定操作栏：
  - 主操作：AI 复查 / 解封（判定维持）/ 加白 24h / 人工标记广告。
  - 主管理员追加：联合封禁 / 解除联合封禁（危险色，ActionSheet 确认）。
  - 操作后：失效 `['log',id]`、`['logs']`、`['user']`、`['state']`；就地更新详情与列表。
- 用户页：资料卡（用户名/昵称/简介/发言/群组/判定统计）+ 分段（只看被处置过的 默认 / 全部记录）+ 无限滚动；从记录行 uid 与详情用户链接进入。

### 4.5 申诉

- 记录页分段「申诉」，标签带未结数角标（`state.todo.open_appeals`）。
- 筛选 Chips：未结（默认）/ 全部；列表行：`#id · uid`、状态徽标（`apStatus`）、AI 结论摘要；无限滚动。
- 详情：**数据源一律 `POST appeal {id}`，不用列表行渲染**——列表把 `statement`/`ai_reason` 截到 300 字且不含解禁码/兑换/网页验证记录（`miniapp.go:1556-1558`、`miniAppealDetail`）。信息卡（申诉人/bot、提交/更新时间、完整申诉理由、AI 复核结论+模型+置信度、网页验证次数与记录、解禁码与到期、兑换记录、详情页链接）+ 底部操作栏按状态渲染：人工解除 / 驳回 / 签发解禁码 / 重跑 AI 复核；危险项 ActionSheet 确认；操作后失效 `['appeal', id]`、`['appeals']` 与 `['state']`。

### 4.6 我的

- 身份卡：角色（主/次级管理员）、uid、权限摘要（一行为一句）。
- 功能列表：名单管理（全员）；上游渠道、模型定价、全局设置（仅主）。
- 页脚：说明「本页仅管理员可见；接入新 bot 请在私聊面板操作」。

### 4.7 名单管理（可见性按 2.1 矩阵；次管只到联封）

主管理员分段：白名单 / 资料放行 / 联合封禁 / 次级管理员；次级管理员只显示「联合封禁」一个分段。
- **白名单**（主）：搜索（uid/来源/群号，本地即时）；行：uid · 范围（全平台/bot 所有群/群 X）· 来源 · 到期；移除按钮；「＋」抽屉：bot 选择 + user_id + chat_id(0=所有群) + 小时(空=永久)。卡片底部保留**「默认豁免（内置，无需配置）」只读卡**（主管理员、各 bot 归属人、群主/管理员实时查询、匿名管理员、有管理权限的 bot、工具 bot 的判定规则；数据来自 `state.me` 与 `bots[].owner_id`，不需要新接口）。
- **资料放行**（主）：搜索（uid/bot/原因）；行：uid · bot · 小时 · 到期；撤销按钮。
- **联合封禁**：二级分段「全局组 / 我的专属组」；全局组搜索 + 添加(uid+原因) + 解除；专属组含启用 Switch、生效群多选（名下 bot 的群，Switch 列表）、名单搜索/添加/移除。
- **次级管理员**（主）：列表 + 添加(uid+备注) + 移除。
- 「＋」为全局悬浮按钮，按当前分段切换表单内容；搜索框共 4 处（白名单/资料放行/全局组/专属组），迁移测试要全覆盖。

### 4.8 上游渠道（主）

- 列表：名称 + 启用徽标 + base_url 副行；「＋」抽屉：名称 / base_url / api_key / 能力开关（chat、systemone）/ 启用。
- 详情：编辑抽屉（api_key 留空=不改，显示当前掩码）、启停 Switch、改名、危险区删除。
- 「测试连通」按钮（已确认进首版，见 Task 6）：新增后端 `upstream {action:"test"}`，用最短 chat 请求验证 base_url/api_key，展示延迟或可操作错误。

### 4.9 模型定价（主）

- 列表：搜索（名称）；行：等宽模型名 + 上游/启用徽标 + 单价摘要。
- 「＋」抽屉：上游选择（仅启用中）+ 模型 ID + 四个价格（数字键盘）；成功后提示去「全局设置→默认模型」引用。
- 详情：价格编辑（四格）、启停、删除（ActionSheet）。

### 4.10 全局设置（主）

- 总开关卡：反广告总开关 / 告警抄送 / 联合封禁（Switch，乐观）。
- 默认模型卡：判定(systemone)/复判/识图，抽屉编辑（模型快选 Chips + 手输，按重试顺序）。
- 6 个主题折叠卡（见 3.5），卡头摘要「已设置 N 项」，行内智能控件。
- 展示时区卡：抽屉选择（常用时区列表 + 自定义 IANA）。
- 群内提示附加链接卡：抽屉文本编辑。
- 形态摘要卡：摘要只读展示 + 「编辑」抽屉 + 「立即重新总结」按钮；修正文本同样抽屉编辑。

---

## Chunk 5：测试与验收

### 5.1 前端测试（Vitest + RTL，重点：从旧 HTML 断言迁移过来的不变量）

| 旧 Go 测试 | 新前端测试 | 断言 |
|---|---|---|
| `TestMiniAppGlobalTogglesValidJS` | `SettingsPage.test.tsx` | 三个开关调用 `set` 时 `value` 是字符串 `'1'/'0'`（mock API 捕获参数），且成功后刷新 |
| `TestMiniAppChatDetailShowsEffectivePunish` | `ChatDetailPage.test.tsx` | 渲染「实际执行」「跟随 bot 设置」；切换处罚方式后展示对应「禁言 N 天/永久禁言/封禁出群」 |
| `TestMiniAppListSearch` | `ListSearch.test.tsx` | 四处名单搜索框（白名单/资料放行/全局组/专属组）按 uid/原因/来源过滤，输入时焦点不丢（受控输入） |
| `TestMiniAppUserViewWired` | `UserPage.test.tsx` | 默认过滤「只看被处置过的」；记录行 uid 可点进入用户页；`hasMore` 用 `shown` 判断 |

另加：`lib/format.test.ts`（fmtTS 时区、muteText、punishLabel、settingOf 覆盖优先级）、`lib/settings.test.ts`（`controlKind` 推断、时长档位按 `min/max` 过滤、覆盖键集合过滤掉非 spec 键）、`api/client.test.ts`（头、401/403/400 分流）、`nav.test.tsx`（栈 push/pop、切 tab 清栈）、`BulkChats.test.tsx`（批量选择与提交参数，`bot_id` 必带）、`AppealDetail.test.tsx`（>300 字的理由完整渲染、解禁码/兑换/网页验证记录可见）。

### 5.2 Go 测试改动

- 删除 5.1 表格中 4 个 `miniAppHTML` 字符串断言测试（不变量已由前端测试覆盖）。
- 新增：
  - `TestMiniAppServesEmbeddedApp`：在 `go test -tags miniapp ./...`（CI 里 webdist 已构建）下执行：GET `/miniapp` 200 且含 `id="root"`、`Cache-Control: no-store`；GET `/miniapp/assets/<真实文件>` 200 且 `immutable`；GET `/miniapp/assets/`（目录）404、`GET /miniapp/api` 非 POST 405；GET `/miniapp/xxx` 回退 index。untagged 时另断言 stub 占位页（503）。
  - `TestMiniAppTodoCounts`：构造未结申诉/演练群/停用 bot，断言 `state.todo`；并覆盖「申诉状态流转（未结→已结）后计数减少」（用 `store.AppealOpenStatusesSQL` 的口径）。
  - `TestMiniAppLogsTotal`：`logs` 返回 `total` 且与筛选条件一致；`appeals.total` 的既有行为补一条回归断言。
  - `TestMiniAppChatBulkUpdate`：`bot_id` 必填（缺失 400）；次管碰不到别人的群（403）；主管理员跨 bot 的同 chat_id 不会被误更新；上限 100；`fields` 缺省键不变；结果落库。
- 保留全部其余 API 测试（协议未变）。

### 5.3 手工 E2E 清单（Telegram 真机，验收必做）

- [ ] 通过 bot 菜单按钮打开 `/miniapp`，主题跟随客户端深浅色切换实时变化。
- [ ] 概览待办数字与列表一致；最近命中可进详情。
- [ ] 机器人：启用开关、改派、模型编辑、加/删参数覆盖、移除（ActionSheet）。
- [ ] 群组：添加、搜索、批量启停/演练、处罚方式与「实际执行」一致、补全入群时间、移除。
- [ ] 记录：搜索/筛选/无限加载、详情四操作、联封两项（主）、用户页资料与分页。
- [ ] 申诉：未结角标、详情四动作（按状态出现）、操作后状态刷新。
- [ ] 名单：四个分段搜索/添加/移除、专属组生效群勾选。
- [ ] 上游/模型/设置：全部增删改、设置智能控件、摘要保存与重新总结、时区。
- [ ] 次级管理员账号：可见性与操作范围与旧版一致。
- [ ] 弱网/失败注入：断网提示可重试，写失败输入不丢。
- [ ] Telegram BackButton 与页内返回行为一致；↔ 交替使用不迷路。
- [ ] iOS 与 Android 各一台，底部安全区与键盘遮挡正常。

---

## Chunk 6：任务分解（TDD、频繁提交）

> 顺序执行；每个任务结束跑一次对应门禁并提交。分支 `feat/miniapp-react`。

### Task 0：脚手架与构建管线

**Files:** Create `web/**`、`internal/panel/miniapp_embed.go`、`internal/panel/miniapp_stub.go`；Modify `internal/panel/miniapp.go`、`.gitignore`、`.dockerignore`、`Dockerfile`、`.github/workflows/docker.yml`、`docs/development.md`

- [ ] `npm create vite@latest web -- --template react-ts`，`npm i @mui/material @emotion/react @emotion/styled @mui/icons-material @tanstack/react-query`；devDeps：`vitest jsdom @testing-library/react @testing-library/user-event @testing-library/jest-dom msw eslint typescript-eslint`。
- [ ] 按 1.3 配 `vite.config.ts`（base/outDir/proxy/manualChunks）与 scripts；`index.html` 加 `viewport-fit=cover`。
- [ ] 写最小 `App.tsx`（仅 `<h1>门神</h1>` + CssBaseline），`npm run build` 产出 `internal/panel/webdist/{index.html,assets/*}`。
- [ ] 新建 `miniapp_embed.go`（`-tags miniapp`，go:embed）+ `miniapp_stub.go`（默认编译，fs 缺失时渲染「前端未构建」占位页）+ `MiniAppHandler` 接入（含 1.4 全部路由边界）；旧页面移到 `/miniapp/classic`。
- [ ] Go 测试：`TestMiniAppServesEmbeddedApp`（先红后绿，`-tags miniapp` 下跑；无 tag 时断言占位页 503）；`gofmt -l . && go vet ./... && go test ./...`。
- [ ] `.gitignore`、`.dockerignore`、Dockerfile、CI 按 1.3 修改（CI 此刻只加 `typecheck + build` 与 `go test -tags miniapp ./...`，前端 lint/test 留到 Task 1）；提交 `web/`（**不提交 webdist，已 gitignore**）。
- [ ] 门禁：`npm --prefix web run typecheck && npm --prefix web run build`；`gofmt -l . && go vet ./... && go test ./... && go test -tags miniapp ./internal/panel/`；`docker build .` 能从零构建成功（验证 .dockerignore 与 Dockerfile tag 生效）。
- [ ] 本地验证：删掉 `internal/panel/webdist/` 后 `go build -o /tmp/menshen .` 仍成功（stub 路径）；`npm --prefix web run build` 后再 `go build -tags miniapp` 成功（embed 路径）。

### Task 1：外壳与设计系统

**Files:** Create `web/src/theme.ts`、`telegram.ts`、`nav.tsx`、`api/client.ts`、`api/types.ts`、`api/hooks.ts`、`api/mutations.ts`、`ui/*`、`lib/format.ts`、`lib/settings.ts`、`lib/filters.ts`、`mocks/*`；Test `web/src/**/*.test.ts(x)`；Modify `.github/workflows/docker.yml`（追加前端 lint/test）

- [ ] `lib/format.ts` 从旧 HTML 的 JS 逐函数迁移（fmtTS 用 `Intl` + `tz_name`，其余行为等价），先写单测（含 `0=永久禁言`、`>1440 分钟按小时`、覆盖优先级）。
- [ ] `theme.ts`：themeParams → palette 映射 + 回退色板 + 全局组件 overrides（3.1/3.4）；写映射单测。
- [ ] `telegram.ts`：SDK 轮询等待（≤3s）、`ready/expand`、BackButton 桥、themeChanged 订阅、非 TG 引导。
- [ ] `nav.tsx`：NavContext（tab+stack）+ `push/pop/switchTab` + BackButton 同步；单测覆盖切 tab 清栈。
- [ ] `api/client.ts`：头注入、`{error}` 解包、401/403/4xx/5xx 分流（`ApiError{status}`）；单测用 msw。
- [ ] `ui/*`：`TopBar/BackBar`、`TabBar`、`SectionCard`、`ListRow`、`SettingRow`、`SwitchRow`、`SearchField`、`Segmented`、`ActionSheet`、`FormDrawer`、`Toast`、`EmptyState`、`ErrorState`、`Skeletons`、`Badge`。
- [ ] `App.tsx`：ThemeProvider + CssBaseline + QueryClientProvider + ToastProvider + 骨架（loading/401/403 全屏态）。
- [ ] 门禁：`npm --prefix web run lint && npm --prefix web run typecheck && npm --prefix web run test && npm --prefix web run build`；CI 在 Task 0 的 `typecheck + build` 后追加 `lint + test`（改 `.github/workflows/docker.yml` 并验证 CI 通过）。

### Task 2：概览 + 机器人 + 群组（高频页）

**Files:** Create `pages/OverviewPage.tsx`、`BotsPage.tsx`、`BotDetailPage.tsx`、`ChatsPage.tsx`、`ChatDetailPage.tsx` + 测试；Modify `internal/panel/miniapp.go`（todo/bulk）

- [ ] 后端 `state.todo`（1.6.1）与 `chat bulk_update`（1.6.3）+ Go 测试（`TestMiniAppTodoCounts`、`TestMiniAppChatBulkUpdate`）。
- [ ] 概览页（4.1）+ 测试（待办跳转、最近命中渲染）。
- [ ] 机器人列表/详情（4.2）+ 测试（过滤、覆盖项视图、模型抽屉、移除确认）。
- [ ] 群组列表/详情/批量/添加（4.3）+ 测试（批量参数、处罚实际执行、补全动作）。
- [ ] `npm --prefix web` 门禁（lint/typecheck/test/build）+ `go test ./internal/panel/`；真机走查 4.1–4.3。

### Task 3：记录 + 申诉 + 用户页

**Files:** Create `pages/RecordsPage.tsx`、`LogDetailPage.tsx`、`UserPage.tsx`、`AppealsPage.tsx`、`AppealDetailPage.tsx` + 测试；Modify `internal/panel/miniapp.go`（total）

- [ ] 后端 `logs` 加 `total`（1.6.2，appeals/user 已有）+ Go 测试（`TestMiniAppLogsTotal`）。
- [ ] 记录页：搜索/筛选/无限滚动/详情操作栏/用户页（4.4）+ 5.1 迁移测试。
- [ ] 申诉分段与详情（4.5）+ 测试（状态→操作可见性、操作后刷新）。
- [ ] `npm --prefix web` 门禁 + `go test ./internal/panel/`；真机走查 4.4–4.5。

### Task 4：我的 + 名单 + 上游 + 模型 + 设置

**Files:** Create `pages/MinePage.tsx`、`ListsPage.tsx`、`UpstreamsPage.tsx`、`ModelsPage.tsx`、`SettingsPage.tsx` + 测试

- [ ] 我的页与可见性（4.6，次管隐藏主管理员入口）+ 测试。
- [ ] 名单四分段（4.7）+ 5.1 搜索迁移测试。
- [ ] 上游/模型（4.8/4.9）+ 测试。
- [ ] 设置：智能控件、覆盖视图、折叠分组、摘要/修正文本、时区（3.5/4.10）+ `SettingsPage` 迁移测试（开关传字符串值）。
- [ ] `npm --prefix web` 门禁；真机走查 4.6–4.10。

### Task 5：收尾与切换

**Files:** Delete `internal/panel/miniapp_html.go` 与 `/miniapp/classic` 分支；Modify `docs/panel.md`、`docs/development.md`、`README.md`（如提及） 、`internal/panel/miniapp_test.go`、`internal/panel/miniapp_embed.go`

- [ ] 删除 4 个 HTML 字符串断言 Go 测试（5.1 已覆盖）；删除旧文件与 classic 路由。
- [ ] 全量门禁：`gofmt -l .`、`go vet ./...`、`go test ./...`、`go test -tags miniapp ./...`、（web）`lint/typecheck/test/build`。
- [ ] 文档：`docs/panel.md` 的 Mini App 段落改写（描述新 IA 与入口）；`docs/development.md` 增加 `web/` 开发与构建说明（Node 版本、npm scripts、**产物不提交**、`-tags miniapp` 与占位 stub、`docker build` 路径）；`docs/deployment.md` 的 Mini App 段落同步（`docs/deployment.md:127-133`）。
- [ ] 5.3 E2E 清单全过；iOS/Android 各一台；次管账号复核。
- [ ] 合并 `feat/miniapp-react` → `main`；更新 `main.go` 版本号（如适用）；`bd sync` + `git push`（按 AGENTS.md 收尾流程）。
- [ ] 建后续 issue：下拉刷新、模型发现。

### Task 6（已确认进首版）：上游连通性测试

**Files:** Modify `internal/panel/miniapp.go`（`upstream` op 加 `test`）、`pages/UpstreamsPage.tsx`；Test `internal/panel/miniapp_test.go`、`UpstreamsPage.test.tsx`

- [ ] 后端 `upstream {action:"test", id}`：用该上游已登记的任一模型发一次最小请求，返回 `{ok, latency_ms, error}`；三条路径单测（无模型/失败/成功）。
- [ ] 上游详情页「测试连通」按钮：loading 态 → 结果行内展示（延迟/错误），失败给可操作文案。

---

## Chunk 7：风险与决策点

### 7.1 风险与对策

| 风险 | 对策 |
|---|---|
| 忘带 `-tags miniapp` 构建导致部署出占位页 | Dockerfile 与 CI 固定带 tag；占位页文案直接给出构建方式；docs/development.md 写清 |
| MUI 体积拖慢首屏 | vendor 分包 + gzip ~200KB；首屏骨架；必要时后续引入 `@mui/material-nextjs`?（不适用）或改 Pigment CSS（暂不） |
| `telegram.org/js` 被墙导致白屏 | 复刻现有 3 秒轮询 + 引导页，不依赖 SDK 渲染 UI |
| Drawer 表单与 TG 键盘/安全区冲突 | 底部抽屉 `pb: env(safe-area-inset-bottom)`；输入 `autoFocus` 谨慎；真机走查（5.3） |
| 无限滚动需要 `hasMore` | 用响应计数 + 已加载数量判断：`logs`/`appeals` 用 `total`，用户页用 `shown`（当前筛选下的总数；用 `total` 会因筛选偏大而多翻页）；页大小 20 已知 |
| 设置智能控件推断错误（无界整数 vs 文本） | 规则单测 + `settingSpecs` 已有 min/max 元数据兜底；未知键回退文本输入 |
| 次管可见性回退 | E2E 用次管账号逐页复核（5.3），服务端权限不动 |

### 7.2 决策记录（2026-10-01 已确认）

1. **构建产物**：不提交，仅 Docker/CI 构建；Go 侧 `-tags miniapp` 嵌入 + 默认 stub（见 1.3/1.4）。
2. **信息架构**：5 Tab（概览/机器人/群组/记录/我的）；申诉并入记录分段；上游/模型/设置/名单收进「我的」。
3. **主题**：跟随 Telegram 主题（themeParams → MUI，深浅色实时），无 SDK 时回退国内风浅色。
4. **上游连通性测试**：进首版（Task 6）。

### 7.3 完成定义（DoD）

- [ ] `/miniapp` 由 React+MUI 应用服务（部署构建固定 `-tags miniapp`），旧 HTML 已删除；仓库无产物，`main` 上 `go build`/`go test`（含 untagged stub 路径）无需 Node。
- [ ] 5.3 E2E 清单全部通过（含次管账号、iOS/Android）。
- [ ] 4 个迁移测试 + 新增前后端测试全绿；CI（gofmt/vet + `go test -tags miniapp` + untagged `go build` + 前端 lint/typecheck/test/build）通过。
- [ ] `docs/panel.md`、`docs/development.md` 与新实现一致；旧功能 parity 无缺失（2.1 映射表全勾）。
