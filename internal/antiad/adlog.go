package antiad

import (
	"log/slog"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/tg"
)

// logAd 落一条判定流水，返回自增 id（失败返回 0）。
// clean 也要记：否则算不出真实开销，形态总结也拿不到样本量基数。
func logAd(b *core.Bot, m *tg.Message, v adVerdict, action, reason string) int64 {
	verdict := "clean"
	if v.IsAd {
		verdict = "ad"
	}
	switch v.Decider {
	case "":
		verdict = "error"
	case adDeciderSkipped:
		verdict = "skipped" // 没送检，不是判定失败
	}
	note := v.Reason
	if reason != "" {
		note = strings.TrimSpace(note + " | " + reason)
	}

	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id,user_name)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Chat.ID, m.From.ID, m.MessageID,
		core.TruncateRunes(displayText(m), adTextLimit),
		verdict, v.Confidence, v.Decider, v.Kind, action, note,
		v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Cost,
		time.Now().Unix(), b.BotID(), displayUserName(m.From))
	if err != nil {
		slog.Error("反广告：流水落库失败", "chat", m.Chat.ID, "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// bumpAdHits 调整历史命中数。delta 为负时不低于 0。
func BumpAdHits(b *core.Bot, chatID, uid, delta int64) {
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET ad_hits = MAX(0, ad_hits + ?) WHERE chat_id=? AND user_id=?`,
		delta, chatID, uid); err != nil {
		slog.Error("反广告：更新命中数失败", "chat", chatID, "uid", uid, "err", err)
	}
}

// ---- 判定账本的读写 ----
//
// antiad_log 是本包写的账本（logAd），读回与订正也归本包：面板只是它的
// 消费者之一，把查询放在面板里会让账本的形状由展示需求决定。
// AdLogRow 是 antiad_log 的一行。
type AdLogRow struct {
	ID         int64
	BotID      int64
	ChatID     int64
	UserID     int64
	MessageID  int64
	Text       string
	Verdict    string
	Confidence float64
	Decider    string
	Kind       string
	Action     string
	Reason     string
	// UserName 是判定当时的昵称与用户名：广告号被处置后常改名，
	// 事后再查就对不上了。
	UserName string

	PromptTokens     int64
	CompletionTokens int64
	QuotaCost        int64
	CreatedAt        int64
}

func LoadAdLog(s *store.Store, id int64) (AdLogRow, bool) {
	var r AdLogRow
	err := s.Read.QueryRow(`SELECT id,bot_id,chat_id,user_id,message_id,text,verdict,
		confidence,decider,ad_kind,action,reason,prompt_tokens,completion_tokens,
		quota_cost,created_at,user_name FROM antiad_log WHERE id=?`, id).
		Scan(&r.ID, &r.BotID, &r.ChatID, &r.UserID, &r.MessageID, &r.Text, &r.Verdict,
			&r.Confidence, &r.Decider, &r.Kind, &r.Action, &r.Reason,
			&r.PromptTokens, &r.CompletionTokens, &r.QuotaCost, &r.CreatedAt, &r.UserName)
	if err != nil {
		return r, false
	}
	return r, true
}

func UpdateAdLog(b *core.Bot, id int64, action, reason string) {
	if _, err := b.Store.Write.Exec(
		`UPDATE antiad_log SET action=?, reason=? WHERE id=?`,
		action, reason, id); err != nil {
		slog.Error("反广告：更新流水失败", "id", id, "err", err)
	}
}
