package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// ---- 私聊全解（/uban）----
//
// /uad 是群管理员在一个群里「这次算了」；/uban 是服务管理员在私聊里把一个
// 人**在自己能管的所有群里**一次放回来：撤联合封禁名单、逐群按真实状态
// 解封 / 解禁言、清掉处罚记录与进群限制。
//
// **不加白名单**：清的是「会把人再封回去」的既有记录（名单会在发言/进群时
// 自动执行，进群限制与未解除的处罚流水会被复查任务重新施加），之后的发言
// 照常进入判定 —— 再发广告照样处置。
//
// 范围按身份分：主管理员 = 全部 bot 的群 + 全局组 + 所有人的专属组；
// 次级管理员 = 自己名下 bot 的群 + 全局组 + 自己的专属组（与 /uad 的
// 「主/次管理员连全局组一起解」同口径）。

const ubanUsage = "用法：<code>/uban &lt;user_id&gt;</code> 或 <code>/uban @用户名</code>" +
	"（频道填 -100 开头的频道 ID）。\n" +
	"在你能管的所有群里解除此人的封禁与禁言，撤掉联合封禁名单、清掉处罚记录与" +
	"进群限制，避免被再次自动封禁。之后的发言照常判定。"

// UbanResult 是一次全解的结果，群用可读名表示。
type UbanResult struct {
	Gban     string   // 撤掉的名单 / 进群限制范围（AdminLiftGban 的说明）
	Checked  int      // 实际问到状态（或尝试过）的群数
	Unbanned []string // 解封的群
	Unmuted  []string // 解除禁言的群
	Failed   []string // 被限制但没解开的群（多半是 bot 权限不够）
}

// UbanEverywhere 在 actor 能管的所有群里全解 uid。跑的是一串 TG 调用，
// 调用方应放在独立 goroutine 上。
func UbanEverywhere(sh *core.Shared, actor, uid int64) UbanResult {
	var r UbanResult
	// 先撤名单：不撤的话人一发言 / 一进群又会被名单禁回去。
	r.Gban = AdminLiftGban(sh, actor, uid)

	snap := sh.Cache.Snap()
	main := sh.IsMain(actor)
	mine := func(botID int64) bool {
		rec := snap.Bots[botID]
		return rec != nil && !rec.IsMain && (main || rec.OwnerID == actor)
	}
	type target struct {
		chatID int64
		bots   []*core.Bot
	}
	var targets []target
	eachActiveChat(sh, mine, func(bots []*core.Bot, chatID int64) {
		targets = append(targets, target{chatID, bots})
	})
	slices.SortFunc(targets, func(x, y target) int { return int(x.chatID - y.chatID) })

	for _, t := range targets {
		conf, _ := snap.ChatConf(t.bots[0].BotID(), t.chatID)
		name := chatDisplayName(conf)
		r.Checked++
		switch ubanInChat(sh, t.bots, t.chatID, uid) {
		case "unbanned":
			r.Unbanned = append(r.Unbanned, name)
		case "unmuted":
			r.Unmuted = append(r.Unmuted, name)
		case "failed":
			r.Failed = append(r.Failed, name)
		}
	}
	unbanGateClear(sh, uid)
	slog.Info("全解完成", "uid", uid, "by", actor, "名单", r.Gban, "核对群数", r.Checked,
		"解封", len(r.Unbanned), "解禁", len(r.Unmuted), "失败", len(r.Failed))
	return r
}

// ubanInChat 在一个群里按真实状态解除限制，并清掉这个群里我们名下的记录。
// 返回 unbanned / unmuted / failed，没被限制时返回空串。
//
// 只对确实被限制的人发「权限全开」：对没被禁言的人那等于提权到群默认之上。
// 解封带 only_if_banned（见 Unban），对没被封的人无副作用。
func ubanInChat(sh *core.Shared, bots []*core.Bot, chatID, uid int64) string {
	// 先落「主动解除」标记：chat_member 更新回流有延迟，复查别把它弹回去。
	NoteLifted(sh, chatID, uid)
	// 未解除的处罚流水也要标掉：人在 TG 侧可能早已能发言（限时到期、被验证
	// 机器人放开），留着它复查任务会把人再禁回去。进群限制已由 AdminLiftGban
	// 按同一范围清过。
	defer func() {
		for _, b := range bots {
			MarkPenaltiesLifted(b, chatID, uid)
		}
	}()

	if uid < 0 {
		// 频道身份没有成员状态可问，当初是 banChatSenderChat 封的。
		for _, b := range bots {
			if ok, _ := Unban(b, chatID, uid); ok {
				return "unbanned"
			}
		}
		return ""
	}
	st := chatMemberState(bots, chatID, uid)
	switch {
	case st == nil:
		return "" // 问不到（bot 不在群里 / 无权限），也就解不了
	case st.Status == "kicked":
		for _, b := range bots {
			if ok, _ := Unban(b, chatID, uid); ok {
				return "unbanned"
			}
		}
		return "failed"
	case st.Muted:
		for _, b := range bots {
			if ok, _ := LiftMute(b, chatID, uid); ok {
				return "unmuted"
			}
		}
		return "failed"
	}
	return ""
}

// HandleUbanCommand 处理管理员私聊里的 /uban。调用方已确认是服务管理员。
func HandleUbanCommand(b *core.Bot, m *tg.Message, arg string) {
	dm, actor := m.Chat.ID, m.From.ID
	if !b.IsStaff(actor) {
		return
	}
	uid, ok := resolveUIDArg(b, arg)
	if !ok {
		b.Send(dm, ubanUsage, nil)
		return
	}
	if !b.AdLimits.Allow(fmt.Sprintf("uban:%d", actor),
		b.Cache.Snap().SettingInt("antiad_cmd_rpm", 3)) {
		b.Send(dm, "操作太频繁，请稍后再试。", nil)
		return
	}
	progress := b.SendGetID(dm, "⏳ 正在你能管的所有群里解除 "+userLink(uid)+
		" 的限制……", nil)
	// 一串 TG 调用（每群至少一次 getChatMember），而更新处理是串行的。
	go func() {
		r := UbanEverywhere(b.Shared, actor, uid)
		b.EditOrSend(dm, progress, ubanSummary(uid, r), nil)
		if uid > 0 && (len(r.Unbanned)+len(r.Unmuted) > 0 || r.Gban != "") {
			// 没和这个 bot 私聊过的人发不出去，忽略结果。
			b.Send(uid, "✅ <b>管理员已解除对你的限制</b>\n\n你现在可以在群里正常发言了。\n\n"+
				"<i>之后的发言仍会照常检查；如果账号资料里还有推广、引流内容，请尽快改掉。</i>", nil)
		}
	}()
}

// ubanSummary 渲染给管理员看的结果（群名做 HTML 转义）。
func ubanSummary(uid int64, r UbanResult) string {
	esc := func(list []string) string {
		out := make([]string, len(list))
		for i, s := range list {
			out[i] = html.EscapeString(s)
		}
		return strings.Join(out, "、")
	}
	var sb strings.Builder
	sb.WriteString("✅ <b>全解完成</b>：" + userLink(uid) + "\n")
	fmt.Fprintf(&sb, "\n核对了 %d 个群。\n", r.Checked)
	if r.Gban != "" {
		sb.WriteString("撤除：" + html.EscapeString(r.Gban) + "\n")
	}
	if len(r.Unbanned) > 0 {
		sb.WriteString("解封：" + esc(r.Unbanned) + "\n")
	}
	if len(r.Unmuted) > 0 {
		sb.WriteString("解除禁言：" + esc(r.Unmuted) + "\n")
	}
	if len(r.Failed) > 0 {
		sb.WriteString("⚠️ 没解开（多半是 bot 缺少封禁权限）：" + esc(r.Failed) + "\n")
	}
	if r.Gban == "" && len(r.Unbanned)+len(r.Unmuted)+len(r.Failed) == 0 {
		sb.WriteString("此人在这些群里没有被限制。\n")
	}
	sb.WriteString("\n处罚记录与进群限制已清除；之后的发言照常判定（没有加白名单）。")
	return sb.String()
}
