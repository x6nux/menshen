package antiad

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 群内解封（/uad）----
//
// 群管理员在群里一句话就能把一个人放回来：解除禁言/封禁、清掉反广告的
// 处罚记录，并给 24 小时白名单缓冲（期满恢复检查）。与 /white 的区别是
// 它是**一次性**放行：白名单永久有效、等于对此人关掉反广告；/uad 只是
// 「这次算了」，之后照常判定。
//
// 记录必须一起清掉：不清的话，接下来任何一个动作都可能把他按回去 ——
// 进群冷判定重新读资料、被外部解除时的复查（见 reassert.go）、申诉页
// 还会显示「你还在限制中」。

const uadUsage = "用法：回复某人的消息发 <code>/uad</code>，" +
	"或发 <code>/uad &lt;user_id&gt;</code>（频道填 -100 开头的频道 ID）。\n" +
	"会解除此人在本群的禁言或封禁，清掉反广告处罚记录，" +
	"并给他 24 小时白名单缓冲（期满恢复判定）。"

// uadGrace 是解封后给的白名单缓冲时长：与申诉通过后的口径一致。
const uadGrace = 24 * time.Hour

// HandleUadCommand 处理群内 /uad。与 /ban、/white 同一道门：
// 解封等于撤掉机器人做的处置，只有群管理员及以上能用，非授权者静默忽略。
func HandleUadCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	// 命令本身不该留在群里（与 /ban 一致）。删不掉也不影响后续。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	ttl := time.Duration(b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) {
		groupNotice(b, conf.ChatID, text, nil, ttl)
	}

	var uid int64
	switch {
	case m.ReplyToMessage != nil && m.ReplyToMessage.From != nil:
		// 以频道身份或访客 bot 发的，处置落在实际发言者身上（与 /ban 同）。
		uid = senderOf(m.ReplyToMessage).ID
	case arg != "":
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id == 0 {
			reply(uadUsage)
			return
		}
		uid = id
	default:
		reply(uadUsage)
		return
	}

	snap := b.Cache.Snap()
	if !b.AdLimits.Allow(fmt.Sprintf("uad:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		reply("操作太频繁，请稍后再试。")
		return
	}

	// 封禁与禁言是两回事，两样都要撤：
	//   - unbanChatMember 必须带 only_if_banned：不带的话语义是「先踢出群
	//     再解封」，对没被封的人等于把他踢出去。
	//   - Unmute 是十项权限全开。已被封禁的人不在群里，这一步会失败，
	//     属正常（他重新进群时按新人处理）。
	banOK, banDesc := Unban(b, conf.ChatID, uid)
	muteOK, muteDesc := LiftMute(b, conf.ChatID, uid)

	// 白名单缓冲：不写的话，他重新进群时冷判定会照着同一份资料再限制一次，
	// 管理员看到的就是「解封没生效」。24 小时后自动失效，不是永久放行。
	if err := AddWhitelist(b.Shared, b.BotID(), conf.ChatID, uid, uadGrace, "uad", m.From.ID); err != nil {
		reply("解除成功，但写入白名单缓冲失败：" + core.TruncateRunes(err.Error(), 60))
		return
	}
	unbanGateClear(b.Shared, uid)

	if !banOK && !muteOK {
		// 两样都没做成（多半是 bot 没有封禁权限），如实告诉操作者。
		msg := strings.TrimSpace(strings.Join([]string{banDesc, muteDesc}, "；"))
		reply("⚠️ 记录已清除，但解除 TG 限制失败：" + htmlEscapeShort(msg))
		return
	}
	reply("✅ 已解除 " + userLink(uid) + " 在本群的限制：" +
		"清掉了反广告处罚记录，并给了 " + uadHoursLabel() +
		"白名单缓冲（期满恢复判定）。")

	// 私聊通知本人：他在群里看到的只是一条会自动撤回的提示。
	// 没和 bot 私聊过的人发不出去，忽略结果。
	b.Send(uid, "✅ <b>群管理员已解除对你的限制</b>\n\n"+
		"你现在可以在群里正常发言了。\n\n"+
		"<i>"+uadHoursLabel()+"内不会再被反广告检查；"+
		"如果账号资料或发言里还有推广、引流内容，请趁这段时间改掉。</i>", nil)
	slog.Info("反广告：群管理员解封", "chat", conf.ChatID, "uid", uid,
		"by", m.From.ID)
}

// uadHoursLabel 渲染缓冲时长（与申诉提示同一句式）。
func uadHoursLabel() string {
	return fmt.Sprintf("%d 小时", int(uadGrace/time.Hour))
}

// htmlEscapeShort 把 TG 报错压成一行给操作者看（不需要完整 HTML 转义：
// 群内提示走 groupNotice，那里会做转义）。
func htmlEscapeShort(s string) string {
	return core.TruncateRunes(strings.TrimSpace(s), 120)
}
