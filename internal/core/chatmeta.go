package core

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// RefreshChatMeta 用本 bot 的 token 查一次群的标题与公开 @ 名并写回
// bot_chats。
//
// 这两样只在添加群那一刻抓一次，而先加配置、后把 bot 拉进群是合法的
// 操作顺序：那时 getChat 还看不到这个群，它们就永久空着，面板与 Mini App
// 只能显示裸 chat_id。启动回填与收到群消息时的按需刷新都走这里。
// 返回是否真的把新值写进了库。
func (b *Bot) RefreshChatMeta(chatID int64) bool {
	raw, err := b.TG.Call("getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return false
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Title    string `json:"title"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &resp) != nil || !resp.OK {
		return false
	}
	return b.SetChatMeta(chatID, resp.Result.Title, resp.Result.Username)
}

// SetChatMeta 用已经拿到的标题与 @ 名写回 bot_chats，不发 TG 调用：群消息
// 本身带着这两个字段，比再发一次 getChat 省一次往返。空值不覆盖已有。
// 返回是否真的改动了。
func (b *Bot) SetChatMeta(chatID int64, title, username string) bool {
	title, username = strings.TrimSpace(title), strings.TrimSpace(username)
	if title == "" && username == "" {
		return false
	}
	// 只在真的变了时写库并重载缓存：按需刷新会反复调用，值没变时一次
	// UPDATE + Reload 都是多余开销。
	res, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET
			title=CASE WHEN ?<>'' THEN ? ELSE title END,
			username=CASE WHEN ?<>'' THEN ? ELSE username END
		WHERE bot_id=? AND chat_id=?
			AND ((?<>'' AND title<>?) OR (?<>'' AND username<>?))`,
		title, title, username, username, b.BotID(), chatID,
		title, title, username, username)
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
	slog.Info("群元信息已刷新", "bot", b.BotID(), "chat", chatID,
		"title", title, "username", username)
	return true
}
