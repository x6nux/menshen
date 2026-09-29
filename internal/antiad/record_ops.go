package antiad

import (
	"fmt"
	"log/slog"
	"strconv"
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

// MarkPenaltiesLifted 把某人在某群尚未标记解除的处罚流水标成已解除。
//
// 申诉入口按「仍在生效的处罚」决定要不要给入口。限时禁言靠时间窗自然过期，
// 而永久禁言不会——人工解除、申诉撤销、解禁码兑换都必须落这个标记，
// 否则入口会永远显示「你还在限制中」。
func MarkPenaltiesLifted(b *core.Bot, chatID, uid int64) {
	if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET lifted_at=?
		WHERE bot_id=? AND chat_id=? AND user_id=? AND lifted_at=0
		AND action IN ('deleted_muted','muted','deleted_banned','banned')`,
		time.Now().Unix(), b.BotID(), chatID, uid); err != nil {
		slog.Error("反广告：标记处罚已解除失败", "chat", chatID, "uid", uid, "err", err)
	}
}
