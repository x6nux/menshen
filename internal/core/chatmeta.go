package core

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// RefreshChatTitle 用本 bot 的 token 查一次群标题并写回 bot_chats。
//
// 群标题只在「添加群」那一刻抓一次，而先加配置、后把 bot 拉进群是合法的
// 操作顺序：那时 getChat 还看不到这个群，标题就永久空着，面板与 Mini App
// 只能显示裸 chat_id。启动回填与收到群消息时的按需刷新都走这里。
// 返回是否真的把标题写进了库。
func (b *Bot) RefreshChatTitle(chatID int64) bool {
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return false
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Title string `json:"title"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return false
	}
	title := strings.TrimSpace(resp.Result.Title)
	if title == "" {
		return false
	}
	// 只在真的变了时写库并重载缓存：按需刷新会反复调用，标题没变时
	// 一次 UPDATE + Reload 都是白费。
	res, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET title=? WHERE bot_id=? AND chat_id=? AND title<>?`,
		title, b.BotID(), chatID, title)
	if err != nil {
		slog.Warn("群标题刷新失败", "bot", b.BotID(), "chat", chatID, "err", err)
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false
	}
	if err := b.Cache.Reload(); err != nil {
		slog.Error("群标题刷新后重载缓存失败",
			"bot", b.BotID(), "chat", chatID, "err", err)
		return false
	}
	slog.Info("群标题已刷新", "bot", b.BotID(), "chat", chatID, "title", title)
	return true
}
