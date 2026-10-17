package antiad

import (
	"fmt"
	"log/slog"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 群内清理上下文（/clear）----
//
// 与 /uad 的差别是作用面：/uad 放行一个人并截断他的判定史，/clear 不解除
// 任何限制，只把**本群全部成员**的判定史截到此刻 —— 群被广告刷过一轮之后，
// 那批留底已经失去参考价值，继续当证据只会让后续判定沿着旧语境走。

const clearUsage = "用法：群内直接发 <code>/clear</code>。\n" +
	"把本群全部成员的判定史截到此刻：此前的留底不再进后续判定" +
	"（最近上下文与 /check 复查），历史命中数归零。\n" +
	"不解除任何限制，也不删留底与流水 —— 需要时仍可在面板上复查。"

// HandleClearCommand 处理群内 /clear。
//
// 权限与 /uad、/ungban 同一道门：它改变的是全群成员的判定输入，
// 只有群管理员及以上能用，非授权者静默忽略。
func HandleClearCommand(b *core.Bot, conf store.BotChat, m *tg.Message) {
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	// 命令本身不该留在群里（与 /ban、/uad 一致）。删不掉也不影响后续。
	b.TG.Call("deleteMessage", map[string]any{
		"chat_id": conf.ChatID, "message_id": m.MessageID,
	})

	snap := b.Cache.Snap()
	ttl := time.Duration(snap.BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) { groupNotice(b, conf.ChatID, text, nil, ttl) }

	if !b.AdLimits.Allow(fmt.Sprintf("clear:%d", m.From.ID),
		snap.BotSettingInt(b.BotID(), "antiad_cmd_rpm", 3)) {
		reply("操作太频繁，请稍后再试。")
		return
	}

	n, err := ResetChatHistory(b, conf.ChatID)
	if err != nil {
		reply("⚠️ 清理失败：" + htmlEscapeShort(err.Error()))
		return
	}
	reply(fmt.Sprintf("✅ 已清理本群 %d 名成员的判定上下文："+
		"此前留底不再进入后续判定，历史命中数归零。\n"+
		"留底与流水保留，需要时可在面板复查。", n))
	slog.Info("反广告：管理员清理本群判定上下文", "chat", conf.ChatID,
		"by", m.From.ID, "成员数", n)
}
