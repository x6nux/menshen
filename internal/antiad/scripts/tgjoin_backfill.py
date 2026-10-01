#!/usr/bin/env python3
"""列出某个群成员的入群时间，按 TSV 打到 stdout。由 bot 以子进程调用。

为什么需要它：Bot API 没有任何「入群时间」字段（getChatMember 只给
status/permissions/until_date），只有 MTProto 的 channels.getParticipants
带 ChannelParticipant.date（官方定义即 Date joined）。好在这一步**不需要
用户账号** —— 用 bot 自己的 token 就能登录 MTProto（auth.importBotAuthorization），
所以整个流程仍然是纯 bot。

两个坑，都在实测里踩过：
  1. 批量列表会截断。服务端给 channels.getParticipants 的翻页上限在一万条
     左右：5 万人的群只能扫到 ~9988 人；小群也会漏掉一部分（受限账号、
     Telegram 自己标记的账号等）。漏掉的人 joined_at 一直是 0。
  2. 受限成员（仍在群、只被限制部分权限）的记录类型是
     ChannelParticipantBanned，官方注释写的是 "When was the user banned"，
     很容易被当成限制时间跳过 —— 实测 left=False 的记录 date 就是入群时间
     （拿实时记录过入群时间的 40 多人交叉验证过）。

因此：先批量扫，再用 --unknowns 名单逐个补查（单查走会话缓存；不行就用
Bot API 拿名字，再按名字搜索成员列表 —— 搜索能越过批量列表的截断；受限
账号连搜索都不给时，用名单里那条留底消息把本人的 access_hash 抠出来再
单查一次）。已经退群/被踢的人没有任何接口能拿到入群时间，直接放弃。

环境变量：
  TG_BOT_TOKEN   必填，要用的那个 bot 的 token
  TG_API_ID      必填，客户端应用标识（my.telegram.org 申请）
  TG_API_HASH    必填，同上
  TG_SESSION_DIR 选填，会话文件目录（默认当前目录；持久化会话能缓存 peer）

参数：
  --chat <chat_id>       群 id（-100 开头的超群）
  --username <name>      群的公开用户名（有的话用它解析，最稳）
  --limit <n>            最多输出多少行（默认全部）
  --unknowns <file>      入群时间未知的名单（每行 `uid\t最新留底消息id`），
                         逐个补查
  --lookup-budget <sec>  逐个补查的时间预算（默认 180 秒）
  --max-lookups <n>      逐个补查的人数上限（默认 400）

输出：每行 `chat_id\tuser_id\t入群时间(unix)`，只包含能拿到 join date 的
成员：普通成员、管理员、仍在群的受限成员。
"""
import argparse
import asyncio
import json
import os
import sys
import time
import urllib.parse
import urllib.request


def parse_args():
    ap = argparse.ArgumentParser()
    ap.add_argument("--chat", type=int, required=True)
    ap.add_argument("--username", default="")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--unknowns", default="")
    ap.add_argument("--lookup-budget", type=float, default=180.0)
    ap.add_argument("--max-lookups", type=int, default=400)
    return ap.parse_args()


async def main():
    args = parse_args()
    token = os.environ.get("TG_BOT_TOKEN", "")
    api_id = os.environ.get("TG_API_ID", "")
    api_hash = os.environ.get("TG_API_HASH", "")
    if not token or not api_id or not api_hash:
        print("缺少 TG_BOT_TOKEN / TG_API_ID / TG_API_HASH", file=sys.stderr)
        return 2

    try:
        from telethon import TelegramClient
        from telethon.tl.functions.channels import (GetParticipantRequest,
                                                    GetParticipantsRequest)
        from telethon.tl.functions.messages import GetMessagesRequest
        from telethon.tl.types import (
            ChannelParticipant,
            ChannelParticipantAdmin,
            ChannelParticipantBanned,
            ChannelParticipantCreator,
            ChannelParticipantLeft,
            ChannelParticipantsSearch,
            InputPeerUser,
        )
    except Exception as e:  # noqa: BLE001
        print("需要 python3 + telethon：%s" % e, file=sys.stderr)
        return 3

    def join_ts(p):
        """从一条成员记录里取入群时间；拿不到语义正确的值就返回 0。"""
        if isinstance(p, (ChannelParticipant, ChannelParticipantAdmin)):
            return int(p.date.timestamp()) if hasattr(p.date, "timestamp") else int(p.date)
        if isinstance(p, ChannelParticipantBanned) and not getattr(p, "left", False):
            # 仍在群的受限成员：date 就是入群时间（见文件头说明）。
            return int(p.date.timestamp()) if hasattr(p.date, "timestamp") else int(p.date)
        return 0

    def puid(p):
        if hasattr(p, "user_id"):
            return p.user_id
        peer = getattr(p, "peer", None)
        return getattr(peer, "user_id", None) if peer else None

    def bot_api(method, params):
        req = urllib.request.Request(
            "https://api.telegram.org/bot%s/%s" % (token, method),
            data=urllib.parse.urlencode(params).encode())
        with urllib.request.urlopen(req, timeout=20) as r:
            return json.load(r)

    sess_dir = os.environ.get("TG_SESSION_DIR", ".")
    os.makedirs(sess_dir, exist_ok=True)
    # 会话按 bot 会话区分：同一个 bot 反复跑复用同一份，peer 缓存能攒下来
    sess = os.path.join(sess_dir, "bot_%s" % token.split(":", 1)[0])

    client = TelegramClient(sess, int(api_id), api_hash)
    try:
        await client.start(bot_token=token)
        # 把错过的更新拉一遍：里面带的 peer 会进实体缓存，私有群（没有公开
        # 用户名）才解析得出来。失败不影响后面的公开群解析。
        try:
            await client.catch_up()
        except Exception:  # noqa: BLE001
            pass

        entity = None
        if args.username:
            try:
                entity = await client.get_entity(args.username)
            except Exception as e:  # noqa: BLE001
                print("按用户名解析失败：%s" % e, file=sys.stderr)
        if entity is None:
            try:
                entity = await client.get_entity(args.chat)
            except Exception as e:  # noqa: BLE001
                print("按 id 解析失败（私有群需要会话里缓存过该群）：%s" % e, file=sys.stderr)
                return 4

        async def lookup_by_name(uid):
            """单查不行时：Bot API 拿名字 → 按名字搜索成员列表。"""
            try:
                r = bot_api("getChatMember", {"chat_id": args.chat, "user_id": uid})
            except Exception:  # noqa: BLE001
                return 0
            res = r.get("result") or {}
            # 已经不在群的人没有入群时间可拿，别浪费搜索。
            if res.get("status") not in ("member", "restricted", "administrator", "creator"):
                return 0
            u = res.get("user") or {}
            fn = (u.get("first_name") or "").strip()
            ln = (u.get("last_name") or "").strip()
            un = (u.get("username") or "").strip()
            names = []
            if fn:
                names.append(fn)
            if ln:
                names.append(ln)
            if fn and ln:
                names.append(fn + " " + ln)
            if un:
                names.append(un)
            for q in names:
                try:
                    r2 = await client(GetParticipantsRequest(
                        channel=entity, filter=ChannelParticipantsSearch(q=q),
                        offset=0, limit=200, hash=0))
                except Exception:  # noqa: BLE001
                    continue
                for p in r2.participants:
                    if puid(p) == uid:
                        ts = join_ts(p)
                        if ts:
                            return ts
            return 0

        n = same = skipped = 0
        seen = set()
        out = []
        async for u in client.iter_participants(entity, aggressive=True):
            n += 1
            p = getattr(u, "participant", None)
            ts = join_ts(p) if p is not None else 0
            if ts:
                out.append("%d\t%d\t%d" % (args.chat, u.id, ts))
                seen.add(u.id)
                same += 1
            else:
                skipped += 1
            if args.limit and len(out) >= args.limit:
                break

        # 逐个补查批量列表漏掉的人。名单每行 `uid\t最新留底消息id`。
        lookups = hits = 0
        if args.unknowns and os.path.exists(args.unknowns):
            deadline = time.monotonic() + max(0.0, args.lookup_budget)
            pending = []
            try:
                for line in open(args.unknowns):
                    line = line.strip()
                    if not line:
                        continue
                    parts = line.split("\t")
                    msg_id = int(parts[1]) if len(parts) > 1 and parts[1].strip() else 0
                    pending.append((int(parts[0]), msg_id))
            except ValueError:
                pending = []
            for uid, msg_id in pending:
                if lookups >= args.max_lookups or time.monotonic() >= deadline:
                    break
                if uid in seen:
                    continue
                lookups += 1
                ts = 0
                try:
                    r = await client(GetParticipantRequest(channel=entity, participant=uid))
                    ts = join_ts(r.participant)
                except Exception:  # noqa: BLE001
                    ts = 0
                if not ts:
                    try:
                        ts = await lookup_by_name(uid)
                    except Exception:  # noqa: BLE001
                        ts = 0
                if not ts and msg_id:
                    # 最后一条路：受限账号搜索不给、会话缓存里也没有他的
                    # access_hash。把他留下的一条消息取出来，响应的 users
                    # 里带着 access_hash，拿它再单查一次。
                    try:
                        r3 = await client(GetMessagesRequest(channel=entity, id=[msg_id]))
                        for u2 in (getattr(r3, "users", None) or []):
                            if getattr(u2, "id", 0) == uid:
                                r4 = await client(GetParticipantRequest(
                                    channel=entity,
                                    participant=InputPeerUser(u2.id, u2.access_hash)))
                                ts = join_ts(r4.participant)
                                break
                    except Exception:  # noqa: BLE001
                        ts = 0
                if ts:
                    out.append("%d\t%d\t%d" % (args.chat, uid, ts))
                    seen.add(uid)
                    hits += 1
                # 手轻一点：连续搜索容易连撞 FLOOD_WAIT。
                await asyncio.sleep(0.25)

        for line in out:
            print(line)
        print("成员 %d，拿到入群时间 %d，跳过 %d；逐个补查 %d 人，命中 %d" % (
            n, same, skipped, lookups, hits), file=sys.stderr)
        return 0
    finally:
        await client.disconnect()


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
