# 入群时间：API 文档

查群成员「入群时间」用到的接口参考。全流程只用 bot token，不需要用户账号。

**先说清楚两件事**：

1. **只有 Bot API 能直接用 curl 调**（`getChat`、`getChatMember`）；
2. **MTProto 方法（`channels.getParticipant` 等）不能 curl** —— 它是二进制
   加密协议，Telegram 没有把它暴露成 HTTP/REST（原因见 1.2）。入群时间
   恰恰只在 MTProto 的成员记录里，所以纯 curl 拿不到入群时间本身。

## 1. 总览

### 1.1 接口清单

| 接口 | 协议 | curl 直调 | 用途 |
|---|---|---|---|
| `getChat` | Bot API（HTTPS） | ✅ | 取群资料（公开用户名等） |
| `getChatMember` | Bot API（HTTPS） | ✅ | 取成员状态、昵称、用户名 |
| `auth.importBotAuthorization` | MTProto | ❌ | bot 登录 MTProto（拿 auth_key） |
| `updates.getState` | MTProto | ❌ | 取更新状态（拉增量、缓存 peer） |
| `channels.getParticipant` | MTProto | ❌ | 单查一个成员的记录，**入群时间在这里** |
| `channels.getParticipants` | MTProto | ❌ | 批量列成员；带搜索过滤器可按名字找 |
| `channels.getAdminLog` | MTProto | ❌ | 有入群事件，但 **bot 被 Telegram 禁止调用** |
| `messages.getChatInviteImporters` | MTProto | ❌ | 邀请导入者（带日期），同样被禁 |
| `messages.getExportedChatInvites` | MTProto | ❌ | 导出邀请链接，同样被禁 |

### 1.2 为什么 MTProto 不能用 curl

MTProto 不是 HTTP API，而是二进制加密协议：

1. 先做 `auth_key` 握手（Diffie-Hellman 密钥交换）；
2. 之后每个请求都要用 AES-IGE 加密、带 message id / seqno、按 TL schema
   二进制序列化；
3. Telegram 没有提供 HTTP/REST 网关。（协议里的 "HTTP transport" 也只是把
   加密后的二进制包塞进 HTTP body，没有客户端库照样调不动。）

所以 MTProto 方法必须用 MTProto 客户端（Telethon、TDLib 等）调用；能
curl 的只有 Bot API，而 Bot API 没有入群时间字段。

### 1.3 参数里的两种凭据

| 凭据 | 从哪来 | 用在哪 |
|---|---|---|
| bot token | @BotFather | Bot API 的 URL；MTProto 登录 |
| api_id / api_hash | my.telegram.org 申请 | MTProto 登录（客户端应用标识） |

补充：`api_id / api_hash` 是**可选**的 —— 只在需要回查历史成员的入群时间时
才用得到。不配就跳过这类查询（已经记录过的人不受影响，新入群由 `chat_member`
实时记录）。要用的话建议填自己申请的；自建实例用公开的 Telegram Desktop
值（`2040` / `b18441a1ff607e10a989891a5462e627`）也能用，但共用同一个
api_id 有被限流的风险。

## 2. Bot API（curl 可调）

- 基址：`https://api.telegram.org/bot<TOKEN>/<方法>`
- 参数可放 query string（GET）或 JSON body（POST）
- 返回统一为 `{"ok":true,"result":…}`；失败为
  `{"ok":false,"error_code":…,"description":"…"}`
- 高频调用会返回 `429` / `retry_after`，按提示等待

### 2.1 getChat

取群资料。

| 参数 | 必填 | 说明 |
|---|---|---|
| `chat_id` | 是 | 群 id（`-100…`）或公开群用户名（`@CMLiussss`） |

```bash
curl -s "https://api.telegram.org/bot$TOKEN/getChat?chat_id=@CMLiussss"
```

```json
{"ok":true,"result":{"id":-1002124027757,"title":"CMLiussss 技术交流群",
 "type":"supergroup","username":"CMLiussss","is_forum":false}}
```

用途说明：本链路只用它取 `username` —— 有公开用户名时 MTProto 解析群实体
最稳、私有群里没有 `username` 字段。

### 2.2 getChatMember

取某成员的状态与资料。

| 参数 | 必填 | 说明 |
|---|---|---|
| `chat_id` | 是 | 群 id 或 `@用户名` |
| `user_id` | 是 | 对方的数字 id |

```bash
curl -s "https://api.telegram.org/bot$TOKEN/getChatMember?chat_id=@CMLiussss&user_id=1397983659"
```

```json
{"ok":true,"result":{"status":"member",
 "user":{"id":1397983659,"is_bot":false,"first_name":"…","username":"…"}}}
```

`status` 取值与注意点：

| status | 含义 | 备注 |
|---|---|---|
| `creator` / `administrator` | 群主 / 管理员 | 在群 |
| `member` | 普通成员 | 在群 |
| `restricted` | 受限成员 | 带 `until_date`（0=永久）与一组 `can_*` 权限字段；**可能是残留记录** —— 人其实已被移出（MTProto 里 `left=true`），不能只凭它判断还在不在群 |
| `left` / `kicked` | 已退群 / 被踢 | 不在群 |

- **没有入群时间字段**：`until_date` 只对限制/踢出有效。
- 对不在群/查不到的人返回 400，例如
  `{"ok":false,"error_code":400,"description":"Bad Request: user not found"}`
- 返回的 `user` 里有名字与用户名，MTProto 侧解析不开实体时可用它做按名字搜索。

## 3. MTProto（curl 不可调，需 MTProto 客户端）

以下按官方 schema 列出方法名、参数与返回；参数名与返回字段名保持 schema
原样，便于对照官方文档。示例基于 **Telethon**（本项目用的就是它）：

```bash
pip install telethon            # Python 3.8+
```

### 3.1 auth.importBotAuthorization

bot 以自身 token 登录，换 `auth_key`。所有 MTProto 调用的前提。

| 参数 | 类型 | 说明 |
|---|---|---|
| `api_id` | int | my.telegram.org 申请 |
| `api_hash` | string | 同上 |
| `bot_auth_token` | string | bot token（`123456:ABC…`） |

成功后进入正常会话；bot 会话不需要手机号验证码。

```python
from telethon import TelegramClient

client = TelegramClient("demo.session", api_id, api_hash)  # api_id/api_hash 来自 my.telegram.org
await client.start(bot_token="123456:ABC…")                # 内部即 auth.importBotAuthorization
```

### 3.2 updates.getState

取当前更新状态。无参数。返回 `updates.state{pts,qts,date,seq,unread_count}`。

用途：客户端启动时拉一次增量（`updates.getDifference`），顺带把错过的更新
里的 peer 写进实体缓存 —— 私有群（没有公开用户名）靠它才解析得出群实体。

```python
await client.catch_up()      # 内部：updates.getState + updates.getDifference
```

### 3.3 channels.getParticipant —— 单查（入群时间在这里）

| 参数 | 类型 | 说明 |
|---|---|---|
| `channel` | InputChannel | 目标群（`-100…` 的超群） |
| `participant` | InputPeer | 要查的人：`inputPeerUser{user_id, access_hash}` |

返回 `channels.channelParticipant{participant: …}`。

**关键**：`access_hash` **填 0 也能拿到记录**（实测）。不要只传裸
`user_id` —— 多数客户端会先做本地实体解析，缓存里没有就报
`Could not find the input entity for PeerUser(...)`，把还在群的人误判成
「查不到」。

`participant` 是六种构造器之一，语义见第 4 节。

常见错误：`USER_NOT_PARTICIPANT`（目标不在群）、`CHANNEL_INVALID`（群实体
不对或身份不对）。

```python
from telethon.tl.functions.channels import GetParticipantRequest
from telethon.tl.types import (ChannelParticipant, ChannelParticipantAdmin,
                               ChannelParticipantBanned, InputPeerUser)

ch = await client.get_entity("@CMLiussss")     # 或 -100 开头的群 id（私有群靠 catch_up 缓存）
r = await client(GetParticipantRequest(channel=ch,
        participant=InputPeerUser(1397983659, 0)))   # access_hash 填 0
p = r.participant
print(type(p).__name__, getattr(p, "date", None), getattr(p, "left", None))

def join_ts(p):
    """六种构造器里，哪些 date 能当入群时间。"""
    if isinstance(p, (ChannelParticipant, ChannelParticipantAdmin)):
        return int(p.date.timestamp())
    if isinstance(p, ChannelParticipantBanned) and not p.left:   # 受限但仍在群
        return int(p.date.timestamp())
    return 0        # 群主 / 已被移出 / 已退群：拿不到
```

### 3.4 channels.getParticipants —— 批量 / 按名字搜索

| 参数 | 类型 | 说明 |
|---|---|---|
| `channel` | InputChannel | 目标群 |
| `filter` | ChannelParticipantsFilter | `channelParticipantsSearch{q: 名字}` 按名字搜索（昵称/姓/用户名包含 q）；不传则默认列表 |
| `offset` | int | 分页偏移 |
| `limit` | int | 单次条数，上限 200 |
| `hash` | long | 填 0 |

返回 `channels.channelParticipants{count, participants, users}`；
`participants` 就是第 4 节那六种构造器的列表（普通成员带入群时间）。

限制（实测）：

- **服务端可翻页总量约 1 万条**：五万人的群只能扫到 ~9988（小群也会漏，
  比如部分受限账号）——批量只能「尽量补」，漏掉的人要单查。
- 连续翻页会撞 `FLOOD_WAIT_X`，需要等 X 秒再继续。

```python
from telethon.tl.functions.channels import GetParticipantsRequest
from telethon.tl.types import ChannelParticipantsSearch

ch = await client.get_entity("@CMLiussss")

# 一页（limit 上限 200；翻页就改 offset，直到返回条数 < limit）
r = await client(GetParticipantsRequest(channel=ch, hash=0,
        filter=ChannelParticipantsSearch(q=""), offset=0, limit=200))
print(r.count, len(r.participants))       # count 是总数（但翻页最多约 1 万条）

# 按名字找某个人：批量列表漏掉时的兜底
r2 = await client(GetParticipantsRequest(channel=ch, hash=0,
        filter=ChannelParticipantsSearch(q="张"), offset=0, limit=200))
hit = [p for p in r2.participants
       if (getattr(p, "user_id", None) or p.peer.user_id) == 1397983659]
```

小提示：`participants` 里受限/已退群的记录没有 `user_id`，改用 `p.peer.user_id`。

### 3.5 合起来的完整最小示例

```python
"""查一个人在某个群的入群时间。"""
import asyncio
from telethon import TelegramClient
from telethon.tl.functions.channels import GetParticipantRequest
from telethon.tl.types import (ChannelParticipant, ChannelParticipantAdmin,
                               ChannelParticipantBanned, InputPeerUser)

API_ID, API_HASH = 2040, "b18441a1ff607e10a989891a5462e627"   # my.telegram.org
TOKEN = "123456:ABC…"                # @BotFather
GROUP = -1002124027757               # 或 "@CMLiussss"
UID = 1397983659

def join_ts(p):
    if isinstance(p, (ChannelParticipant, ChannelParticipantAdmin)):
        return int(p.date.timestamp())
    if isinstance(p, ChannelParticipantBanned) and not p.left:
        return int(p.date.timestamp())
    return 0                          # 群主 / 已被移出 / 已退群

async def main():
    client = TelegramClient("demo.session", API_ID, API_HASH)
    await client.start(bot_token=TOKEN)
    await client.catch_up()           # 私有群要靠它缓存 peer
    ch = await client.get_entity(GROUP)
    r = await client(GetParticipantRequest(channel=ch,
            participant=InputPeerUser(UID, 0)))
    print(join_ts(r.participant))     # 0 = 拿不到

asyncio.run(main())
```

## 4. 成员记录的 date 语义

`channels.getParticipant` / `channels.getParticipants` 返回的
`participant` 有六种构造器，`date` 的含义不一样：

| 构造器 | 含义 | 关键字段 | `date` 能当入群时间吗 |
|---|---|---|---|
| `channelParticipant` | 普通成员 | `user_id`、`date` | ✅ 入群时间 |
| `channelParticipantAdmin` | 管理员 | `user_id`、`date`、`promoted_by` | ✅ 入群时间 |
| `channelParticipantBanned`，`left=false` | 受限但仍在群 | `peer`、`date`、`banned_rights` | ✅ **入群时间** |
| `channelParticipantBanned`，`left=true` | 已被移出 | `peer`、`date`、`kicked_by` | ❌ 移除时间 |
| `channelParticipantLeft` | 已退群 | `peer` | ❌ 无 `date` |
| `channelParticipantCreator` | 群主 | `user_id`、`rank` | ❌ 无 `date` |

易错点：`channelParticipantBanned` 的官方注释写的是 "When was the user
banned"，容易以为 `date` 是限制时间而跳过 —— 实测对 `left=false`（还在群、
只被限制部分权限）的记录，`date` 就是入群时间。

验证方法（回归时可用）：拿 Bot API `chat_member` 更新实时记录过的入群时间，
与 MTProto 记录逐条对照。曾两个群全量比对（4411 + 9989 人）：普通成员
348 条一致、受限成员 53 条一致；另用 4 个「处罚时间明显晚于入群」的人做
对照，`date` 全部等于入群时间而不是处罚时间。

## 5. 错误与限制速查

| 现象 | 出处 | 含义 / 处理 |
|---|---|---|
| `400 user not found` | getChatMember | 查不到此人；按未知处理 |
| `USER_NOT_PARTICIPANT` | channels.getParticipant | 目标不在群；入群时间无解 |
| `BOT_METHOD_INVALID` | 各类 MTProto 方法 | bot 被禁止调用该方法（见第 6 节） |
| `FLOOD_WAIT_X` | MTProto | 请求过快，等 X 秒 |
| `429 retry_after` | Bot API | 同上 |
| 批量扫不满 | channels.getParticipants | 服务端约 1 万条上限；漏掉的人单查 |

## 6. bot 不可用的 MTProto 方法（已实测确认）

| 方法 | 本可用来做什么 | 结果 |
|---|---|---|
| `channels.getAdminLog` | 管理日志里的「成员加入」事件带时间 | `BOT_METHOD_INVALID` |
| `messages.getChatInviteImporters` | 邀请链接导入者列表（带日期） | `BOT_METHOD_INVALID` |
| `messages.getExportedChatInvites` | 导出群邀请链接 | `BOT_METHOD_INVALID` |

补充：`channels.getMessages`（曾想用被删消息反查 access_hash）已确认无用 ——
被删消息取回来没有 `from_id`、`users` 为空。

## 附：为什么还需要 chat_member

Bot API 的 `chat_member` 更新带加入/离开事件，但**只在实时推送**：它负责
新入群的实时记录；bot 拿到管理员权限之前就在群里的人没有事件可收，只能按
本文的办法回查。
