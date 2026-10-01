#!/usr/bin/env python3
"""列出某个群成员的入群时间，按 TSV 打到 stdout。由 bot 以子进程调用。

为什么需要它：Bot API 没有任何「入群时间」字段（getChatMember 只给
status/permissions/until_date），只有 MTProto 的 channels.getParticipants
带 ChannelParticipant.date（官方定义即 Date joined）。好在这一步**不需要
用户账号** —— 用 bot 自己的 token 就能登录 MTProto（auth.importBotAuthorization），
所以整个流程仍然是纯 bot。

环境变量：
  TG_BOT_TOKEN   必填，要用的那个 bot 的 token
  TG_API_ID      必填，客户端应用标识（my.telegram.org 申请）
  TG_API_HASH    必填，同上
  TG_SESSION_DIR 选填，会话文件目录（默认当前目录；持久化会话能缓存 peer）

参数：
  --chat <chat_id>       群 id（-100 开头的超群）
  --username <name>      群的公开用户名（有的话用它解析，最稳）
  --limit <n>            最多输出多少行（默认全部）

输出：每行 `chat_id\tuser_id\t入群时间(unix)`，只包含能拿到 join date 的
成员（普通成员与管理员有；群主、被封禁/已退群的人在 MTProto 里是另一类
记录，那个 date 不是入群时间，跳过）。
"""
import argparse
import asyncio
import os
import sys


def parse_args():
    ap = argparse.ArgumentParser()
    ap.add_argument("--chat", type=int, required=True)
    ap.add_argument("--username", default="")
    ap.add_argument("--limit", type=int, default=0)
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
        from telethon.tl.types import (
            ChannelParticipant,
            ChannelParticipantAdmin,
            ChannelParticipantBanned,
            ChannelParticipantCreator,
            ChannelParticipantLeft,
        )
    except Exception as e:  # noqa: BLE001
        print("需要 python3 + telethon：%s" % e, file=sys.stderr)
        return 3

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

        n = same = skipped = 0
        out = []
        async for u in client.iter_participants(entity, aggressive=True):
            n += 1
            p = getattr(u, "participant", None)
            if isinstance(p, (ChannelParticipant, ChannelParticipantAdmin)):
                ts = int(p.date.timestamp()) if hasattr(p.date, "timestamp") else int(p.date)
                out.append("%d\t%d\t%d" % (args.chat, u.id, ts))
                same += 1
            elif isinstance(p, (ChannelParticipantCreator, ChannelParticipantBanned,
                                 ChannelParticipantLeft)):
                skipped += 1
            else:
                skipped += 1
            if args.limit and len(out) >= args.limit:
                break
        for line in out:
            print(line)
        print("成员 %d，拿到入群时间 %d，跳过 %d" % (n, same, skipped), file=sys.stderr)
        return 0
    finally:
        await client.disconnect()


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
