package antiad

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// ---- 按群白名单（/adw）----
//
// 全局豁免在 antiad_exempt_users（面板上按 bot 配）；这里是群管理员在群里
// 一句话就能加的、只对本群生效的那一份，落在 group_members.whitelisted。

const adwUsage = "用法：回复某人的消息发 <code>/adw</code>，" +
	"或发 <code>/adw &lt;user_id&gt;</code>，把此人加入本群反广告白名单。" +
	"频道填 -100 开头的频道 ID。"

// HandleAdwCommand 处理群内 /adw。与 /adb 同一道门：白名单等于对此人关掉
// 反广告，只有群管理员及以上能用，非授权者静默忽略。
func HandleAdwCommand(b *core.Bot, conf store.BotChat, m *tg.Message, arg string) {
	if !canMarkAd(b, conf.ChatID, m.From.ID) {
		return
	}
	ttl := time.Duration(b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_alert_ttl", 300)) * time.Second
	reply := func(text string) {
		scheduleAlertCleanup(b, conf.ChatID, b.SendGetID(conf.ChatID, text, nil), ttl)
	}

	var uid int64
	switch {
	case m.ReplyToMessage != nil && m.ReplyToMessage.From != nil:
		uid = senderOf(m.ReplyToMessage).ID
	case arg != "":
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id == 0 {
			reply(adwUsage)
			return
		}
		uid = id
	default:
		reply(adwUsage)
		return
	}

	if err := setGroupWhitelist(b, conf.ChatID, uid, true); err != nil {
		reply("加入白名单失败，请稍后再试。")
		return
	}
	// 只写 ID 链接不写昵称，理由同 userLink。
	reply("🤍 已将 " + userLink(uid) + " 加入本群反广告白名单。")
}

// isGroupWhitelisted 报告此人是否在该群的反广告白名单（/adw）里。
func isGroupWhitelisted(b *core.Bot, chatID, uid int64) bool {
	var w int
	b.Store.Read.QueryRow(`SELECT whitelisted FROM group_members
		WHERE chat_id=? AND user_id=?`, chatID, uid).Scan(&w)
	return w == 1
}

// setGroupWhitelist 把此人加入或移出该群白名单。从没发过言的人也能加：
// 行不存在时插入一行，first_seen 记为此刻，msg_count 从 0 起。
func setGroupWhitelist(b *core.Bot, chatID, uid int64, on bool) error {
	w := 0
	if on {
		w = 1
	}
	_, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits,whitelisted)
		VALUES (?,?,0,?,0,0,0,?)
		ON CONFLICT(chat_id,user_id) DO UPDATE SET whitelisted=excluded.whitelisted`,
		chatID, uid, time.Now().Unix(), w)
	if err != nil {
		slog.Error("反广告：更新白名单失败", "chat", chatID, "uid", uid, "err", err)
	}
	return err
}
