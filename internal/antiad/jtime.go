package antiad

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

const jtimeUsage = "用法：直接发 <code>/jtime</code> 查你自己，" +
	"回复某人的消息查对方，或发 <code>/jtime &lt;user_id&gt;</code> / " +
	"<code>/jtime @用户名</code>。\n显示该用户在本群的入群时间。"

// joinAgeLabel 把入群时长渲染成可读文案。
func joinAgeLabel(sec int64) string {
	switch {
	case sec < 60:
		return "刚刚"
	case sec < 3600:
		return fmt.Sprintf("%d 分钟", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%d 小时", sec/3600)
	case sec < 86400*90:
		return fmt.Sprintf("%d 天", sec/86400)
	case sec < 86400*730:
		return fmt.Sprintf("%d 个月", sec/(86400*30))
	default:
		return fmt.Sprintf("%d 年", sec/(86400*365))
	}
}

// HandleJtimeCommand 显示某人在本群的入群时间。
//
// 查询对象：回复某人的消息 = 对方；带 user_id/@用户名 = 那人；什么都不带 =
// 发送者自己。
//
// 只读命令：不调用模型、不做处置，对所有人开放（与 /check 同），但仍按
// 发起人限频 —— 结果是一条发到群里的卡片，刷屏会占用群消息。
// 命令消息立即删除（与 /ban、/uad 一致），结果卡片按 antiad_alert_ttl
// 延迟撤回。
//
// 数据来源就是 group_members.joined_at：新入群靠 chat_member 事件实时记，
// bot 拿到管理员权限之前就在群里的人靠 MTProto 补全（Mini App 的
// “补全历史入群时间”或拿到权限时的自动补全）。两样都没有时如实说未知，
// 顺带给出首见时间作为至少待到此刻的下界。
func HandleJtimeCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	chatID := conf.ChatID
	snap := b.Cache.Snap()
	ttl := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) { groupNotice(b, chatID, text, nil, ttl) }

	if !b.AdLimits.Allow(fmt.Sprintf("jtime:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		reply("操作太频繁，请稍后再试。")
		return
	}
	// 命令本身立即删除（与 /ban、/uad 一致）：它只是触发查询，留在群里没意义。
	// 结果卡片仍按 antiad_alert_ttl 延迟撤回。
	b.TG.Call("deleteMessage", map[string]any{"chat_id": chatID, "message_id": m.MessageID})

	var uid int64
	var who *tg.TGUser
	switch {
	case m.ReplyToMessage != nil && m.ReplyToMessage.From != nil:
		// 以频道身份或访客 bot 发的，查的是实际发言者（与 /ban 同口径）。
		who = senderOf(m.ReplyToMessage)
		uid = who.ID
	case strings.TrimSpace(arg) != "":
		id, ok := resolveUIDArg(b, arg)
		if !ok {
			reply(jtimeUsage)
			return
		}
		uid = id
	default:
		// 没回复、也没指定人：查发送者自己。
		who = senderOf(m)
		uid = who.ID
	}
	if uid < 0 {
		reply("频道身份没有入群时间（频道不是群成员）。")
		return
	}

	// 名字：回复形态直接有；参数形态查一次资料（带缓存，查不到就只显示 ID）。
	label := userLink(uid)
	if who != nil {
		if n := senderName(who); n != "" && n != strconv.FormatInt(uid, 10) {
			label = escText(n) + " " + label
		}
	} else {
		e := userInfo(b, uid)
		n := strings.TrimSpace(e.firstName + " " + e.lastName)
		if e.username != "" {
			n = strings.TrimSpace(n + " @" + e.username)
		}
		if n != "" {
			label = escText(n) + " " + label
		}
	}

	loc := snap.Location()
	fmtTime := func(unix int64) string {
		return time.Unix(unix, 0).In(loc).Format("2006-01-02 15:04")
	}

	gm, ok := loadMember(b.Store, chatID, uid)
	// 库里没有就按需实时查一次（几秒），查到即入库，之后都是秒回。
	if !ok || gm.JoinedAt == 0 {
		if ts, found := ResolveJoinTime(b, chatID, uid); found {
			if !ok {
				gm = groupMember{ChatID: chatID, UserID: uid, Known: true}
			}
			gm.JoinedAt = ts
			ok = true
		}
	}
	var sb strings.Builder
	sb.WriteString("🗂 <b>入群时间</b>\n")
	sb.WriteString(label + "\n")
	switch {
	case ok && gm.JoinedAt > 0:
		ago := time.Now().Unix() - gm.JoinedAt
		sb.WriteString("入群：<b>" + fmtTime(gm.JoinedAt) + "</b>（" + joinAgeLabel(ago) + "前）\n")
	case ok && gm.FirstSeen > 0:
		sb.WriteString("入群：<b>未知</b>\n")
		sb.WriteString("bot 拿到管理员权限之前他就在群里了。最早能确认的是首见时间：" +
			fmtTime(gm.FirstSeen) + " 时他已经在群。\n")
	default:
		sb.WriteString("入群：<b>未知</b>\n")
		sb.WriteString("本群没有他的记录（没发过言，也没有入群事件）。\n")
	}
	if ok && gm.MsgCount > 0 {
		sb.WriteString("发言 " + strconv.FormatInt(gm.MsgCount, 10) + " 条")
		if gm.LastMsgAt > 0 {
			sb.WriteString(" ｜ 最近 " + fmtTime(gm.LastMsgAt))
		}
		sb.WriteString("\n")
	}
	if !ok || gm.JoinedAt == 0 {
		sb.WriteString("<i>管理员可在 Mini App 群组卡片点「补全历史入群时间」重试。</i>")
	}
	reply(strings.TrimRight(sb.String(), "\n"))
}
