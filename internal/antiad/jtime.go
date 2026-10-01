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

const jtimeUsage = "用法：回复某人的消息发 <code>/jtime</code>，" +
	"或直接发 <code>/jtime &lt;user_id&gt;</code> / <code>/jtime @用户名</code>。\n" +
	"显示该用户在本群的入群时间。"

// joinAgeLabel 把「入群多久了」渲染成人话。
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
// 只读命令：不花钱、不动手，对所有人开放（与 /check 同），但仍按发起人
// 限频 —— 结果是一条发到群里的卡片，刷起来会刷屏。
// 命令消息与结果卡片都按 antiad_alert_ttl 延迟撤回，不留痕。
//
// 数据来源就是 group_members.joined_at：新入群靠 chat_member 事件实时记，
// bot 拿到管理员权限之前就在群里的人靠 MTProto 补全（Mini App 的
// 「补全历史入群时间」或拿到权限时的自动补全）。两样都没有时如实说未知，
// 顺带给出首见时间当「至少待到这时」的下界。
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
	// 命令与结果一起延迟撤回：立刻删命令看着像消息被吃掉，也和卡片对不上。
	// 两样按同一个 ttl 挂进 alert_cleanup，扫到点一起收走；ttl=0 则都不撤。
	scheduleAlertCleanup(b, chatID, m.MessageID, ttl)

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
		reply(jtimeUsage)
		return
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
