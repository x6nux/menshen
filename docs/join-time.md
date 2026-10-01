# 入群时间：接口与调用方式

入群时间是「新人 / 老人」分档（年龄轴）和 `/jtime` 的数据来源。Bot API
拿不到它，只能走 MTProto —— 好在**仍然不需要用户账号**：用 bot 自己的
token 就能登录（`auth.importBotAuthorization`）。

本文是这条链路的接口清单与调用方式。按需实时查询的实现见
`internal/antiad/joinbackfill.go`（`ResolveJoinTime`），脚本见
`internal/antiad/scripts/tgjoin_backfill.py`。

## 一、为什么 Bot API 不够用

- `getChatMember` 没有任何「入群时间」字段，只有 `status` / 权限 /
  `until_date`（而且 `until_date` 只对限制有效）。
- Bot API 的 `chat_member` 更新只在**实时发生**时推送 —— 它负责新入群的
  实时记录（主来源），但 bot 拿到管理员权限之前就在群里的人没有事件可收。
- 群主以外的成员记录里，入群时间只在 MTProto 的 participant 记录里
  （`ChannelParticipant.date`，官方定义就是 Date joined）。

## 二、接口清单

### Bot API（HTTPS，bot token）

| 接口 | 用途 | 什么时候调用 |
|---|---|---|
| `getChat` | 取群的公开用户名（给脚本解析群实体用） | 每次批量 / 单查前（Go 侧 `chatUsername`） |
| `getChatMember` | 取用户状态与昵称 / 用户名 | **只在单查主路径没结果时的兜底**（判断还在不在群、按名字搜索） |

```bash
curl "https://api.telegram.org/bot<TOKEN>/getChat?chat_id=-1002124027757"
curl "https://api.telegram.org/bot<TOKEN>/getChatMember?chat_id=-1002124027757&user_id=1397983659"
```

注意：`getChatMember` 的 `status` 可能是 `member` / `restricted` /
`administrator` / `creator` / `left` / `kicked`。**`restricted` 也可能是
残留记录** —— 人其实已经被移出（MTProto 里是 `left=True`），不能只凭
Bot API 状态判断他还在不在群。

### MTProto（Telethon + bot token）

| 接口 | 用途 | 什么时候调用 |
|---|---|---|
| `auth.importBotAuthorization`（`client.start(bot_token=...)`） | bot 登录 MTProto | 每次起脚本 |
| `updates.getState` 增量（`client.catch_up`） | 把错过的更新里的 peer 塞进实体缓存 | 每次起脚本（失败不影响后续） |
| **`channels.getParticipant`** | 单查一个人的成员记录 | **按需实时查询的主路径** |
| `channels.getParticipants` | 批量扫成员列表 | 批量模式（Mini App「补全历史入群时间」按钮） |
| `channels.getParticipants` + `ChannelParticipantsSearch(q=名字)` | 按名字搜索成员（可越过批量列表的截断） | 单查主路径失败后的兜底 |

```python
from telethon import TelegramClient
from telethon.tl.functions.channels import GetParticipantRequest
from telethon.tl.types import InputPeerUser

client = TelegramClient(session_path, api_id, api_hash)
await client.start(bot_token=token)
ch = await client.get_entity("group_username")   # 或按 chat_id（私有群要靠会话缓存）

r = await client(GetParticipantRequest(channel=ch, participant=InputPeerUser(uid, 0)))
p = r.participant      # ChannelParticipant / ChannelParticipantBanned / …
```

**关键坑（吃过大亏）**：`participant` 一定要用 `InputPeerUser(uid, 0)`，
**access_hash 传 0 就能拿到记录**。直接传裸 `uid`（int）时 Telethon 会先
去解析实体，没缓存就报 `Could not find the input entity for PeerUser(...)`
—— 会把还在群的人误判成「查不到」。

**批量列表有硬上限**：Telegram 对 `channels.getParticipants` 的可翻页数
有服务端限制，实测约 **1 万条** —— 五万人的群只能扫到 ~9988，小群也会漏
（受限账号等）。Telethon 1.45 里 `iter_participants(aggressive=True)` 已是
空操作（官方注释：API 限制不再允许绕过）。所以批量结果只是「尽量补」，
漏掉的人靠按需单查。

### 试过、但 Telegram 不给 bot 用（别再试）

| 接口 | 结果 |
|---|---|
| `channels.getAdminLog`（管理日志里有入群事件） | `BOT_METHOD_INVALID`：bot 用户被禁止调用 |
| `messages.getChatInviteImporters`（邀请导入者，带日期） | 同上 |
| `messages.getExportedChatInvites`（导出邀请链接） | 同上 |

### 其他消息

`channels.getMessages`（曾经想用它反查 access_hash）已删除：被删除的消息
取回来没有 `from_id`、`users` 为空，抠不出东西。

## 三、成员记录的语义（决定 date 能不能用）

| 记录类型 | 含义 | `date` 的语义 | 能当入群时间吗 |
|---|---|---|---|
| `ChannelParticipant` | 普通成员 | 入群时间 | ✅ |
| `ChannelParticipantAdmin` | 管理员 | 入群时间 | ✅ |
| `ChannelParticipantBanned`，`left=False` | 受限但仍在本群 | **入群时间** | ✅ |
| `ChannelParticipantBanned`，`left=True` | 已被移出 | **移除时间**，不是入群时间 | ❌ |
| `ChannelParticipantLeft` | 已退群 | 无 date 字段 | ❌ |
| `ChannelParticipantCreator` | 群主 | 无 date 字段 | ❌ |

`ChannelParticipantBanned` 的官方注释写的是 "When was the user banned"，
很容易以为那是限制时间而跳过 —— 实测对 `left=False`（还在群、只被限制
部分权限）的记录，**date 就是入群时间**。

验证方法（将来回归可复用）：拿**实时记录过入群时间**的人（Bot API
`chat_member` 更新写入的 `joined_at`）与 MTProto 记录逐条对照。曾在两个群
全量比对（4411 + 9989 人）：普通成员 **348 条一致**、受限成员 **53 条一致**
（少数不一致，疑似重新入群后 date 取最近一次）；再用 4 个「我们处罚过、
处罚时间明显晚于入群」的人做对照，date 全部等于入群时间而不是处罚时间。

## 四、调用流程

### 按需单查（主路径）

```
/jtime、消息判定、哈希命中、/check 复查、冷判定
        │
        ▼
ResolveJoinTime(bot, chat_id, uid)                       [Go]
  ├─ 库里 joined_at>0？→ 直接返回（库就是缓存）
  ├─ 负缓存命中？→ 直接放弃
  ├─ 拿不到脚本锁（批量在跑）？→ 放弃，不排队
  └─ 起脚本：python3 tgjoin_backfill.py --chat … --username … --lookup <uid>
        │
        ▼
  ① channels.getParticipant(InputPeerUser(uid, 0))       [主路径]
  ② ①没结果 → getChatMember 拿名字 + channels.getParticipants(search)  [兜底]
        │
        ▼
  输出一行 TSV：chat_id \t uid \t unix
        │
        ▼
  Go 侧写库：UPDATE group_members SET joined_at=? WHERE … AND joined_at=0
```

### 批量（Mini App 按钮）

```
面板 POST /miniapp/api/chat {action:"backfill"}          [Go]
  → StartJoinBackfill（force：绕过 24h 冷却；全局单飞锁）
  → python3 tgjoin_backfill.py --chat … --username …
      → channels.getParticipants 分页扫成员
      → 每行 TSV
  → 只填 joined_at=0 的行（不覆盖实时记录）
  → 私聊通知管理员「补上 N 条」
```

## 五、缓存、限流与触发点

- **库即缓存**：查到一次写进 `group_members.joined_at`，之后所有路径秒回；
  批量写入也只填 `joined_at=0`，不覆盖实时记录。
- **负缓存**：`ResolveJoinTime` 内部按 (bot, chat, uid) 记失败 ——
  「确实没有」（已退群/被踢）缓存 6 小时，「查询失败」（网络 / FLOOD_WAIT /
  超时）缓存 15 分钟，避免反复起脚本。
- **单飞与串行**：所有脚本共用一把全局锁 `joinBackfillRunning`（同一个 bot
  的 MTProto 会话文件不能并发给两个进程用）；单查之间另有 `joinLookupMu`
  排队。批量在跑时单查直接放弃，不让判定排队等十分钟。
- **超时**：单查 20 秒（`joinLookupTimeout`），批量 10 分钟
  （`joinBackfillTimeout`）。
- **触发点**：`/jtime`、`judgeAndAct`（消息判定）、`hashHit`（哈希命中）、
  `HandleAdCommand`（/check 复查）、`coldJudge`（进群冷判定）、
  `reviewProfileOnly`（资料复查）。都通过 `ensureJoinAge` 补进画像。
- **新成员**：由 Bot API `chat_member` 更新实时记录，不走这条路。

## 六、调用量与耗时

| 场景 | 请求数 | 耗时 |
|---|---|---|
| 单查命中（正常） | 2~3 次：`getChat` + 解析群实体 + `getParticipant` | 约 2~5 秒（含起 python + 连 MTProto） |
| 单查走兜底 | 最多 +2 次：`getChatMember` + 搜索 | 再多几秒 |
| 批量（万人群） | 1 次 `getChat` + 约 100 次翻页 | 1 分钟内（10 万人群受 1 万条上限约束，扫不满） |

## 七、运行前提与配置

- 配置项 `tg_api_id` / `tg_api_hash`（my.telegram.org 申请；这是 MTProto
  客户端标识，与 bot token 不是一回事）。没配就只记日志、跳过。
- 宿主需要 `python3` + `telethon`（缺了同样只记日志、跳过，不影响判定）。
- 脚本以 `go:embed` 嵌进二进制，首次使用时写到
  `data/join_backfill/tgjoin_backfill.py`；MTProto 会话文件在
  `data/join_backfill/sessions/`（每个 bot 一份，重启复用，写权限 700）。

## 八、代码位置

| 位置 | 干什么 |
|---|---|
| `internal/antiad/scripts/tgjoin_backfill.py` | 脚本本体：批量模式 + 单查模式（`--lookup`） |
| `internal/antiad/joinbackfill.go` | `StartJoinBackfill`（按钮批量）、`ResolveJoinTime`（按需单查）、`ensureJoinAge`、`chatUsername`、负缓存与锁 |
| `internal/antiad/jtime.go` | `/jtime` 渲染，库里没有时调 `ResolveJoinTime` |
| `internal/panel/miniapp.go` / `miniapp_html.go` | 群组卡片的「补全历史入群时间」按钮 |
| `internal/antiad/antiad.go`、`hash.go`、`coldjudge.go` | 各判定路径的 `ensureJoinAge` 触发点 |

设计上**不再预先全量补全**：拿到管理员权限不再自动扫全群，逐人批量补查也
已去掉；用到谁查谁、查到即入库。批量只保留 Mini App 按钮这一个手动入口。
