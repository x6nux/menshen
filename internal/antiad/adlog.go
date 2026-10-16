package antiad

import (
	"encoding/json"
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
	// 复判声明的证据并进理由列：面板、资料卡与申诉页都从这一列取文字，
	// 落在这里管理员事后回看时才知道当初凭什么判的（见 evidence_gate.go）。
	if len(v.Evidence) > 0 {
		note = appendNote(note, "证据："+strings.Join(v.Evidence, "、"))
	}
	if reason != "" {
		note = strings.TrimSpace(note + " | " + reason)
	}

	soJSON, llmJSON := stageJSON(v)
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id,user_name,
		 so_verdict,llm_verdict)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.Chat.ID, m.From.ID, m.MessageID,
		core.TruncateRunes(displayText(m), adTextLimit),
		verdict, logConfidence(v), v.Decider, v.Kind, action, note,
		v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Cost,
		time.Now().Unix(), b.BotID(), displayUserName(m.From),
		soJSON, llmJSON)
	if err != nil {
		slog.Error("反广告：流水落库失败", "chat", m.Chat.ID, "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// logConfidence 是写进 confidence 列的值。复判不给置信度，就用它挂着的初判
// 置信度 —— 面板与记录卡片的「置信度」列仍有意义，不至于整列变成 0%；两级
// 的原始结论另存 so_verdict / llm_verdict 两列，需要精确对照时可读它们。
func logConfidence(v adVerdict) float64 {
	if v.Prior != nil && v.Prior.Confidence > 0 {
		return v.Prior.Confidence
	}
	return v.Confidence
}

// stageJSON 把一条结论里的初判与复判原始结论分别序列化，落进流水两列。
//
//   - 走过复判（Decider 含 llm）：llm 列记最终结论，so 列记它挂的 Prior
//     （初判 / 规则命中 / 内容哈希）；没有 Prior（初判失败、复判独跑）时 so 列为空。
//   - 没走复判：这条结论本身就是初判级结论，so 列记它，llm 列为空。
func stageJSON(v adVerdict) (soJSON, llmJSON string) {
	if strings.Contains(v.Decider, "llm") {
		if v.Prior != nil {
			if b, err := json.Marshal(v.Prior); err == nil {
				soJSON = string(b)
			}
		}
		if b, err := json.Marshal(v.stage("llm")); err == nil {
			llmJSON = string(b)
		}
		return soJSON, llmJSON
	}
	if b, err := json.Marshal(v.stage(initialStageName(v.Decider))); err == nil {
		soJSON = string(b)
	}
	return soJSON, ""
}

// BumpAdHits 调整历史命中数。delta 为负时不低于 0。
func BumpAdHits(b *core.Bot, chatID, uid, delta int64) {
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET ad_hits = MAX(0, ad_hits + ?) WHERE chat_id=? AND user_id=?`,
		delta, chatID, uid); err != nil {
		slog.Error("反广告：更新命中数失败", "chat", chatID, "uid", uid, "err", err)
	}
}

// ---- 判定账本的读写 ----
//
// antiad_log 是本包写的账本：读回与订正也归本包，面板只是它的消费者之一。
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
	// 事后按当前资料查询无法还原。
	UserName string
	// SoVerdict / LlmVerdict 是初判与复判各自的原始结论（JSON，
	// 见 verdictStage）。复判不再覆盖初判，面板与复查卡片据此对照两级。
	// 历史行与未走过的级为空串。
	SoVerdict  string
	LlmVerdict string

	PromptTokens     int64
	CompletionTokens int64
	QuotaCost        int64
	CreatedAt        int64
}

// SoStage / LlmStage 解析两级原始结论；解析失败或为空返回 nil。
func (r AdLogRow) SoStage() *verdictStage  { return parseStage(r.SoVerdict) }
func (r AdLogRow) LlmStage() *verdictStage { return parseStage(r.LlmVerdict) }

func parseStage(s string) *verdictStage {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var st verdictStage
	if json.Unmarshal([]byte(s), &st) != nil {
		return nil
	}
	return &st
}

// recordConfidence 取记录里可信的置信度：优先初判那级的（复判不给置信度，
// 走复判的记录最终置信度为 0）；老记录或未走复判时用最终结论自己的。
func recordConfidence(r AdLogRow) float64 {
	if so := r.SoStage(); so != nil && so.Confidence > 0 {
		return so.Confidence
	}
	return r.Confidence
}

// recordDecision 取记录里复判给的处置意见文本（如「删除消息 + 禁言 5 分钟」）；
// 没有复判结论时返回空串。
func recordDecision(r AdLogRow) string {
	llm := r.LlmStage()
	if llm == nil {
		return ""
	}
	return stageDecision(llm.Delete, llm.Mute)
}

func LoadAdLog(s *store.Store, id int64) (AdLogRow, bool) {
	var r AdLogRow
	err := s.Read.QueryRow(`SELECT id,bot_id,chat_id,user_id,message_id,text,verdict,
		confidence,decider,ad_kind,action,reason,prompt_tokens,completion_tokens,
		quota_cost,created_at,user_name,so_verdict,llm_verdict FROM antiad_log WHERE id=?`, id).
		Scan(&r.ID, &r.BotID, &r.ChatID, &r.UserID, &r.MessageID, &r.Text, &r.Verdict,
			&r.Confidence, &r.Decider, &r.Kind, &r.Action, &r.Reason,
			&r.PromptTokens, &r.CompletionTokens, &r.QuotaCost, &r.CreatedAt, &r.UserName,
			&r.SoVerdict, &r.LlmVerdict)
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
