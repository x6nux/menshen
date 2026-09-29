# 交接文档（门神 Menshen）

> 交接时间：2026-09-28（晚场更新）｜ 仓库：https://github.com/x6nux/menshen ｜ 分支：main（已全部推送，最新 `be2b712`）
> 本文不含任何密钥。服务器密码、bot token 与 Turnstile 密钥在 `dev/deploy.md`（本机文件，未提交）。

## 1. 一句话现状

线上 dev 实例（`https://menshen.free.edu.kg`）跑 `be2b712`：主 bot 提供面板 /
Mini App / 申诉网页，**工作 bot 已启用**并在「优选IP」群判定（该群目前是**演练模式**）。
申诉网页子系统已端到端验证过（合成单，已清理）：Turnstile → siteverify → 签发解禁码全通。
唯一没跑过的是「真实处罚 → 真人申诉 → 兑换」这条链。

## 2. 已完成的工作（按提交顺序）

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
| `4eb6796` `317198f` | **Mini App**（`/miniapp`）+ 管理员菜单按钮；修复内联脚本语法错误 | 6 个测试 + 线上 `node --check` |
| `2be0b3c` | miniapp：列表→详情两段式导航、跟随全局的配置占位显示「30(全局)」、默认豁免说明卡 | 测试 |
| `e74d278` | miniapp：记录与申诉全流程可操作、时区改 IANA 名、交互细节 | 测试 |
| `fc89a41` | 冷判定演练流水补原文；miniapp 记录详情内联原文、查看页改按钮 | 测试 |
| `88c4a77` | 群内提醒一行化、可关闭、自动撤回 | 测试 |
| `d6b8a21` | **联合封禁改组制**：全局组可选加入、专属组按管理员分账本（新表 `gban_own*`，自动建表）；发言路径新增联合封禁守卫；群内 `/white` = 本群解封 | 测试 |
| `5c3365f` | **bot 豁免收紧**：默认只豁免管理员 bot，普通 bot 照判（`antiad_judge_bots=0` 退回全豁免） | 测试 |
| `27c2a30` | 改派 bot 归属时同步运行中实例（原实现只写库，告警/账本/豁免还指向旧归属人） | 测试 |
| `e3d49f8` | **miniapp 机器人详情页**：移除 bot + 改归属（改归属仅主管理员，目标必须是管理员） | 2 个测试 |
| `d9a1b9d` | **修复网页 CSP**：`script-src 'nonce-xxx` 少了收尾引号，整个 script-src 非法 → 内联脚本从未执行 → 验证成功后永远不出码 | 测试 + 线上实测出码 |
| `7407e07` | 复判首字 5s→15s、`AIClient` 总超时 20s→45s（对齐 tgbotaiapi：初判快、复判给慢模型留时间） | 测试 + 线上 |
| `93a60fc` | 临时禁言 2→5 分钟；复判加 **Shared 级并发闸**（≤50，多 bot 叠加不超上游配额） | 测试 |
| `33d2825` | **「打开 bot」深链带记录号**：管理员点进记录卡片、普通用户进申诉入口 | 测试 |
| `df2fde5` `7fc0fa0` | 初判理由改「判定 广告 置信度:100%，危害度:2.2」；私聊 `/start` 记录原始参数（排障用） | 测试 |
| `47b5bd4` | 冷判定通知改带记录号（`ub<记录号>`）：管理员点进记录卡片、被限制的人进申诉入口 | 测试 |
| `be2b712` | 明显广告（置信度 ≥ 处置线 且危害度 ≥ 2）的群内提醒只弹 30 秒（`antiad_alert_ttl_hard`） | 测试 |

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
| bot | 主 `@admenshen_bot`（8715529198，运行中）；工作 `@anti_ad_ai_bot`（7674016285，**已启用、运行中**） |
| 部署版本 | `be2b712`（2026-09-29 12:0x 部署）；升级时把停用期间积压的 216 条陈旧更新丢弃了（`deleteWebhook?drop_pending_updates=true` 后重新注册） |
| 上游/模型 | 单上游 `lfree`；`antiad_so_model=lfree/jev-1.13`、`antiad_llm_model=lfree/mimo-v2.5`（旧单值键，读侧兼容） |
| 生效群 | 工作 bot 名下 1 个：`-1001976894016`「优选IP」，`enabled=1`、**`dryrun=1`（演练）**、群内展示开、处罚跟随 bot（默认禁言 24h） |
| 全局状态 | 总开关**已开**；冷判定为工作 bot 单独开（`antiad_cold=1`）；`antiad_judge_bots=1`（仅豁免管理员 bot，本次升级新默认） |
| 网页链路 | **Turnstile 已配**（`turnstile_site_key`/`turnstile_secret` 在服务器 config.yaml，域名白名单含 `menshen.free.edu.kg`）；`client_ip_header: CF-Connecting-IP`；验证页实测能签发解禁码 |
| 数据 | 申诉单 0 / 白名单 0 / 合成验证单已清理（`web_checks` 0） |
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
`sqlite3 /opt/menshen-dev/data/data.db`（查库）；服务器上没有 curl，用 `wget -qO-`。
`curl https://menshen.free.edu.kg/healthz`（探活）。

**本机文件（未提交，换机器会丢）**：`dev/deploy.md`（凭据+部署笔记）、
`dev/plan-*.md`（两份设计规划）、`.beads/`（bd 任务库）、
`CLAUDE.md`、`AGENTS.md`（bd 生成）、`.gitattributes`（bd 生成）。
换机器后要重建 `dev/` 并在 `.git/info/exclude` 里加回 `dev/`。

## 4. 待办（按优先级）

1. **端到端跑一次真实处罚 + 申诉**（唯一没跑过的链路，网页段已单独验证）：
   - 把「优选IP」群切到**正式模式**（Mini App → 群组 → 演练开关；或面板）
   - 用一个**非管理员**测试小号在群里发一条明显广告 → 应删除 + 禁言 24h；
     该号需先私聊过 `@anti_ad_ai_bot` 才能收到申诉入口（收不到也可直接
     `t.me/anti_ad_ai_bot?start=appeal`）
   - 私聊走：申诉理由 → AI 复核 → 维持原判时网页人机验证 → 解禁码 →
     群里/私聊发码兑换 → 白名单 24h。全程可对照 `antiad_log` / `appeals` /
     `web_checks` / `ad_whitelist` 四张表
   - 注意：主管理员、归属人、群管、有管理员权限的 bot 都是默认豁免；
     工作 bot 必须是该群管理员
2. **Mini App 已知缺口（剩）**：新加的次级管理员要等下次重启才会收到菜单
   按钮（`AddAdmin` 不触发 `registerMiniAppButton`）
3. **安全**：轮换服务器密码与 bot token（聊天记录里明文出现过）；
   Cloudflare Turnstile 的 site key/secret 也已在聊天里出现过，必要时重建
4. **收尾决定**：`AGENTS.md` / `.gitattributes`（bd 产物）是否提交；
   冗余合并提交 `0fb5683` 是否清理（无害，可不动）

## 5. 开发约定（接手必读）

- **提交前必跑**：`gofmt -l .`（必须为空）、`go vet ./...`、`go test ./...`。
  CI（`.github/workflows/docker.yml`）也以这三项为门槛。
- **测试文化**：先写会变红的测试再实现；注释用中文，解释「为什么」而不是「做了什么」；
  提交信息中文、带模块前缀（如 `antiad:`、`panel:`、`miniapp:`）。
- **网页侧改动要当心 CSP**：验证页靠 `script-src 'nonce-<value>'` 放行内联脚本，
  **nonce 表达式必须成对引号**且与页面脚本上的 nonce 一致 —— 少了收尾引号时
  CSP 解析器会把整个 `script-src` 判无效、静默回退 `default-src 'self'`，
  页面表面上一切正常（Turnstile 都能解题），直到用户拿不到码才暴露。
  `TestAppealPageHeadersAndExpiry` 用严格正则钉住了这一点。
- **真机调试页面**：本机可用的 agent-browser（CloakBrowser，`agbt`/`agb`）
  能给出正常 Chrome UA 与 `navigator.webdriver=false`，Turnstile 可自动通过；
  用它验证过本次的「出码」全流程。
- **任务跟踪**：bd（beads）工作流，`bd ready` / `bd create` / `bd close` / `bd sync`；
  本仓库的 bd 状态在 `.beads/`（本机排除，未提交）。
- **本地运行**：`CGO_ENABLED=0 go build -o menshen . && ./menshen -config config.yaml`；
  轮询模式只跑主 bot（而主 bot 不判定），要看完整功能需 webhook 模式。
- **Mini App API 手工调试**：用 bot token 按 Telegram WebApp 规则签一份 initData
  （`secret = HMAC_SHA256(key="WebAppData", msg=token)`，
  `hash = HMAC_SHA256(secret, data_check_string)`；`data_check_string` 是除 hash
  外按 key 排序的 `k=v` 用 `\n` 连接），请求头带 `X-Tg-Init-Data` 与 `X-Bot-Id`。
  本次就是用它启用了工作 bot、丢弃陈旧更新、验证 owner_opts。token 见 `dev/deploy.md`。

## 6. 本次新增/改动的关键代码

| 位置 | 职责 |
|---|---|
| `internal/antiad/appeal.go` | 申诉入口、有效限制、AI 复判、自动解除、卡片 |
| `internal/antiad/appeal_web.go` `appeal_web_page.go` `appeal_web_html.go` | 验证页、siteverify、信号/指纹/IP、路由与签名（CSP nonce 修复在 `writeHTMLHeaders`） |
| `internal/antiad/appeal_code.go` | 解禁码生成/识别、群内与私聊兑换、白名单写入 |
| `internal/antiad/adview.go` | 查看页 `v` / 申诉详情页 `apv` |
| `internal/antiad/gban.go` | 联合封禁三态：全局组 / 专属组 / 群内解封（`gban_own*` 三表） |
| `internal/panel/miniapp.go` `miniapp_html.go` | Mini App API 与前端；`miniBot` 的 `remove` / `owner` 两个动作 |
| `internal/core/upstream.go` | 上游改名连带改写、旧模型自动补前缀 |
| `internal/core/registry.go` | `SetBotOwner` 改派 + 运行中实例同步 |
| `main.go` | `webRouter`（`_w` → 网页、`/miniapp` → Mini App、其余 → webhook）；启动走 `FinishSetup` |
| `internal/core/bot.go` | `IsMainBot`、群命令菜单、`registerMiniAppButton`、`JoinNotice` |

更多设计细节见 `CLAUDE.md`（本机）与 `docs/`、`README.md`。
