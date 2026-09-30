package antiad

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 禁言被外部解除时重新施加 ----
//
// 现实场景：群里挂着入群验证机器人（nmbot 那类）。它验证前的限制、验证
// 通过后的「权限全开」都是 restrictChatMember —— 而权限是全量覆盖的：
// 它一次「全开」就把我们刚给的进群限制一起抹掉了。库里记录还在（人还在
// 限制里），Telegram 侧却已经能发言。
//
// 所以盯 chat_member 更新：当事人从「在群里但不能发言」变成「能发言」、
// 而我们名下还有没解除的限制时，按剩余时长重新施加一次。我们自己的解除
// （申诉、解禁码、人工放行）会先落一个短期标记，不会被这条规则弹回去。
//
// 只管禁言、不管封禁：把已被封禁的人解封几乎一定是人类管理员的决定
// （验证机器人不做这事），不该由机器人再封回去。

const (
	// reassertTTL 是「刚刚是我们自己解除的」标记时长。解除动作本身要发
	// TG 调用，更新回流有几秒延迟，三分钟足够覆盖。
	reassertTTL = 3 * time.Minute
	// reassertLimit 是同一个群、同一个人每小时最多重新施加几次。防的是
	// 与别的 bot 打起来（它解除、我们施加、它再解除……）。
	reassertLimit = 3
)

var (
	// justLifted 记下「这是我们主动解除的」：键 chat:uid。
	justLifted sync.Map // string -> time.Time
	// reasserts 记重新施加的时间点（限流用）：键 chat:uid。
	reasserts sync.Map // string -> []time.Time
)

func reassertKey(chatID, uid int64) string {
	return fmt.Sprintf("%d:%d", chatID, uid)
}

// NoteLifted 记下「这是我们主动解除的限制」，外部解除复查会跳过这个
// 群里的这个人一小段时间。解除路径（申诉、解禁码、人工放行、白名单）
// 都要调它。
func NoteLifted(chatID, uid int64) {
	justLifted.Store(reassertKey(chatID, uid), time.Now())
}

// canSpeak 报告这个成员状态是否代表「现在能发言」。
func canSpeak(m *tg.ChatMemberInfo) bool {
	if m == nil {
		return false
	}
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		// restricted 是「权限非默认满值」：can_send_messages 为真才算能说话。
		return m.CanSendMessages != nil && *m.CanSendMessages
	}
	return false // left / kicked
}

// activeMute 找我们名下还没解除的禁言，返回剩余时长（0 = 无限期）与说明。
func activeMute(b *core.Bot, chatID, uid int64) (time.Duration, string, bool) {
	// 进群资料限制：无限期，直到被解除。
	if rec, ok := loadJoinMute(b.Store, chatID, uid); ok {
		return 0, "进群资料审核：" + core.TruncateRunes(rec.Reason, 80), true
	}
	// 消息级禁言：看还没标记解除的流水，按原时长算剩余。
	minutes := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
	var at int64
	var reason string
	if err := b.Store.Read.QueryRow(`SELECT created_at, reason FROM antiad_log
		WHERE bot_id=? AND chat_id=? AND user_id=? AND lifted_at=0
		AND action IN ('muted','deleted_muted') ORDER BY id DESC LIMIT 1`,
		b.BotID(), chatID, uid).Scan(&at, &reason); err != nil {
		return 0, "", false
	}
	desc := "消息判定禁言：" + core.TruncateRunes(reason, 80)
	if minutes <= 0 {
		return 0, desc, true // 永久禁言
	}
	left := at + minutes*60 - time.Now().Unix()
	if left <= 0 {
		return 0, "", false // 限时禁言已经到期
	}
	return time.Duration(left) * time.Second, desc, true
}

// reassertAllowed 做限流：同一个人每小时最多重新施加 reassertLimit 次。
func reassertAllowed(chatID, uid int64) bool {
	key := reassertKey(chatID, uid)
	now := time.Now()
	cut := now.Add(-time.Hour)
	var kept []time.Time
	if v, ok := reasserts.Load(key); ok {
		if list, ok := v.([]time.Time); ok {
			for _, t := range list {
				if t.After(cut) {
					kept = append(kept, t)
				}
			}
		}
	}
	if len(kept) >= reassertLimit {
		reasserts.Store(key, kept)
		return false
	}
	reasserts.Store(key, append(kept, now))
	return true
}

// reassertMute 处理「我们的禁言被外部解除」：重新施加，并留一行日志。
func reassertMute(b *core.Bot, conf store.BotChat, cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil {
		return
	}
	u := cu.NewChatMember.User
	if u == nil || u.ID <= 0 || u.ID == b.BotID() || conf.Dryrun {
		return
	}
	// 只处理「本来在群里、且本不该能发言」的那次跃迁：真进群由冷判定与
	// 联封进群拦截负责（那是另一条路，old 还没进群）。
	if cu.OldChatMember == nil || !isMemberStatus(cu.OldChatMember.Status) {
		return
	}
	if canSpeak(cu.OldChatMember) || !canSpeak(cu.NewChatMember) {
		return
	}
	chatID := cu.Chat.ID
	if v, ok := justLifted.Load(reassertKey(chatID, u.ID)); ok {
		if t, ok := v.(time.Time); ok && time.Since(t) < reassertTTL {
			return // 我们自己刚解除的，不反弹
		}
	}
	if b.Cache.Snap().Whitelisted(b.BotID(), chatID, u.ID, time.Now().Unix()) {
		return // 管理员明确放行
	}
	left, why, ok := activeMute(b, chatID, u.ID)
	if !ok {
		return // 库里没有我们的限制：这限制是别人的事
	}
	if !reassertAllowed(chatID, u.ID) {
		slog.Warn("反广告：禁言被反复外部解除，本轮不再重新施加",
			"chat", chatID, "uid", u.ID, "限制", why)
		return
	}
	if ok2, desc := MuteSender(b, chatID, u.ID, left); !ok2 {
		slog.Warn("反广告：重新施加禁言失败", "chat", chatID, "uid", u.ID, "tg", desc)
		return
	}
	slog.Info("反广告：禁言被外部解除，已重新施加",
		"chat", chatID, "uid", u.ID, "剩余", left.String(), "限制", why)
}

// ReassertActiveMutes 周期性核对「我们名下还在生效的进群限制」有没有被外部
// 解除（验证机器人、另一个 bot 的权限覆盖都会造成这个），有就重新施加。
//
// 事件驱动那条路（chat_member）在更新丢失时什么都看不到：bot 重启、TG
// 掉推、或者解除动作发生在我们上线之前。这一轮按 join_mutes 逐条问一次
// 真实状态，把漏掉的补上。数量按 join_mutes 行数算，通常几十条。
func ReassertActiveMutes(sh *core.Shared) {
	if sh.Reg == nil {
		return
	}
	rows, err := sh.Store.Read.Query(
		`SELECT bot_id,chat_id,user_id FROM join_mutes LIMIT 500`)
	if err != nil {
		slog.Error("禁言复查：读取进群限制失败", "err", err)
		return
	}
	type item struct{ botID, chatID, uid int64 }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.botID, &it.chatID, &it.uid) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	if len(items) == 0 {
		return
	}

	now := time.Now().Unix()
	for _, it := range items {
		rec, live := sh.Reg.LookupID(it.botID)
		if !live {
			continue
		}
		conf, ok := rec.Cache.Snap().ChatConf(it.botID, it.chatID)
		if !ok || !conf.Enabled || conf.Dryrun {
			continue
		}
		if rec.Cache.Snap().Whitelisted(it.botID, it.chatID, it.uid, now) {
			continue
		}
		if v, ok := justLifted.Load(reassertKey(it.chatID, it.uid)); ok {
			if t, ok := v.(time.Time); ok && time.Since(t) < reassertTTL {
				continue
			}
		}
		st := chatMemberState([]*core.Bot{rec}, it.chatID, it.uid)
		if st == nil || !st.Member || !st.Muted {
			continue // 已经不在了，或者本来就不能发言（还在限制里）
		}
		if !reassertAllowed(it.chatID, it.uid) {
			continue
		}
		if ok2, desc := MuteSender(rec, it.chatID, it.uid, 0); !ok2 {
			slog.Warn("禁言复查：重新施加失败",
				"chat", it.chatID, "uid", it.uid, "tg", desc)
			continue
		}
		slog.Info("禁言复查：进群限制被外部解除，已重新施加",
			"chat", it.chatID, "uid", it.uid)
	}
}
