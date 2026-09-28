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
