package antiad

import (
	"menshen/internal/core"
	"menshen/internal/store"
)

// ---- 处置矩阵 ----

// adTextLimit 是 antiad_log 里保存的原文字符上限。
// 一条几千字的广告不该把库撑大，管理员也读不完。
const adTextLimit = 1000

// severeAd 报告这条判定是否属于高危害：色情、诈骗、赌博这类内容不因发送者
// 是老成员就只删不禁 —— 老人免禁言是为了避免误伤普通聊天，不是豁免惯犯。
// 阈值可按 bot 调（antiad_severe_mute，默认 2.0），设 0 关闭。
func severeAd(b *core.Bot, snap *store.Snapshot, v adVerdict) bool {
	line := float64(snap.BotSettingInt(b.BotID(), "antiad_severe_mute", 2))
	if line <= 0 {
		return false
	}
	if v.Severity >= line {
		return true
	}
	// 分类兜底：色情/诈骗/赌博本身即高危害。初判还要置信度够（低置信初判
	// 的分类不可信）；复判（Planned）已直接判了广告并给了处置意见，分类按其
	// 结论采信，不再要求置信度。
	if !v.Planned && v.Confidence < 0.8 {
		return false
	}
	switch v.Kind {
	case "porn", "porn_bait", "scam", "gambling":
		return true
	}
	return false
}

// adAccepted 报告一条结论是否过了本路径的采信线。
//
// 复判（Planned）的处置意见本身就是采信依据：joinOnly 表示这条路径只有禁言
// 一种处置（进群资料检查、前置号复核没有可删的消息），要给出禁言才算命中。
// 初判与本地合成结论没有处置意见，仍比置信度是否达到 line（百分数）。
func adAccepted(v adVerdict, line float64, joinOnly bool) bool {
	if !v.IsAd {
		return false
	}
	if !v.Planned {
		return v.Confidence*100 >= line
	}
	if joinOnly {
		return v.Mute != muteNone
	}
	return v.Delete || v.Mute != muteNone
}

// muteConfLine 返回本 bot 的禁言置信度下限（百分数）。终判要禁言/封禁
// 时置信度必须达到它，否则降级为只删/仅告警。
func muteConfLine(b *core.Bot, snap *store.Snapshot) float64 {
	return float64(snap.BotSettingInt(b.BotID(), "antiad_mute_conf", store.DefaultMuteConf))
}

// belowMuteConf 报告这个终判的置信度是否低于禁言线。
func belowMuteConf(b *core.Bot, snap *store.Snapshot, v adVerdict) bool {
	return v.Confidence*100 < muteConfLine(b, snap)
}

// decideAction 按模型给的处置意见或置信度决定处置强度。
//
// 复判（Planned）自带 delete / mute，直接照它定档：delete 决定删不删这条
// 消息，mute 决定禁言时长（5 分钟或永久）。初判与本地合成结论没有处置意见，
// 仍走置信度矩阵（见 decideTier）并过禁言置信度下限。
//
// 无论哪条路，老人都不自动禁言（高危害除外，见 severeAd）：误伤一个长期
// 成员的社交代价远大于漏一条广告，告警里带一键处置按钮即可。
//
// 置信度下限（antiad_mute_conf）只作用于置信度矩阵那条路：它是为了防止模型
// 偶尔给出 is_ad=true、confidence=0 却写着正常讨论的结论直接删消息+禁言一个
// 普通人。复判不给置信度，改用它的 delete/mute 意见本身作依据。
func decideAction(b *core.Bot, snap *store.Snapshot, newbie bool, v adVerdict) adAction {
	if !v.IsAd {
		return adAction{Name: "none"}
	}
	if v.Planned {
		return plannedAction(b, snap, newbie, v)
	}
	act := decideTier(b, snap, newbie, v)
	if (act.Mute || act.Ban) && belowMuteConf(b, snap, v) {
		act.Mute, act.Ban, act.Purge = false, false, false
		switch {
		case act.Delete:
			act.Name = "deleted"
		case act.Alert:
			act.Name = "alerted"
		default:
			act.Name = "none"
		}
	}
	return act
}

// plannedAction 按复判给的处置意见定档。老人免禁言兜底照旧（高危害除外）：
// 那是误伤成本与社会代价的底线，与定档依据无关。
func plannedAction(b *core.Bot, snap *store.Snapshot, newbie bool, v adVerdict) adAction {
	act := adAction{Delete: v.Delete, Purge: v.Delete && v.Scope == "account"}
	switch v.Mute {
	case muteShort:
		act.Mute, act.MuteFor = true, tempMute
	case muteForever:
		act.Mute, act.MuteFor = true, mutePermanent
	}
	// 账号本身就是广告号：这个账号的存在目的就是发广告，短暂禁言没有意义，
	// 一律按永久处理 —— 不依赖模型在 5m/forever 之间选对档。
	if act.Mute && v.Scope == "account" && act.MuteFor != mutePermanent {
		act.MuteFor = mutePermanent
	}
	if act.Mute && !newbie && !severeAd(b, snap, v) {
		act.Mute, act.MuteFor = false, 0
	}
	// 判为广告就发告警：即便复判说不动手（delete=false、mute=none），也让
	// 管理员看见并自行处置 —— 与旧的「仅告警」档同义。
	act.Alert = true
	act.Name = actionName(act)
	return act
}

// actionName 由动作组合推出流水里的档名，与既有取值保持一致
// （none / alerted / deleted / muted / deleted_muted；封禁模式另由 withPunish 改写）。
func actionName(act adAction) string {
	switch {
	case act.Delete && act.Mute:
		return "deleted_muted"
	case act.Delete:
		return "deleted"
	case act.Mute:
		return "muted"
	case act.Alert:
		return "alerted"
	}
	return "none"
}

// decideTier 是定档本体，不含禁言置信度下限（见 decideAction）：下限作用
// 在两套定档方式之上。
func decideTier(b *core.Bot, snap *store.Snapshot, newbie bool, v adVerdict) adAction {
	// 按模型结论定档：只看模型的是/否结论，不看置信度。大模型在同一类内容
	// 上的置信度抖动很大，卡死硬线会让明显是广告的内容时而只删、时而禁言。
	// 老人仍只删不禁——那是误伤成本和社会代价的底线，与本开关无关；高危害
	// 内容除外（severeAd）。
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
		// 老成员误判的代价与自动禁言同理，且删除无法挽回。
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
