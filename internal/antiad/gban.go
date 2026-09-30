package antiad

import (
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// gbanFanout 限制联合封禁并发执行的路数。
//
// 一次联合封禁要对「所有 bot × 所有生效群」各发一次 banChatMember，
// 规模上去之后是几百个请求。不限并发会在几百毫秒内全部打出去，
// 直接撞上 Telegram 对 bot 的全局速率限制，连正常的业务消息一起发不出。
const gbanFanout = 4

// gbanSem 是进程级的执行闸。每个调用各拿一份信号量的话，并发的命中叠加
// 起来对 TG 的实际并发就是无上限的（刷屏高峰里命中会接连发生）。
var gbanSem = make(chan struct{}, gbanFanout)

// gbanEnabled 报告联合封禁总开关。它是全局的，只有主管理员能动 ——
// 一次操作会波及所有接入方的所有群。
func GbanEnabled(sh *core.Shared) bool {
	return sh.Cache.Snap().SettingInt("gban_enabled", 0) == 1
}

// botInGlobal 报告某个 bot 是否加入了全局联合封禁组（默认加入，
// 可在配置台按 bot 退出）。
func botInGlobal(snap *store.Snapshot, botID int64) bool {
	return snap.BotSettingInt(botID, "gban_global", 1) == 1
}

// gbanHit 查该用户在该群是否被某个**适用**的联合封禁组命中。
//
// 两个组：全局组（默认存在，bot 自主选择加入）与归属人的专属组（可
// 开关、圈定生效群）。适用性判定：
//   - 全局组：总开关打开且本 bot 已加入；
//   - 专属组：总开关打开、归属人开了组、且本群在组的生效群清单里。
//
// 纯内存判断，进群与发言路径上每个人都要过一次。
func gbanHit(sh *core.Shared, botID, chatID, uid int64) (store.GbanRec, bool) {
	snap := sh.Cache.Snap()
	if !GbanEnabled(sh) {
		return store.GbanRec{}, false
	}
	if botInGlobal(snap, botID) {
		if r, ok := snap.Gban[uid]; ok {
			return r, true
		}
	}
	if rec := snap.Bots[botID]; rec != nil &&
		snap.GbanOwnOn(rec.OwnerID) && snap.GbanOwnChats[rec.OwnerID][chatID] {
		if r, ok := snap.GbanOwnBans[rec.OwnerID][uid]; ok {
			return r, true
		}
	}
	return store.GbanRec{}, false
}

// maybeGban 在总开关打开时把最高档处置（删+禁言）与人工标记的命中写进
// 联合封禁：专属组总是记（归属人的账本），全局组只有 bot 加入时才共享。
// 记完在各自覆盖范围内当场执行。
func maybeGban(b *core.Bot, srcChat, uid int64, reason string) {
	if !GbanEnabled(b.Shared) {
		return
	}
	snap := b.Cache.Snap()
	shared := botInGlobal(snap, b.BotID())
	// 已在适用名单里的人不再重复全平台扇出：刷屏号连着几条消息都判到最高档
	// 时，每一条都扇出几百个 banChatMember 只是撞 TG 的限流。
	_, ownListed := snap.GbanOwnBans[b.Owner()][uid]
	_, globalListed := snap.Gban[uid]
	alreadyEnforced := ownListed && (!shared || globalListed)

	// 两条记录一次写完、只刷新一次快照：逐条写会各重建一次全量配置
	// （十几条查询），而刷屏高峰里这是每条最高档处置都要付的。
	if err := gbanRecordBoth(b.Shared, b.Owner(), uid, reason, srcChat, b.BotID(), shared); err != nil {
		slog.Error("联合封禁：入账失败", "uid", uid, "err", err)
		return
	}
	if alreadyEnforced {
		return
	}
	go func() {
		if shared {
			EnforceGban(b.Shared, uid, reason)
		}
		EnforceGbanOwn(b.Shared, b.Owner(), uid)
	}()
}

// gbanRecordBoth 一次写入专属组与（shared 为真时）全局组，最后只刷新
// 一次快照。两个 INSERT 都是 ON CONFLICT DO NOTHING，重复命中不报错。
func gbanRecordBoth(sh *core.Shared, ownerID, uid int64, reason string, srcChat, byBot int64,
	shared bool) error {

	now := time.Now().Unix()
	if _, err := sh.Store.Write.Exec(`INSERT INTO gban_own_bans
		(owner_id,user_id,reason,src_chat,created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(owner_id,user_id) DO NOTHING`,
		ownerID, uid, core.TruncateRunes(reason, 200), srcChat, now); err != nil {
		return err
	}
	if shared {
		if _, err := sh.Store.Write.Exec(`INSERT INTO gban
			(user_id,reason,src_chat,by_bot,created_at) VALUES (?,?,?,?,?)
			ON CONFLICT(user_id) DO NOTHING`,
			uid, core.TruncateRunes(reason, 200), srcChat, byBot, now); err != nil {
			return err
		}
	}
	return sh.Cache.Reload()
}

// gbanAdd 把人写进名单。已在名单里时保留最早那条记录 ——
// 首次判定的来源与理由比后来的重复命中更有价值。
func GbanAdd(sh *core.Shared, uid int64, reason string, srcChat, byBot int64) error {
	if _, err := sh.Store.Write.Exec(`INSERT INTO gban
		(user_id,reason,src_chat,by_bot,created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(user_id) DO NOTHING`,
		uid, core.TruncateRunes(reason, 200), srcChat, byBot, time.Now().Unix()); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// gbanRemove 把人移出名单。
func gbanRemove(sh *core.Shared, uid int64) error {
	if _, err := sh.Store.Write.Exec(`DELETE FROM gban WHERE user_id=?`, uid); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// eachActiveChat 遍历全平台所有「已启用」的群，带上该群里全部可用的 bot。
// include 决定哪些 bot 参与：全局组的封禁/解除只覆盖加入全局组的 bot，
// 专属组只覆盖圈进组里的群。传 nil 表示不过滤。
//
// 按群而不是按 (bot, 群) 遍历：同群挂着几个 bot 时，对同一个人只该动手一次。
func eachActiveChat(sh *core.Shared, include func(botID int64) bool,
	fn func(bots []*core.Bot, chatID int64)) {
	if sh.Reg == nil {
		return
	}
	snap := sh.Cache.Snap()
	byChat := map[int64][]*core.Bot{}
	sh.Reg.Each(func(b *core.Bot) {
		if include != nil && !include(b.BotID()) {
			return
		}
		for _, c := range snap.ChatsOf(b.BotID()) {
			if c.Enabled {
				byChat[c.ChatID] = append(byChat[c.ChatID], b)
			}
		}
	})
	for chatID, bots := range byChat {
		fn(bots, chatID)
	}
}

// botsOfChat 收集当前活着的、覆盖某个群的全部 bot。专属组按群圈定，
// 执行时谁在群里谁动手（callFirstOK 会挑到第一个有权限的）。
func botsOfChat(sh *core.Shared, chatID int64) []*core.Bot {
	if sh.Reg == nil {
		return nil
	}
	var out []*core.Bot
	sh.Reg.Each(func(b *core.Bot) {
		if c, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID); ok && c.Enabled {
			out = append(out, b)
		}
	})
	return out
}

// callFirstOK 让群里的 bot 依次尝试，直到有一个成功：只挂一个 bot 去执行的话，
// 恰好挑中一个没有封禁权限的，这个群就被放掉了。
func callFirstOK(bots []*core.Bot, method string, payload map[string]any) (bool, string) {
	desc := ""
	for _, b := range bots {
		var ok bool
		if ok, desc = b.CallOK(method, payload); ok {
			return true, ""
		}
	}
	return false, desc
}

// EnforceGban 在全局组覆盖范围内执行一次封禁：只动加入全局组的 bot 名下
// 的群——退出的 bot 不再共享，也就不该被全组执行波及。
//
// 失败只记日志：bot 在某个群没有封禁权限、或对方本就不在那个群，
// 都是常态而非故障，不该让一个群的失败挡住其余的。
func EnforceGban(sh *core.Shared, uid int64, reason string) {
	n := gbanFanover(sh, uid, "banChatMember", nil, false)
	slog.Info("联合封禁已执行", "uid", uid, "群数", n, "理由", reason)
}

// gbanApply 在一个群里执行联合封禁，方式服从该群自己的处置配置：
//
//	dryrun   → 什么都不做（演练群不该因为全平台名单真动手）
//	封禁档   → banChatMember（请出群）
//	禁言档   → restrictChatMember（按本 bot 的禁言时长，0 = 永久）
//
// 返回实际动作（ban / mute / dryrun）与是否成功。频道身份由 BanSender /
// MuteSender 各自处理（封频道不支持限时，退回永久封）。
func gbanApply(b *core.Bot, conf store.BotChat, uid int64) (string, bool, string) {
	snap := b.Cache.Snap()
	if conf.Dryrun {
		return "dryrun", true, ""
	}
	if snap.BanMode(conf) {
		ok, desc := BanSender(b, conf.ChatID, uid)
		return "ban", ok, desc
	}
	minutes := snap.BotSettingInt(b.BotID(), "antiad_mute_minutes", 1440)
	ok, desc := MuteSender(b, conf.ChatID, uid, time.Duration(minutes)*time.Minute)
	return "mute", ok, desc
}

// gbanActInChat 在某个群里执行联合封禁。一个群可能挂着同归属人的多个 bot，
// 配置各算各的（演练、处罚方式都是 per-bot 的）：取第一个**不是演练**的配置
// 执行；它没权限（或不在群里）时再换下一个；全是演练群就什么都不做。
func gbanActInChat(sh *core.Shared, bots []*core.Bot, chatID, uid int64) {
	var lastDesc string
	for _, b := range bots {
		conf, ok := b.Cache.Snap().ChatConf(b.BotID(), chatID)
		if !ok || conf.Dryrun {
			continue
		}
		act, ok2, desc := gbanApply(b, conf, uid)
		if ok2 {
			slog.Info("联合封禁已执行", "chat", chatID, "uid", uid, "方式", act)
			gbanLogAction(b, chatID, uid, act)
			return
		}
		lastDesc = desc
	}
	if lastDesc != "" {
		slog.Warn("联合封禁：执行失败", "chat", chatID, "uid", uid, "tg", lastDesc)
	}
}

// gbanLogAction 把名单在某群里的实际动作落一条流水。
//
// 没有这条记录，日后撤销联合封禁时就不知道该在哪些群解禁：unbanChatMember
// 只解封禁、解不了禁言，而禁言档的群当初走的是 restrictChatMember ——
// 「名单已经撤了，人在群里还是发不了言」就是这么来的。发言路径上的
// 联封（GbanMessageGuard）从一开始就有记录，名单扇出这一支漏了。
func gbanLogAction(b *core.Bot, chatID, uid int64, act string) {
	reason := "联合封禁"
	if rec, ok := gbanHit(b.Shared, b.BotID(), chatID, uid); ok {
		reason += "：" + GbanReasonLabel(rec.Reason)
	}
	action := "gban_muted"
	if act == "ban" {
		action = "gban_banned"
	}
	logAd(b, &tg.Message{Chat: &tg.Chat{ID: chatID}, From: &tg.TGUser{ID: uid}},
		adVerdict{Decider: adDeciderSkipped, Reason: reason}, action, "联合封禁（名单）")
}

// penaltyRowsActive 报告某人在某群里还有没有本 bot 生效中的处罚（不含
// 联合封禁行）。撤联合封禁时用它避免顺手解掉别的判定留下的禁言。
func penaltyRowsActive(sh *core.Shared, botID, chatID, uid int64) bool {
	var n int
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE bot_id=? AND chat_id=? AND user_id=? AND lifted_at=0
		AND action IN ('deleted_muted','muted','deleted_banned','banned')`,
		botID, chatID, uid).Scan(&n); err == nil && n > 0 {
		return true
	}
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM join_mutes
		WHERE chat_id=? AND user_id=?`, chatID, uid).Scan(&n); err == nil && n > 0 {
		return true
	}
	return false
}

// gbanLiftRecorded 撤销联合封禁时，把当初在各群真禁过言的也解掉。
//
// unbanChatMember 只解封禁、不碰禁言；禁言档的群（punish=0，或跟随 bot
// 的 antiad_ban=0）当初走的是 restrictChatMember，只能再发一次「权限全开」
// 去解。少了这一步，误判撤了名单、人在那些群里却永远发不了言。
//
// 只动有 gban_muted 流水、且群里没有别的生效处罚的那些群：没记录就不敢
// 动（可能是人类管理员施加的禁言，解禁还会把人提到群默认权限之上）。
func gbanLiftRecorded(sh *core.Shared, uid int64) {
	rows, err := sh.Store.Read.Query(`SELECT DISTINCT chat_id FROM antiad_log
		WHERE user_id=? AND action='gban_muted' AND lifted_at=0 LIMIT 100`, uid)
	if err != nil {
		slog.Error("联合封禁：读取待解除的禁言失败", "uid", uid, "err", err)
		return
	}
	var chats []int64
	for rows.Next() {
		var chatID int64
		if rows.Scan(&chatID) == nil {
			chats = append(chats, chatID)
		}
	}
	rows.Close()

	for _, chatID := range chats {
		bots := botsOfChat(sh, chatID)
		if len(bots) == 0 {
			continue
		}
		skip := false
		for _, b := range bots {
			if penaltyRowsActive(sh, b.BotID(), chatID, uid) {
				skip = true
				break
			}
		}
		if skip {
			slog.Info("联合封禁：群里还有别的生效处罚，保留禁言",
				"chat", chatID, "uid", uid)
			continue
		}
		for _, b := range bots {
			if ok, _ := Unmute(b, chatID, uid); !ok {
				continue
			}
			slog.Info("联合封禁：已解除禁言", "chat", chatID, "uid", uid)
			if _, err := sh.Store.Write.Exec(`UPDATE antiad_log SET lifted_at=?
				WHERE user_id=? AND action='gban_muted' AND lifted_at=0
				AND chat_id=?`, time.Now().Unix(), uid, chatID); err != nil {
				slog.Error("联合封禁：标记禁言已解除失败",
					"chat", chatID, "uid", uid, "err", err)
			}
			break
		}
	}
}

// gbanFanover 是封禁/解除的公共执行器：chats 为 nil 时遍历全局组覆盖的
// 所有群，否则只动给定清单（专属组用）。返回实际派发的群数。
//
// 封禁按各群自己的处置方式执行（见 gbanApply）；解除是无条件修复动作
// （dryrun 群也会执行——那边可能曾经真封过），只带 only_if_banned。
func gbanFanover(sh *core.Shared, uid int64, method string, chats []int64,
	onlyIfBanned bool) int {
	sem := gbanSem
	done := make(chan struct{})
	n := 0

	work := func(bots []*core.Bot, chatID int64) {
		n++
		go func() {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			if method == "banChatMember" && !onlyIfBanned {
				gbanActInChat(sh, bots, chatID, uid)
				return
			}
			payload := map[string]any{"chat_id": chatID, "user_id": uid}
			if onlyIfBanned {
				payload["only_if_banned"] = true
			}
			callFirstOK(bots, method, payload)
		}()
	}

	if chats == nil {
		eachActiveChat(sh, func(botID int64) bool {
			return botInGlobal(sh.Cache.Snap(), botID)
		}, work)
	} else {
		for _, chatID := range chats {
			if bots := botsOfChat(sh, chatID); len(bots) > 0 {
				work(bots, chatID)
			}
		}
	}
	for i := 0; i < n; i++ {
		<-done
	}
	return n
}

// LiftGban 解除全局组封禁：移出名单并在全局组覆盖范围内解封。
//
// unbanChatMember 必须带 only_if_banned —— 不带的话，对一个**没被封**
// 的人调用它，Telegram 的语义是「把他踢出群再解除封禁」，于是解封动作
// 反而把无辜的人踢了出去。这是整个联合封禁里最容易写反的一处。
func LiftGban(sh *core.Shared, uid int64) {
	if err := gbanRemove(sh, uid); err != nil {
		slog.Error("联合封禁：移出名单失败", "uid", uid, "err", err)
	}
	n := gbanFanover(sh, uid, "unbanChatMember", nil, true)
	gbanLiftRecorded(sh, uid)
	slog.Info("联合封禁已解除", "uid", uid, "群数", n)
}

// ---- 专属联合封禁组 ----

// GbanOwnSetEnabled 开/关某管理员的专属组。关掉后条目保留，
// 只是既不执行也不新增生效。
func GbanOwnSetEnabled(sh *core.Shared, ownerID int64, on bool) error {
	enabled := int64(0)
	if on {
		enabled = 1
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO gban_own (owner_id,enabled,created_at)
		VALUES (?,?,?)
		ON CONFLICT(owner_id) DO UPDATE SET enabled=excluded.enabled`,
		ownerID, enabled, time.Now().Unix()); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// GbanOwnSetChat 把群加进/移出专属组的生效群清单。
// 只能圈自己名下 bot 覆盖的群，调用方负责校验。
func GbanOwnSetChat(sh *core.Shared, ownerID, chatID int64, on bool) error {
	var err error
	if on {
		_, err = sh.Store.Write.Exec(`INSERT OR IGNORE INTO gban_own_chats
			(owner_id,chat_id) VALUES (?,?)`, ownerID, chatID)
	} else {
		_, err = sh.Store.Write.Exec(`DELETE FROM gban_own_chats
			WHERE owner_id=? AND chat_id=?`, ownerID, chatID)
	}
	if err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// GbanOwnAddBan 把人写进专属组的账本。已在账上时保留最早那条 ——
// 首次判定的来源与理由比后来的重复命中更有价值。
func GbanOwnAddBan(sh *core.Shared, ownerID, uid int64, reason string, srcChat int64) error {
	if _, err := sh.Store.Write.Exec(`INSERT INTO gban_own_bans
		(owner_id,user_id,reason,src_chat,created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(owner_id,user_id) DO NOTHING`,
		ownerID, uid, core.TruncateRunes(reason, 200), srcChat, time.Now().Unix()); err != nil {
		return err
	}
	return sh.Cache.Reload()
}

// GbanOwnRemoveBan 把人移出专属组账本，并在组内生效群里解封。
func GbanOwnRemoveBan(sh *core.Shared, ownerID, uid int64) error {
	if _, err := sh.Store.Write.Exec(`DELETE FROM gban_own_bans
		WHERE owner_id=? AND user_id=?`, ownerID, uid); err != nil {
		return err
	}
	if err := sh.Cache.Reload(); err != nil {
		return err
	}
	if snap := sh.Cache.Snap(); snap.GbanOwnOn(ownerID) {
		gbanFanover(sh, uid, "unbanChatMember", ownChats(snap, ownerID), true)
	}
	gbanLiftRecorded(sh, uid)
	return nil
}

// EnforceGbanOwn 在专属组的生效群里执行封禁。组没开或群没圈就没有覆盖，
// 什么都不做——圈定清单本身就是执行范围的边界。
func EnforceGbanOwn(sh *core.Shared, ownerID, uid int64) {
	snap := sh.Cache.Snap()
	if !snap.GbanOwnOn(ownerID) {
		return
	}
	gbanFanover(sh, uid, "banChatMember", ownChats(snap, ownerID), false)
}

// ownChats 把专属组的生效群清单摊平成切片。
func ownChats(snap *store.Snapshot, ownerID int64) []int64 {
	var out []int64
	for chatID := range snap.GbanOwnChats[ownerID] {
		out = append(out, chatID)
	}
	return out
}

// AdminLiftGban 管理员解除某人的联合封禁：全局组对所有管理员开放
// （共同维护的名单），专属组只动自己的账本。返回解除到的范围说明，
// 空串表示此人不在任何名单里。
func AdminLiftGban(sh *core.Shared, adminUID, target int64) string {
	var lifted []string
	snap := sh.Cache.Snap()
	if _, ok := snap.Gban[target]; ok {
		LiftGban(sh, target)
		lifted = append(lifted, "全局联合封禁组")
	}
	if _, ok := snap.GbanOwnBans[adminUID][target]; ok {
		if err := GbanOwnRemoveBan(sh, adminUID, target); err != nil {
			slog.Error("联合封禁：专属组移除失败", "uid", target, "err", err)
		} else {
			lifted = append(lifted, "你的专属联合封禁组")
		}
	}
	return strings.Join(lifted, "、")
}

// gbanGuard 在有人进群时查适用的联合封禁组，命中即按**本群自己的处置方式**
// 处置（演练群不动手、禁言档禁言、封禁档请出群，见 gbanApply）——联合封禁
// 是「这个人已在别处被判为广告」的跨群结论，但怎么处置仍归各群的配置管。
//
// 这是联合封禁最值钱的部分：拦在门口，而不是等他发完广告再删。
// 返回 true 表示这个人已被拦下，调用方不必再做后续处理。
func gbanGuard(b *core.Bot, conf store.BotChat, u *tg.TGUser) bool {
	if u == nil {
		return false
	}
	rec, ok := gbanHit(b.Shared, b.BotID(), conf.ChatID, u.ID)
	if !ok {
		return false
	}
	act, ok2, desc := gbanApply(b, conf, u.ID)
	if !ok2 {
		slog.Warn("联合封禁：进群拦截失败", "chat", conf.ChatID, "uid", u.ID, "tg", desc)
		return false
	}
	if act == "dryrun" {
		// 演练群：判定/名单照常走，但不动手，也不发群内消息。
		slog.Info("联合封禁：演练群不动手", "chat", conf.ChatID, "uid", u.ID)
		return false
	}
	slog.Info("联合封禁：进群即拦下", "chat", conf.ChatID, "uid", u.ID, "方式", act)

	verdict := map[string]string{"ban": "被封禁出群", "mute": "被禁言"}[act]
	for _, admin := range b.AlertTargets() {
		b.Send(admin, fmt.Sprintf(
			"🚫 <b>联合封禁拦截</b>\n\n用户 %s (<code>%d</code>) 进入群 <code>%d</code> 时%s。\n原因: %s",
			html.EscapeString(senderName(u)), u.ID, conf.ChatID, verdict,
			html.EscapeString(core.TruncateRunes(rec.Reason, 120))), nil)
	}
	return true
}

// GbanMessageGuard 在发言路径上执行联合封禁。白名单优先——/white 的
// 「本群解封」就是白名单，管理员放行的人不该再被名单拦下。命中即按本群
// 自己的处置方式处置（演练群不动手、禁言档禁言、封禁档请出群），删除本条
// 并落一条流水，不再留底、不再送检。
// 返回 true 表示这条消息已按联合封禁处理完，调用方直接返回。
//
// whitelisted 是调用方从画像行带出来的 /white 标记（见 touchMember）。
func GbanMessageGuard(b *core.Bot, conf store.BotChat, u *tg.TGUser, msgID int64,
	whitelisted bool) bool {

	if u == nil || u.ID < 0 {
		return false
	}
	// 总开关默认关：先看它，别为每条群消息白查一次白名单（快照线性扫）。
	if !GbanEnabled(b.Shared) {
		return false
	}
	// 与 adExempt 同源的两组白名单：/white 的本群名单与解禁码白名单。
	if whitelisted ||
		b.Cache.Snap().Whitelisted(b.BotID(), conf.ChatID, u.ID, time.Now().Unix()) {
		return false
	}
	rec, hit := gbanHit(b.Shared, b.BotID(), conf.ChatID, u.ID)
	if !hit {
		return false
	}
	act, ok, desc := gbanApply(b, conf, u.ID)
	if act == "dryrun" {
		// 演练群：不动手，照常走后面的判定链路（判定照跑、开销照花）。
		return false
	}
	if !ok {
		slog.Warn("联合封禁：发言处置失败", "chat", conf.ChatID, "uid", u.ID, "tg", desc)
	}
	if msgID != 0 {
		b.CallOK("deleteMessage", map[string]any{
			"chat_id": conf.ChatID, "message_id": msgID})
	}
	action := "gban_muted"
	if act == "ban" {
		action = "gban_banned"
	}
	logAd(b, &tg.Message{Chat: &tg.Chat{ID: conf.ChatID}, From: u},
		adVerdict{Decider: adDeciderSkipped,
			Reason: "联合封禁：" + GbanReasonLabel(rec.Reason)},
		action, "联合封禁（发言）")
	slog.Info("联合封禁：发言即处置", "chat", conf.ChatID, "uid", u.ID, "方式", act)
	return true
}

// gbanList 取名单，按加入时间倒序。
func GbanList(sh *core.Shared, limit, offset int) []store.GbanRec {
	rows, err := sh.Store.Read.Query(`SELECT user_id,reason,src_chat,by_bot,created_at
		FROM gban ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		slog.Error("联合封禁：读取名单失败", "err", err)
		return nil
	}
	defer rows.Close()
	var out []store.GbanRec
	for rows.Next() {
		var g store.GbanRec
		if err := rows.Scan(&g.UserID, &g.Reason, &g.SrcChat, &g.ByBot,
			&g.CreatedAt); err != nil {
			slog.Warn("联合封禁：名单行解析失败", "err", err)
			continue
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		slog.Error("联合封禁：名单读取中断", "err", err)
	}
	return out
}

// gbanReasonLabel 把理由压成一行，供面板展示。
func GbanReasonLabel(r string) string {
	r = strings.ReplaceAll(r, "\n", " ")
	if r == "" {
		return "（无）"
	}
	return core.TruncateRunes(r, 60)
}

// CleanupData 按 settings.log_retention_days 删除过期数据。
//
// 非法值（0 / 负数 / 非数字）一律回落到 30 天而不是把 cutoff 算成「现在」——
// 后者会把全部历史一次性删光，且不可逆。
//
// 全部走 deleteBatched：写连接固定为 1，一条大 DELETE 从第一行到提交都持有
// 写锁，期间所有群消息的留底与流水都在后面排队（广告洪峰时正好是最不能停的
// 时候）。分批之间会让出写连接，消息写入得以插队。
func CleanupData(sh *core.Shared) {
	days := sh.Cache.Snap().SettingInt("log_retention_days", 30)
	if days < 1 {
		days = 30
	}
	cut := time.Now().Unix() - days*86400

	// antiad_log 存着群消息原文，不能永久留在库里。
	if _, err := deleteBatched(sh, "antiad_log", "created_at < ?", cut); err != nil {
		slog.Error("清理判定流水失败", "err", err)
	}
	// group_messages 是群聊原文的留底，与 antiad_log 同寿。
	if _, err := deleteBatched(sh, "group_messages", "at < ?", cut); err != nil {
		slog.Error("清理群消息留底失败", "err", err)
	}
	// group_members 同样是群聊衍生数据（TG user_id + 发言计数），保留策略
	// 要与上面一致，否则群从白名单移除后它的行永久留着。
	// 有命中史的行保留：ad_hits 是风控证据，清掉等于给惯犯重置档案。
	// 「最近活动」取发言与进群里较晚的那个：只看 last_msg_at 的话，进群后
	// 还没发言的人（last_msg_at 为 0）第一轮就被删掉，丢了进群时间。
	// 白名单（/white）是管理员的明确决定，不随不发言过期。
	// 谓词写成两个单列比较（MAX(a,b) < c 等价于 a < c AND b < c），
	// 这样 last_msg_at 上的索引才用得上。
	if _, err := deleteBatched(sh, "group_members",
		"last_msg_at < ? AND joined_at < ? AND ad_hits = 0 AND whitelisted = 0",
		cut, cut); err != nil {
		slog.Error("清理群成员画像失败", "err", err)
	}
	// 内容哈希按最近一次命中过期：广告模板换得很快，久不出现的留着只是占地方。
	if _, err := deleteBatched(sh, "ad_hashes", "last_hit_at < ?", cut); err != nil {
		slog.Error("清理内容哈希失败", "err", err)
	}
	// 待撤回告警里 bot 已被删掉、没人去撤的残留行。
	if _, err := deleteBatched(sh, "alert_cleanup", "due_at < ?", cut); err != nil {
		slog.Error("清理待撤回告警失败", "err", err)
	}
	// 申诉与网页验证：过保留期的结案单与验证记录一起清；未结单不动
	// （用户可能还在流程里，noweb 的还在等管理员处理）。
	if _, err := deleteBatched(sh, "web_checks", "created_at < ?", cut); err != nil {
		slog.Error("清理网页验证记录失败", "err", err)
	}
	if _, err := deleteBatched(sh, "appeals", `updated_at < ? AND status NOT IN (`+
		store.AppealOpenStatusesSQL+`)`, cut); err != nil {
		slog.Error("清理申诉单失败", "err", err)
	}
	if _, err := deleteBatched(sh, "appeal_redeems",
		"appeal_id NOT IN (SELECT id FROM appeals)"); err != nil {
		slog.Error("清理兑换记录失败", "err", err)
	}
	// 过期的白名单：到点即失效，删掉后恢复正常检查。
	if _, err := deleteBatched(sh, "ad_whitelist",
		"expires_at != 0 AND expires_at < ?", time.Now().Unix()); err != nil {
		slog.Error("清理过期白名单失败", "err", err)
	}
}

// cleanupBatch 是每批删除的行数。取几千：一批的锁持有时间是毫秒级，
// 又不会让循环因为批次太小而跑几百轮。
const cleanupBatch = 5000

// deleteBatched 分批删除过期行，返回删除总数。
//
// SQLite 的 DELETE 默认不支持 LIMIT（未开 SQLITE_ENABLE_UPDATE_DELETE_LIMIT），
// 所以用 rowid 子查询分批。table 与 cond 只来自本包的字面量，不存在注入面。
func deleteBatched(sh *core.Shared, table, cond string, args ...any) (int64, error) {
	var total int64
	for {
		q := `DELETE FROM ` + table + ` WHERE rowid IN (
			SELECT rowid FROM ` + table + ` WHERE ` + cond + ` LIMIT ?)`
		all := append(append([]any{}, args...), cleanupBatch)
		res, err := sh.Store.Write.Exec(q, all...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < cleanupBatch {
			return total, nil
		}
	}
}
