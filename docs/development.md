# 开发与发布

## 构建

```bash
CGO_ENABLED=0 go build -o menshen .   # 无 CGO，纯 Go（SQLite 走 modernc.org/sqlite）
./menshen -version                    # buildTime 只能由 ldflags 注入
```

## 检查

```bash
go test ./...      # 全量
gofmt -l .         # 必须为空
go vet ./...
```

这三项在 CI 上是打包前的门槛（`.github/workflows/docker.yml`）。

## 前端（web/：Mini App + 桌面端管理面板）

`web/` 是一个 React + MUI 工程，产出两个互相独立的界面，由 Go 分别托管：
**Telegram Mini App**（`/miniapp`，`vite.config.ts`）与**桌面端管理面板**
（`/admin`，`vite.admin.config.ts`）。两者只共用数据层（`web/src/api`、`web/src/lib`）
与通用组件（`web/src/ui`）；页面与外壳各写一份。需要 Node ≥ 22.12（Vite 8 的下限）：

```bash
npm --prefix web install          # 安装依赖
npm --prefix web run dev          # Mini App 开发服务器；/miniapp/api 代理到 127.0.0.1:8081
VITE_MOCK=1 npm --prefix web run dev   # 不连后端：MSW 在浏览器里 mock /miniapp/api
npm --prefix web run dev:admin    # 管理面板开发服务器（http://localhost:5173/admin/）
npm --prefix web run typecheck    # tsc -b
npm --prefix web run test         # vitest run
npm --prefix web run lint         # oxlint
npm --prefix web run build        # tsc -b && 两次 vite build，产物都输出到 internal/panel/webdist/
```

（`VITE_MOCK=1` 只 mock 接口；页面仍要能读到 `window.Telegram.WebApp` 才过得了
打开检查，通常直接在 Telegram 客户端里开，或在 DevTools 里注入一个假 SDK。
管理面板不吃 SDK，`dev:admin` 直接开 `/admin/`，会话过期时页面给重新获取
登录链接的指引。）

- **两个入口、两次构建**：`npm run build` 先用 `vite.config.ts`（base `/miniapp/`）
  构建 `index.html`（Mini App）与 `public.html`（公开页），再用 `vite.admin.config.ts`
  （base `/admin/`）构建 `admin.html`（管理面板）。两次产物都落在
  `internal/panel/webdist/`，所以后者 `emptyOutDir: false` —— 顺序不能反，否则
  第二次构建会把第一次的 `index.html` 清掉。发现目录共用一个 `assets/`，
  但引用前缀不同（`/miniapp/assets/*` vs `/admin/assets/*`），互不影响。
- **Telegram SDK 自托管**：`web/public/telegram-web-app.js`（2026-10-02 取自
  telegram.org 的官方脚本，sha256 `3549138a7934039fe7dfd1291a4ee739bd2b705a614308053a8b08a87d85c451`）
  随产物发布到 `/miniapp/telegram-web-app.js`，`index.html` 用根绝对路径
  `/telegram-web-app.js` 引用（public 资源交给 Vite 按 base 重写）。不要改回 CDN
  外链——外链一旦拿不到，Mini App 就停在引导页。升级 SDK：替换该文件、更新此处的
  sha256，并重跑前端测试与 Go 托管测试。管理面板不加载它。
- **构建产物不入库、默认嵌入**（`internal/panel/webdist/` 只提交一个 `.gitkeep` 占位；
  其余内容 gitignore）。Go 侧 `go:embed all:webdist` **默认编译**，不需要任何 build tag：
  - 跑过 `npm --prefix web run build` 后，`go build` / `go test` 直接嵌入真实前端；
  - 没跑过（全新克隆）时目录里只有占位文件，编译与测试照常工作，`/miniapp`
    与管理面板 `/admin` 分别返回各自的「前端未构建」提示（503）。
- 托管路由（`internal/panel/miniapp.go` 与 `admin_panel.go`）：`/miniapp/assets/*`
  与 `/admin/assets/*` 长期强缓存；产物根下的文件（自托管 SDK 等）短缓存
  `max-age=3600`；其余未匹配路径回退各自入口页，`/miniapp/api` 与 `/admin/api`
  绝不回退。
- 本地要看真实页面：先 `npm --prefix web run build`，再 `go build`（无需 tag）。
- `docker build` 会自动构建前端并嵌入（Dockerfile 的 web 阶段），部署无需手工构建前端。
- CI 顺序：先 `npm ci / lint / test / typecheck / build`，再 `go vet`、`go test ./...`
  （此时 webdist 已构建）；另跑一次 `go build ./...` 验证无产物的全新克隆也能编译。

## 发布镜像

1. 递增 `main.go` 里的 `version`，提交
2. 打同名 tag 并推送：`git tag v2.3.0 && git push origin v2.3.0`

GitHub Actions 会先跑上面三项检查，然后 amd64 与 arm64 各用一台原生 runner
分别构建（不走 QEMU 模拟），再把两个架构的镜像按 digest 合并成一个多架构
manifest，打到 `ghcr.io/x6nux/menshen`，标签为 `2.3.0`、`2.3`、`latest`。
预发布（`v2.3.0-rc1`）只出 `2.3.0-rc1`，不动 `latest`。

tag 与 `version` 不一致时直接失败。推 main 与 PR 只检查、构建，不发布。

## 数据

| 表 | 内容 |
|---|---|
| `antiad_log` | 判定账本：置信度、处置、开销。**只记判过的** |
| `group_messages` | 群消息全量留底，供 `/check` 复查。被护栏拦下的、豁免者发的都在这里 |
| `group_members` | 群成员画像，「新人 / 老人」分档的唯一来源 |
| `bots` / `bot_chats` / `bot_settings` | 接入的 bot、它的生效群与每群行为、per-bot 阈值覆盖 |
| `admins` | 次级管理员。主管理员**不在这里**，他只存在于配置文件 |
| `gban` / `join_mutes` | 联合封禁名单、进群冷判定的限制记录 |
| `ad_hashes` | 消息级广告的内容指纹（按 bot），同样的内容再出现直接删 |
| `alert_cleanup` | 待撤回的群内告警 |

`group_members` 与 `group_messages` 不带 `bot_id`：同一个群的同一个人
本就该共用一份画像，哪怕两个 bot 都在这个群。

三者按「记录保留天数」一起清理（有命中史的画像行保留 —— 那是风控证据）。
库里有上游 api_key 明文，建议 `chmod 600 data.db`。

## 代码结构

包结构与依赖方向、关键设计沿革见 [CLAUDE.md](../CLAUDE.md)。
