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
	if err := GbanOwnAddBan(b.Shared, b.Owner(), uid, reason, srcChat); err != nil {
		slog.Error("联合封禁：专属组入账失败", "uid", uid, "err", err)
	}
	shared := botInGlobal(b.Cache.Snap(), b.BotID())
	if shared {
		if err := GbanAdd(b.Shared, uid, reason, srcChat, b.BotID()); err != nil {
			slog.Error("联合封禁：入名单失败", "uid", uid, "err", err)
			return
		}
	}
	go func() {
		if shared {
			EnforceGban(b.Shared, uid, reason)
		}
		EnforceGbanOwn(b.Shared, b.Owner(), uid)
	}()
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

// gbanFanover 是封禁/解除的公共执行器：chats 为 nil 时遍历全局组覆盖的
// 所有群，否则只动给定清单（专属组用）。返回实际派发的群数。
// onlyIfBanned 给 unbanChatMember 用：不带它，对一个**没被封**的人解封
// 的语义是「先踢出群再解封」，会把无辜的人踢出去。
func gbanFanover(sh *core.Shared, uid int64, method string, chats []int64,
	onlyIfBanned bool) int {
	sem := make(chan struct{}, gbanFanout)
	done := make(chan struct{})
	n := 0

	work := func(bots []*core.Bot, chatID int64) {
		n++
		go func() {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
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

// gbanGuard 在有人进群时查适用的联合封禁组，命中即当场封禁。
//
// 这是联合封禁最值钱的部分：拦在门口，而不是等他发完广告再删。
// 返回 true 表示这个人已被拦下，调用方不必再做后续处理。
func gbanGuard(b *core.Bot, chatID int64, u *tg.TGUser) bool {
	if u == nil {
		return false
	}
	rec, ok := gbanHit(b.Shared, b.BotID(), chatID, u.ID)
	if !ok {
		return false
	}
	if ok, desc := b.CallOK("banChatMember", map[string]any{
		"chat_id": chatID, "user_id": u.ID,
	}); !ok {
		slog.Warn("联合封禁：进群拦截失败", "chat", chatID, "uid", u.ID, "tg", desc)
		return false
	}
	slog.Info("联合封禁：进群即拦下", "chat", chatID, "uid", u.ID)

	for _, admin := range b.AlertTargets() {
		b.Send(admin, fmt.Sprintf(
			"🚫 <b>联合封禁拦截</b>\n\n用户 %s (<code>%d</code>) 进入群 <code>%d</code> 时被拦下。\n原因: %s",
			html.EscapeString(senderName(u)), u.ID, chatID,
			html.EscapeString(core.TruncateRunes(rec.Reason, 120))), nil)
	}
	return true
}

// GbanMessageGuard 在发言路径上执行联合封禁。白名单优先——/white 的
// 「本群解封」就是白名单，管理员放行的人不该再被名单拦下。命中即禁言、
// 删除本条并落一条流水，不再留底、不再送检。
// 返回 true 表示这条消息已按联合封禁处理完，调用方直接返回。
func GbanMessageGuard(b *core.Bot, chatID int64, u *tg.TGUser, msgID int64) bool {
	if u == nil || u.ID < 0 {
		return false
	}
	// 与 adExempt 同源的两组白名单：/white 的本群名单与解禁码白名单。
	if isGroupWhitelisted(b, chatID, u.ID) ||
		b.Cache.Snap().Whitelisted(b.BotID(), chatID, u.ID, time.Now().Unix()) {
		return false
	}
	rec, hit := gbanHit(b.Shared, b.BotID(), chatID, u.ID)
	if !hit {
		return false
	}
	if ok, desc := b.CallOK("restrictChatMember", map[string]any{
		"chat_id": chatID, "user_id": u.ID,
		"permissions": MutedPermissions(),
	}); !ok {
		slog.Warn("联合封禁：发言禁言失败", "chat", chatID, "uid", u.ID, "tg", desc)
	}
	if msgID != 0 {
		b.CallOK("deleteMessage", map[string]any{
			"chat_id": chatID, "message_id": msgID})
	}
	logAd(b, &tg.Message{Chat: &tg.Chat{ID: chatID}, From: u},
		adVerdict{Decider: adDeciderSkipped,
			Reason: "联合封禁：" + GbanReasonLabel(rec.Reason)},
		"gban_muted", "联合封禁（发言）")
	slog.Info("联合封禁：发言即禁言", "chat", chatID, "uid", u.ID)
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
func CleanupData(sh *core.Shared) {
	days := sh.Cache.Snap().SettingInt("log_retention_days", 30)
	if days < 1 {
		days = 30
	}
	cut := time.Now().Unix() - days*86400

	// antiad_log 存着群消息原文，不能永久留在库里。
	if _, err := sh.Store.Write.Exec(
		`DELETE FROM antiad_log WHERE created_at < ?`, cut); err != nil {
		slog.Error("清理判定流水失败", "err", err)
	}
	// group_messages 是群聊原文的留底，与 antiad_log 同寿。
	if _, err := sh.Store.Write.Exec(
		`DELETE FROM group_messages WHERE at < ?`, cut); err != nil {
		slog.Error("清理群消息留底失败", "err", err)
	}
	// group_members 同样是群聊衍生数据（TG user_id + 发言计数），保留策略
	// 要与上面一致，否则群从白名单移除后它的行永久留着。
	// 有命中史的行保留：ad_hits 是风控证据，清掉等于给惯犯重置档案。
	// 「最近活动」取发言与进群里较晚的那个：只看 last_msg_at 的话，进群后
	// 还没发言的人（last_msg_at 为 0）第一轮就被删掉，丢了进群时间。
	// 白名单（/white）是管理员的明确决定，不随不发言过期。
	if _, err := sh.Store.Write.Exec(`DELETE FROM group_members
		WHERE MAX(last_msg_at, joined_at) < ? AND ad_hits = 0 AND whitelisted = 0`,
		cut); err != nil {
		slog.Error("清理群成员画像失败", "err", err)
	}
	// 内容哈希按最近一次命中过期：广告模板换得很快，久不出现的留着只是占地方。
	if _, err := sh.Store.Write.Exec(
		`DELETE FROM ad_hashes WHERE last_hit_at < ?`, cut); err != nil {
		slog.Error("清理内容哈希失败", "err", err)
	}
	// 待撤回告警里 bot 已被删掉、没人去撤的残留行。
	if _, err := sh.Store.Write.Exec(
		`DELETE FROM alert_cleanup WHERE due_at < ?`, cut); err != nil {
		slog.Error("清理待撤回告警失败", "err", err)
	}
	// 申诉与网页验证：过保留期的结案单与验证记录一起清；未结单不动
	// （用户可能还在流程里）。
	if _, err := sh.Store.Write.Exec(`DELETE FROM web_checks WHERE created_at < ?`,
		cut); err != nil {
		slog.Error("清理网页验证记录失败", "err", err)
	}
	if _, err := sh.Store.Write.Exec(`DELETE FROM appeals WHERE updated_at < ?
		AND status NOT IN ('statement','ai','web','code')`, cut); err != nil {
		slog.Error("清理申诉单失败", "err", err)
	}
	if _, err := sh.Store.Write.Exec(`DELETE FROM appeal_redeems
		WHERE appeal_id NOT IN (SELECT id FROM appeals)`); err != nil {
		slog.Error("清理兑换记录失败", "err", err)
	}
	// 过期的白名单：到点即失效，删掉后恢复正常检查。
	if _, err := sh.Store.Write.Exec(`DELETE FROM ad_whitelist
		WHERE expires_at != 0 AND expires_at < ?`, time.Now().Unix()); err != nil {
		slog.Error("清理过期白名单失败", "err", err)
	}
}
