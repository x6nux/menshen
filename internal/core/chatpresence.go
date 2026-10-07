package core

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/tg"
)

// ---- 子 bot 在群痕迹与超时未配置退群 ----
//
// 群配置是注册制的另一面：bot 被拉进一个从未在面板挂到它名下的群时，
// 它在那里不做任何处理，但 Telegram 仍会把那个群的每一条更新推到它的
// webhook 上，任意群都能借此占用 webhook。对策：my_chat_member 实时
// 记录在群痕迹，加群后超过 subbot_autoleave_minutes（默认 60 分钟）仍未
// 配置就自动退群。
//
// 主 bot 不走这里：它被拉进群当场就退（见 main 的 handleMainBotChatMember），
// 没有等待期。

const (
	// SettingAutoLeaveMinutes 是加群后多久未配置就退群的全局设置键。
	// 0 表示关闭；关闭时仍记录痕迹：配置了或离群了仍会清掉，只是不再
	// 自动退群。
	SettingAutoLeaveMinutes = "subbot_autoleave_minutes"
	// DefaultAutoLeaveMinutes 是默认等待期。给管理员留出拉 bot 进群再在
	// 面板添加群配置的操作窗口，一小时足够，也不至于让无关群长期占用
	// webhook。
	DefaultAutoLeaveMinutes = 60

	// autoLeaveSweepLimit 是单轮清扫最多处理的痕迹行数。正常情况下一个
	// bot 名下未配置的群数量很少；上限只为极端情况设限，避免一轮
	// leaveChat 触发 TG 限速。
	autoLeaveSweepLimit = 200
)

// ChatMemberPresent 报告这次成员变更之后该成员是否还在群里。
//
// member / administrator / creator 没有 is_member 字段，一律算在群里；
// restricted 要额外看 is_member —— 被踢走的人也会留下一条受限记录。
func ChatMemberPresent(cm *tg.ChatMemberInfo) bool {
	switch cm.Status {
	case "member", "administrator", "creator":
		return true
	case "restricted":
		return cm.IsMember
	}
	return false
}

// NoteChatPresence 维护本 bot 的在群痕迹，子 bot 的 my_chat_member 更新
// 每次都过这里（main 的 dispatch）。
//
// 改权限、置顶这类变更也会推 my_chat_member，因此进群时刻用
// INSERT OR DO NOTHING 只记首次：反复覆盖会把退群时限一次次顺延，
// 一个不断变更权限的群就永远等不到超时。退了再进走离群分支删行重记，
// 得到新的进群时刻。
func (b *Bot) NoteChatPresence(cu *tg.ChatMemberUpdated) {
	if cu == nil || cu.Chat == nil || cu.NewChatMember == nil {
		return
	}
	if cu.Chat.Type == "private" {
		return
	}
	if b.IsMainBot() {
		// 主 bot 不留痕迹：它被拉进群当场就退，走的是另一条分发路径。
		// 这里防御性清理，避免 is_main 标记变动后残留痕迹。
		b.dropChatSeen(cu.Chat.ID)
		return
	}
	if ChatMemberPresent(cu.NewChatMember) {
		at := cu.Date
		if at == 0 {
			at = time.Now().Unix()
		}
		if _, err := b.Store.Write.Exec(`INSERT INTO bot_chat_seen
			(bot_id,chat_id,first_seen) VALUES (?,?,?)
			ON CONFLICT(bot_id,chat_id) DO NOTHING`,
			b.BotID(), cu.Chat.ID, at); err != nil {
			slog.Error("记录子 bot 在群痕迹失败", "bot", b.BotID(),
				"chat", cu.Chat.ID, "err", err)
		}
		return
	}
	// 离群（left/kicked）就删痕迹：留着的话，下次进群会被当成
	// 一直在群里而直接超时退群。
	b.dropChatSeen(cu.Chat.ID)
}

// dropChatSeen 删一行在群痕迹。行不存在是常态（本来就没记过），不报错。
func (b *Bot) dropChatSeen(chatID int64) {
	if _, err := b.Store.Write.Exec(
		`DELETE FROM bot_chat_seen WHERE bot_id=? AND chat_id=?`,
		b.BotID(), chatID); err != nil {
		slog.Error("删除子 bot 在群痕迹失败", "bot", b.BotID(),
			"chat", chatID, "err", err)
	}
}

// SweepUnconfiguredChats 是分钟级清扫（main 的 tickMinute）：到期的未配置
// 群退掉，已配置的把痕迹清掉。退群失败的下轮重试；对方报查无此群说明本来
// 就不在了（被踢、TG 已自行移除，或上轮退成功但没走到删痕迹），按结案处理，
// 不再反复重试。
func (b *Bot) SweepUnconfiguredChats(now time.Time) {
	if b.IsMainBot() {
		return
	}
	minutes := b.Cache.Snap().SettingInt(SettingAutoLeaveMinutes, DefaultAutoLeaveMinutes)
	if minutes <= 0 {
		return
	}
	rows, err := b.Store.Read.Query(`SELECT chat_id,first_seen FROM bot_chat_seen
		WHERE bot_id=? LIMIT ?`, b.BotID(), autoLeaveSweepLimit)
	if err != nil {
		slog.Error("读取子 bot 在群痕迹失败", "bot", b.BotID(), "err", err)
		return
	}
	type seen struct{ chatID, at int64 }
	var items []seen
	for rows.Next() {
		var it seen
		if rows.Scan(&it.chatID, &it.at) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		// 挂到名下（含停用 / 演练）即视为配置过：这是管理员要它待的群，
		// 退不退由人决定，痕迹清掉。快照逐行现取：leaveChat 一发就是
		// 几十毫秒，若整轮扫完才读新快照，管理员在这一轮里刚补上的配置
		// 会被当成未配置。
		if _, ok := b.Cache.Snap().ChatConf(b.BotID(), it.chatID); ok {
			b.dropChatSeen(it.chatID)
			continue
		}
		if now.Unix()-it.at < minutes*60 {
			continue
		}
		if ok, desc := b.CallOK("leaveChat", map[string]any{"chat_id": it.chatID}); !ok {
			if strings.Contains(strings.ToLower(desc), "not found") {
				b.dropChatSeen(it.chatID)
				continue
			}
			slog.Warn("子 bot 退出未配置群失败，下轮重试",
				"bot", b.BotID(), "chat", it.chatID, "tg", desc)
			continue
		}
		b.dropChatSeen(it.chatID)
		slog.Info("子 bot 加群超时未配置，已自动退群",
			"bot", b.BotID(), "chat", it.chatID,
			"在群分钟数", (now.Unix()-it.at)/60)
		b.notifyAutoLeave(it.chatID, minutes)
	}
}

// notifyAutoLeave 退群后通知归属人：bot 自己退群若没有解释，看起来像
// 故障。频次天然受限，同一个群要再触发需有人再把它拉进去并等满一个
// 等待期。
func (b *Bot) notifyAutoLeave(chatID int64, minutes int64) {
	msg := "ℹ️ 本 bot 已自动退出群组 <code>" +
		strconv.FormatInt(chatID, 10) + "</code>\n\n" +
		"加入后 <b>" + strconv.FormatInt(minutes, 10) + " 分钟</b>内未在面板把它配置到该群。" +
		"如果这是你的群，请先在面板把群添加到本 bot 名下，再把 bot 拉回群并设为管理员。"
	for _, admin := range b.AlertTargets() {
		b.Send(admin, msg, nil)
	}
}
