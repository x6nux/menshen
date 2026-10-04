package antiad

import (
	"menshen/internal/core"
	"menshen/internal/store"
)

// ---- 处置矩阵 ----

// adTextLimit 是 antiad_log 里保存的原文字符上限。
// 一条几千字的广告不该把库撑大，管理员也读不完。
const adTextLimit = 1000

// severeAd 报告这条判定够不够「高危害」：色情、诈骗、赌博这类内容不因为
// 「他是老成员」就只删不禁 —— 老人免禁言是为了避免误伤普通聊天，不是给
// 惯犯留豁免。阈值可按 bot 调（antiad_severe_mute，默认 2.0），设 0 关闭。
//
// 复判结论里没有危害度（复判 JSON 不含 severity），所以再按分类兜底一次，
// 但要置信度够 —— 只有「分类是色情/诈骗/赌博 且 有把握」才算高危害。
func severeAd(b *core.Bot, snap *store.Snapshot, v adVerdict) bool {
	line := float64(snap.BotSettingInt(b.BotID(), "antiad_severe_mute", 2))
	if line <= 0 {
		return false
	}
	if v.Severity >= line {
		return true
	}
	if v.Confidence < 0.8 {
		return false
	}
	switch v.Kind {
	case "porn", "porn_bait", "scam", "gambling":
		return true
	}
	return false
}

// decideAction 按「用户风险档 × 置信度」决定处置强度。
//
//	置信度 ≥ hard(90%)      新人: 删 + 禁言 + 告警    老人: 删 + 告警
//	soft(75%) ≤ 置信 < hard 新人: 删 + 告警           老人: 仅告警
//	置信度 < soft           都不处置
//
// 老人不自动禁言：误伤一个长期成员的社交代价远大于漏一条广告，告警里给
// 管理员一键补刀的按钮就够了。**例外是高危害内容**（见 severeAd）：色情、
// 诈骗、赌博这类不因为「他是老成员」就放过。
//
// 两条线都可以按 bot 覆盖，归 owner 调 —— 他最清楚自己的群该多严。
func decideAction(b *core.Bot, snap *store.Snapshot, newbie bool, v adVerdict) adAction {
	if !v.IsAd {
		return adAction{Name: "none"}
	}
	// 「按模型结论定档」：只看模型的是/否结论，不看置信度。大模型在同一类
	// 内容上的置信度抖动很大（同一条广告可能 88% 也可能 95%），卡 90% 硬线
	// 会让「明显是广告」的内容时而只删、时而禁言。老人仍只删不禁——那是
	// 误伤成本和社会代价的底线，与本开关无关；高危害内容除外（severeAd）。
	if snap.BotSettingInt(b.BotID(), "antiad_bool_verdict", 0) == 1 {
		if newbie || severeAd(b, snap, v) {
			return adAction{Delete: true, Mute: true, Alert: true,
				Purge: v.Scope == "account", Name: "deleted_muted"}
		}
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	}
	id := b.BotID()
	conf := v.Confidence * 100
	hard := float64(snap.BotSettingInt(id, "antiad_act_hard", 90))
	soft := float64(snap.BotSettingInt(id, "antiad_act_soft", 75))

	switch {
	case conf >= hard && (newbie || severeAd(b, snap, v)):
		// 账号本身就是广告号时连带删掉此人近期的全部消息。只在这一档：
		// 老成员误判的代价与自动禁言同理，删光了更是没法挽回。
		return adAction{Delete: true, Mute: true, Alert: true,
			Purge: v.Scope == "account", Name: "deleted_muted"}
	case conf >= hard:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft && newbie:
		return adAction{Delete: true, Alert: true, Name: "deleted"}
	case conf >= soft:
		return adAction{Alert: true, Name: "alerted"}
	}
	return adAction{Name: "none"}
}
