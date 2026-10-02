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

## 前端（Mini App，web/）

Mini App 是独立的 React + MUI 工程（`web/`，源码在 `web/src/`），构建产物由 Go
在 `/miniapp` 托管。需要 Node ≥ 22.12（Vite 8 的下限）：

```bash
npm --prefix web install          # 安装依赖
npm --prefix web run dev          # 开发服务器；/miniapp/api 代理到 127.0.0.1:8081
VITE_MOCK=1 npm --prefix web run dev   # 不连后端：MSW 在浏览器里 mock /miniapp/api
npm --prefix web run typecheck    # tsc -b
npm --prefix web run test         # vitest run
npm --prefix web run lint         # oxlint
npm --prefix web run build        # tsc -b && vite build，产物输出到 internal/panel/webdist/
```

（`VITE_MOCK=1` 只 mock 接口；页面仍要能读到 `window.Telegram.WebApp` 才过得了
打开检查，通常直接在 Telegram 客户端里开，或在 DevTools 里注入一个假 SDK。）

- **构建产物不入库**（`internal/panel/webdist/` 已 gitignore），仓库里不存在构建结果。
- **Telegram SDK 自托管**：`web/public/telegram-web-app.js`（2026-10-02 取自
  telegram.org 的官方脚本，sha256 `3549138a7934039fe7dfd1291a4ee739bd2b705a614308053a8b08a87d85c451`）
  随产物发布到
  `/miniapp/telegram-web-app.js`，`index.html` 用根绝对路径 `/telegram-web-app.js`
  引用（public 资源交给 Vite 按 base 重写，dev/build 都是 `/miniapp/...`）。
  不要改回 CDN 外链——外链一旦拿不到，Mini App 就停在引导页。升级 SDK：替换该
  文件、更新此处的 sha256，并重跑前端测试与 Go 托管测试。
- Go 侧是 build tag 双实现：`go build -tags miniapp` 用 `go:embed all:webdist`
  嵌入产物；不带 tag 时编译占位 stub，`/miniapp` 返回「前端未构建」提示（503）。
  因此没有 Node、没有产物的机器上 `go build` / `go test` 照常工作。
- 托管路由：`/miniapp/assets/*` 长期强缓存；产物根下的文件（自托管 SDK 等）短缓存
  `max-age=3600`；其余未匹配路径回退入口页，`/miniapp/api` 绝不回退。
- 本地要看真实页面：先 `npm --prefix web run build`，再 `go build -tags miniapp`。
- `docker build` 会自动构建前端并带 tag 编译（Dockerfile 的 web 阶段），部署无需手工构建前端。
- CI 会跑 `go test -tags miniapp ./...`（此时 webdist 已构建），本地要复现同样先
  `npm --prefix web run build`。

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
