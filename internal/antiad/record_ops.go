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

// 配置台（Mini App）的记录操作入口：把群内命令（/check、/ban、/white）
// 里「对某条记录/某人」的动作提出来，按流水记录直接调用。
// 群内提示一律走 groupNotice / sendGroup（尊重群内静默开关），
// 结果照常进管理员私聊汇总，与群内命令的可见性保持一致。

// ReviewByRecord 按流水记录发起复查（等同群内 /check <uid>）。
// 该用户在本群没有留底时返回 false：调用方直接提示操作者，
// 不惊动群里——那是 /check 命令路径里才需要的提示。
func ReviewByRecord(b *core.Bot, conf store.BotChat, chatID, uid, byUID int64) bool {
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages
		WHERE chat_id=? AND user_id=?`, chatID, uid).Scan(&n)
	if n == 0 {
		return false
	}
	// 复查的限频与结果展示都以发起人为锚：From 是操作者本人。
	m := &tg.Message{Chat: &tg.Chat{ID: chatID}, From: &tg.TGUser{ID: byUID}}
	HandleAdCommand(b, conf, m, strconv.FormatInt(uid, 10))
	return true
}

// ManualMarkByRecord 按流水记录人工标记广告并按本群处罚方式处置
// （等同群内回复 /ban，不经 AI）。返回处置说明（ApplyAction 的失败
// 说明），空串表示全部成功。原文取流水里留的那份，消息可能已被删。
func ManualMarkByRecord(b *core.Bot, conf store.BotChat,
	chatID, msgID, uid int64, text string, byUID int64) string {

	snap := b.Cache.Snap()
	v := adVerdict{
		IsAd: true, Confidence: 1, Kind: "manual",
		Decider: "manual",
		Reason:  fmt.Sprintf("由管理员 %d 在配置台人工标记为广告", byUID),
	}
	// 与 /ban 同一档：判断已由人做出，直接取最高档，禁言与否仍服从
	// 本群的处罚方式。
	act := withPunish(adAction{Delete: true, Mute: true, Alert: true, Name: "deleted_muted"},
		snap.BanMode(conf))
	tgt := &tg.Message{Chat: &tg.Chat{ID: chatID}, From: &tg.TGUser{ID: uid},
		MessageID: msgID, Text: text}

	note := ApplyAction(b, tgt, act, conf.Dryrun)
	logID := logAd(b, tgt, v, logAction(act, conf.Dryrun), note)
	if !conf.Dryrun {
		BumpAdHits(b, chatID, uid, 1)
		maybeGban(b, chatID, uid, "人工标记："+core.TruncateRunes(text, 60))
	}
	alert, kb := renderAdAlertBrief(b, tgt, v, act, note, logID, conf.Dryrun)
	groupNotice(b, chatID, "🖐 <b>人工标记</b>\n"+alert, kb,
		time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300))*time.Second)
	return note
}

// LiftMute 解除某群对某人的禁言，并清掉冷判定限制记录、撤回群内通知。
// 申诉自动解除与配置台的记录操作共用这一条路径。
//
// TG 解除失败时提前返回、**不**清 join_mutes：禁言实际还在，
// 清了记录会让申诉流程以为限制已经不存在。
func LiftMute(b *core.Bot, chatID, uid int64) (bool, string) {
	// 先落标记再解除：解除要发 TG 调用，chat_member 更新回流有几秒延迟，
	// 落在这里才能保证「外部解除复查」不会把这次主动解除当成被抹掉。
	NoteLifted(chatID, uid)
	ok, desc := Unmute(b, chatID, uid)
	if !ok {
		return false, desc
	}
	if rec, found := loadJoinMute(b.Store, chatID, uid); found {
		dropJoinMute(b, chatID, uid)
		if rec.NoticeMsg != 0 {
			b.TG.Call("deleteMessage", map[string]any{
				"chat_id": chatID, "message_id": rec.NoticeMsg})
		}
	}
	MarkPenaltiesLifted(b, chatID, uid)
	return true, ""
}

// appendNote 在理由后面追加一句，保持原有理由在前。
func appendNote(old, add string) string {
	old = strings.TrimSpace(old)
	if old == "" {
		return add
	}
	return old + "；" + add
}

// ReleaseUser 解封/解禁言一个人并清掉本群的限制记录 —— 与「误判」不同，
// **判定维持不变**：动作标签、样本池、历史命中与内容哈希都不动，只把还在
// 生效的限制撤掉（管理员确认过这个人现在可以放回来，但当初判得没错）。
//
// 记录上若是封禁就走解封，否则解禁言；两步都试，被封禁的人不在群里、解禁言
// 那一步本来就会失败，失败只记日志。在本群生效的联合封禁也一并解除（见
// LiftGbanForChat）—— 否则人一发言又被名单禁回去，等于没解。
// actor 是执行人：服务管理员（主/次）连全局组一起解除，群管理员只解除
// 本群所属的专属组。返回给管理员看的一句结果。
func ReleaseUser(b *core.Bot, r AdLogRow, actor int64) string {
	name := strings.TrimPrefix(r.Action, "dryrun:")
	ban := name == "banned" || name == "deleted_banned" || name == "gban_banned"

	// 无论记录上写的是什么，两步都试一遍：记录可能只是「消息级禁言」，而
	// 人同时被别处封禁过；反过来也一样。only_if_banned 保证不会把没被封的
	// 人踢出去。
	var did []string
	if ok, _ := Unban(b, r.ChatID, r.UserID); ok && ban {
		did = append(did, "解封")
	}
	if ok, _ := LiftMute(b, r.ChatID, r.UserID); ok {
		did = append(did, "解除禁言")
	}
	if len(did) == 0 {
		// 两步都没生效：多半本来就没有生效中的 TG 限制。记录照样清掉，
		// 免得申诉页一直显示「你还在限制中」。
		MarkPenaltiesLifted(b, r.ChatID, r.UserID)
		did = append(did, "清除记录")
	}
	if s := LiftGbanForChat(b.Shared, b.BotID(), r.ChatID, r.UserID,
		b.IsStaff(actor)); s != "" {
		did = append(did, "联合封禁（"+s+"）")
	}
	UpdateAdLog(b, r.ID, r.Action,
		appendNote(r.Reason, "管理员解封（判定维持）："+strings.Join(did, "、")))
	slog.Info("反广告：管理员解封（判定维持）", "log", r.ID,
		"chat", r.ChatID, "uid", r.UserID, "动作", strings.Join(did, "、"))
	return strings.Join(did, "、")
}

// ReleaseUserInChat 面板「生效中的限制」解除按钮的实现：封禁与禁言都试
// 一遍，把「个人简介限制」记录与在本群生效的联合封禁一并清掉。
//
// 记录卡片上的 ReleaseUser 有具体流水行可依托（按动作选解封/解禁言）；
// 这里只有 (群, 人)，所以两个动作都做：unban 带 only_if_banned，不会把
// 没被封的人踢出去；LiftMute 成功时本来就清资料限制，只有它失败（人已
// 不在群）时补一次，免得记录留着把复查任务引回来。
// actor 是执行人（决定联合封禁的解除范围），与 ReleaseUser 同口径。
func ReleaseUserInChat(b *core.Bot, chatID, uid, actor int64) (bool, string) {
	ok1, _ := LiftMute(b, chatID, uid)
	ok2, _ := Unban(b, chatID, uid)
	gban := LiftGbanForChat(b.Shared, b.BotID(), chatID, uid, b.IsStaff(actor))
	if !ok1 && !ok2 && gban == "" {
		return false, "解除失败（可能已不在群里）"
	}
	if _, found := loadJoinMute(b.Store, chatID, uid); found {
		dropJoinMute(b, chatID, uid)
	}
	note := "已解除"
	if gban != "" {
		note += "（联合封禁：" + gban + "）"
	}
	return true, note
}

// MarkPenaltiesLifted 把某人在某群尚未标记解除的处罚流水标成已解除。
//
// 申诉入口按「仍在生效的处罚」决定要不要给入口。限时禁言靠时间窗自然过期，
// 而永久禁言不会——人工解除、申诉撤销、解禁码兑换都必须落这个标记，
// 否则入口会永远显示「你还在限制中」。
func MarkPenaltiesLifted(b *core.Bot, chatID, uid int64) {
	if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET lifted_at=?
		WHERE bot_id=? AND chat_id=? AND user_id=? AND lifted_at=0
		AND action IN ('deleted_muted','muted','deleted_banned','banned',
			'gban_muted','gban_banned')`,
		time.Now().Unix(), b.BotID(), chatID, uid); err != nil {
		slog.Error("反广告：标记处罚已解除失败", "chat", chatID, "uid", uid, "err", err)
	}
}
