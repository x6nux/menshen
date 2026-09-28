# 交接文档（门神 Menshen）

> 交接时间：2026-09-28 ｜ 仓库：https://github.com/x6nux/menshen ｜ 分支：main（已全部推送）
> 本文不含任何密钥。服务器密码与 bot token 在 `dev/deploy.md`（本机文件，未提交）。

## 1. 一句话现状

线上 dev 实例（`https://menshen.free.edu.kg`）在跑最新代码：主 bot 提供面板 /
Mini App / 申诉网页，工作 bot 负责群内判定（**当前是停用状态**）。本会话完成了
主 bot 隔离、多模型轮换、上游告警、申诉与网页子系统、Mini App 配置台、群内静默。

## 2. 本次会话完成的工作（按提交顺序）

| 提交 | 内容 | 验证 |
|---|---|---|
| `5290edd` | README 改为项目介绍，技术细节拆到 `docs/` | 链接核对 |
| `7f50f70` `9403c0a` | `dev/` 本地目录，排除规则走本机 `.git/info/exclude` | `git check-ignore` |
| `8e5ec26` | **主 bot 隔离**：`bots.is_main` 落库；群消息/编辑/成员事件硬守卫；被拉进群自动静默退出；面板只读；启停/移除在 Registry 层拒绝；命令菜单跳过群 scope | 13 个测试 |
| `a46ea82` | 只有主 bot 时不再提示「名下没有群组」；菜单计数区分主/工作 bot | 2 个测试 |
| `67953d2` | **多模型按序重试 + 上游绑定前缀 `<上游名>/<模型ID>` + 上游连续失败告警**；面板新增模型先选上游；上游改名连带改写、有模型时拒删 | 12 个测试 |
| `1580f9f` | 只有一个上游时启动自动给旧格式模型补前缀（含冗余合并 `0fb5683`，无害） | 测试 + 线上日志 |
| `b4dcc24` | 进群冷判定提为全局设置（`both` 分组），bot 默认跟随、可覆盖、填 `-` 撤销 | 测试 |
| `72b310a` | **申诉/网页 阶段1**：4 张新表、白名单快照 + 两处检查点、3 个配置项、`web_secret` 与签名、`_w` 路由 | 测试 |
| `d1e1ff4` | **阶段2**：申诉入口、有效限制查询、AI 复判（出错不解除）、自动解除、卡片；**删除 captcha 包**；命令改名 | 6 个测试 |
| `8dd6f23` | **阶段3**：Turnstile 验证页、siteverify、硬/软信号、指纹、客户端 IP、`web_checks`、签发解禁码、关联账号 | 8 个测试 |
| `6741f5a` | **阶段4**：群内/私聊兑换、白名单 24h、LiftGban、清理任务 | 5 个测试 |
| `088e9b9` | **阶段5**：查看页 `v` / 申诉详情页 `apv`、`/log` `/user`、展示去敏、`/white` 私聊、白名单面板、入群服务消息删除 | 6 个测试 |
| `3036f56` | **群内静默** `antiad_group_silent`（per-bot）：群里一条消息都不发，判定/处置/兑换照常 | 测试 |
| `4eb6796` `317198f` | **Mini App**（`/miniapp`）+ 管理员菜单按钮；修复内联脚本语法错误（页面卡「加载中」的根因） | 6 个测试 + 线上 `node --check` |

**命令改名（用户要求）**：`/ad`→`/check`、`/adb`→`/ban`、`/adw`→`/white`；
管理员私聊新增 `/log <id>`、`/user <uid>`。旧命令不再响应。
`docs/superpowers/specs/2026-09-24-appeal-and-web-design.md` 是设计档案，
里面的旧命令名未同步（历史记录，勿改）。

## 3. 线上环境（dev）

| 项 | 值 |
|---|---|
| 服务器 | 见 `dev/deploy.md`（公开仓库不写源站 IP，Debian 13） |
| 域名 | `https://menshen.free.edu.kg`（Cloudflare 回源 80，TLS 在 CF 终止） |
| 服务 | systemd `menshen-dev`，`/opt/menshen-dev/menshen`，配置 `/opt/menshen-dev/config.yaml`，库 `/opt/menshen-dev/data/data.db` |
| bot | 主 `@admenshen_bot`（8715529198，运行中）；工作 `@anti_ad_ai_bot`（7674016285，**已停用**） |
| 全局状态 | 总开关 **已开**；冷判定全局未开（工作 bot 单独开了）；`turnstile_*` 与 `client_ip_header` **未配**；申诉单/白名单均为 0（申诉链路尚未真跑过） |
| 凭据 | 在 `dev/deploy.md`（本机，未提交）。**部署前请先轮换密码与 token** |

**部署流程**（本机执行）：

```bash
cd <repo>
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X 'main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" -o /tmp/menshen-linux-amd64 .
sshpass -p '<密码>' scp /tmp/menshen-linux-amd64 root@<dev-server-ip>:/opt/menshen-dev/menshen.new
sshpass -p '<密码>' ssh root@<dev-server-ip> \
  'systemctl stop menshen-dev; mv /opt/menshen-dev/menshen.new /opt/menshen-dev/menshen; \
   chmod 755 /opt/menshen-dev/menshen; systemctl start menshen-dev'
```

**运维速查**：`journalctl -u menshen-dev -f`（日志）；
`sqlite3 /opt/menshen-dev/data/data.db`（查库）；
`curl https://menshen.free.edu.kg/healthz`（探活）。

**本机文件（未提交，换机器会丢）**：`dev/deploy.md`（凭据+部署笔记）、
`dev/plan-*.md`（两份设计规划）、`.beads/`（bd 任务库，23 个任务全关闭）、
`CLAUDE.md`、`AGENTS.md`（bd 生成）、`.gitattributes`（bd 生成）。
换机器后要重建 `dev/` 并在 `.git/info/exclude` 里加回 `dev/`。

## 4. 待办（按优先级）

1. **启用工作 bot**（或在 TG 侧删除它）：它停用时判定完全不工作，
   Telegram 仍往它推更新，日志里会刷「未注册的 bot，已拒绝」。
   入口：Mini App → 机器人 → 启用；或 bot 面板。
2. **配 Turnstile**：`turnstile_site_key` / `turnstile_secret`（Cloudflare 后台，
   域名白名单必须含 `menshen.free.edu.kg`）+ `client_ip_header: CF-Connecting-IP`。
   不配的话申诉在 AI 维持原判后降级为「请联系群管理员」（noweb）。
3. **端到端验证申诉**：制造一次真实处罚 → 私聊申诉 → AI 复核 → 网页验证 →
   解禁码 → 群内/私聊兑换。目前库里 0 条申诉，链路只跑过单测。
4. **Mini App 已知缺口**（后端已支持、前端没做 UI）：
   - 群组页：没有「移除群」按钮；处罚方式（跟随/禁言/封禁）只读展示，
     不能切换（后端 `chat update` 已支持 `punish` 与 `remove`）
   - 机器人页：没有删除 bot / 改归属
   - 新加的次级管理员要等下次重启才会收到菜单按钮（`AddAdmin` 不触发
     `registerMiniAppButton`）
5. **安全**：轮换服务器密码与 bot token（聊天记录里明文出现过）。
6. **收尾决定**：`AGENTS.md` / `.gitattributes`（bd 产物）是否提交；
   冗余合并提交 `0fb5683` 是否清理（无害，可不动）。

## 5. 开发约定（接手必读）

- **提交前必跑**：`gofmt -l .`（必须为空）、`go vet ./...`、`go test ./...`。
  CI（`.github/workflows/docker.yml`）也以这三项为门槛。
- **测试文化**：先写会变红的测试再实现；注释用中文，解释「为什么」而不是「做了什么」；
  提交信息中文、带模块前缀（如 `antiad:`、`panel:`、`miniapp:`）。
- **任务跟踪**：bd（beads）工作流，`bd ready` / `bd create` / `bd close` / `bd sync`；
  本仓库的 bd 状态在 `.beads/`（本机排除，未提交）。
- **本地运行**：`CGO_ENABLED=0 go build -o menshen . && ./menshen -config config.yaml`；
  轮询模式只跑主 bot（而主 bot 不判定），要看完整功能需 webhook 模式。
- **Mini App API 手工调试**：用 bot token 按 Telegram WebApp 规则签一份 initData
  （`secret = HMAC_SHA256("WebAppData", token)`，`hash = HMAC_SHA256(secret, data_check_string)`），
  请求头带 `X-Tg-Init-Data` 与 `X-Bot-Id`。token 见 `dev/deploy.md`。

## 6. 本次新增/改动的关键代码

| 位置 | 职责 |
|---|---|
| `internal/antiad/appeal.go` | 申诉入口、有效限制、AI 复判、自动解除、卡片 |
| `internal/antiad/appeal_web.go` `appeal_web_page.go` `appeal_web_html.go` | 验证页、siteverify、信号/指纹/IP、路由与签名 |
| `internal/antiad/appeal_code.go` | 解禁码生成/识别、群内与私聊兑换、白名单写入 |
| `internal/antiad/adview.go` | 查看页 `v` / 申诉详情页 `apv` |
| `internal/panel/miniapp.go` `miniapp_html.go` | Mini App API 与前端 |
| `internal/core/upstream.go` | 上游改名连带改写、旧模型自动补前缀 |
| `main.go` | `webRouter`（`_w` → 网页、`/miniapp` → Mini App、其余 → webhook）；启动走 `FinishSetup` |
| `internal/core/bot.go` | `IsMainBot`、群命令菜单、`registerMiniAppButton`、`JoinNotice` |

更多设计细节见 `CLAUDE.md`（本机）与 `docs/`、`README.md`。
