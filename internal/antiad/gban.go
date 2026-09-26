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

// isGbanned 查名单。纯内存判断，进群路径上每个人都要过一次。
func isGbanned(sh *core.Shared, uid int64) (store.GbanRec, bool) {
	r, ok := sh.Cache.Snap().Gban[uid]
	return r, ok
}

// maybeGban 在开关打开时把人加进联合封禁名单，并在全平台执行。
//
// 只在**最高档处置**（删+禁言，即置信度过了 hard 线）和人工标记时调用：
// 联合封禁会把人从所有接入群一起请出去，证据不足不能动。
func maybeGban(b *core.Bot, srcChat, uid int64, reason string) {
	if !GbanEnabled(b.Shared) {
		return
	}
	if err := GbanAdd(b.Shared, uid, reason, srcChat, b.BotID()); err != nil {
		slog.Error("联合封禁：入名单失败", "uid", uid, "err", err)
		return
	}
	go EnforceGban(b.Shared, uid, reason)
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
//
// 按群而不是按 (bot, 群) 遍历：同群挂着几个 bot 时，对同一个人只该动手一次。
func eachActiveChat(sh *core.Shared, fn func(bots []*core.Bot, chatID int64)) {
	if sh.Reg == nil {
		return
	}
	snap := sh.Cache.Snap()
	byChat := map[int64][]*core.Bot{}
	sh.Reg.Each(func(b *core.Bot) {
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

// enforceGban 在全平台执行一次封禁。
//
// 失败只记日志：bot 在某个群没有封禁权限、或对方本就不在那个群，
// 都是常态而非故障，不该让一个群的失败挡住其余的。
func EnforceGban(sh *core.Shared, uid int64, reason string) {
	sem := make(chan struct{}, gbanFanout)
	done := make(chan struct{})
	n := 0

	eachActiveChat(sh, func(bots []*core.Bot, chatID int64) {
		n++
		go func() {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			if ok, desc := callFirstOK(bots, "banChatMember", map[string]any{
				"chat_id": chatID, "user_id": uid,
			}); !ok {
				slog.Info("联合封禁：该群未执行成功",
					"chat", chatID, "uid", uid, "tg", desc)
			}
		}()
	})
	for range n {
		<-done
	}
	slog.Info("联合封禁已执行", "uid", uid, "群数", n, "理由", reason)
}

// liftGban 解除联合封禁：移出名单并在全平台解封。
//
// unbanChatMember 必须带 only_if_banned —— 不带的话，对一个**没被封**
// 的人调用它，Telegram 的语义是「把他踢出群再解除封禁」，于是解封动作
// 反而把无辜的人踢了出去。这是整个联合封禁里最容易写反的一处。
func LiftGban(sh *core.Shared, uid int64) {
	if err := gbanRemove(sh, uid); err != nil {
		slog.Error("联合封禁：移出名单失败", "uid", uid, "err", err)
	}
	sem := make(chan struct{}, gbanFanout)
	done := make(chan struct{})
	n := 0

	eachActiveChat(sh, func(bots []*core.Bot, chatID int64) {
		n++
		go func() {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			callFirstOK(bots, "unbanChatMember", map[string]any{
				"chat_id": chatID, "user_id": uid, "only_if_banned": true,
			})
		}()
	})
	for range n {
		<-done
	}
	slog.Info("联合封禁已解除", "uid", uid, "群数", n)
}

// gbanGuard 在有人进群时查名单，命中即当场封禁。
//
// 这是联合封禁最值钱的部分：拦在门口，而不是等他发完广告再删。
// 返回 true 表示这个人已被拦下，调用方不必再做后续处理。
func gbanGuard(b *core.Bot, chatID int64, u *tg.TGUser) bool {
	if u == nil || !GbanEnabled(b.Shared) {
		return false
	}
	rec, ok := isGbanned(b.Shared, u.ID)
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
	// 白名单（/adw）是管理员的明确决定，不随不发言过期。
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
}
