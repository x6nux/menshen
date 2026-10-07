package antiad

import (
	"fmt"
	"html"
	"strings"
	"time"

	"menshen/internal/core"
	"menshen/internal/tg"
)

// ---- /check 的多步响应 ----
//
// 命令一收到先发一条「当前状态」，之后每完成一步（规则 → 初判 → 复判 →
// 处置）就把小节追加进同一条消息（editMessageText）。判定要跑两个模型，
// 干等一条最终结果动辄几十秒；逐步编辑让发起人看着结论逐级出炉，也顺便
// 知道现在进行到哪一步、卡在哪一级。
//
// 群内静默打开时 sendGroup 发不出消息（msgID=0），后续小节整体跳过：
// 静默的承诺是「群里一个字都不发」，编辑一条不存在的消息同样是发。

type checkProgress struct {
	b      *core.Bot
	chatID int64
	msgID  int64
	// status 是发送初始消息时查到的限制状态文本；收尾时重新查一次，
	// 有变化（这次复查上了处置/解了限制）才在结尾重发一遍。
	status string
	// kb 是当前挂在消息上的按钮。TG 的 editMessageText 不带 reply_markup
	// 时会把按钮摘掉，所以每次编辑都要原样挂回去。
	kb map[string]any
	sb strings.Builder
}

// newCheckProgress 发出初始消息：目标 + 当前生效的限制（含解除深链按钮）。
func newCheckProgress(b *core.Bot, chatID, uid int64) *checkProgress {
	p := &checkProgress{b: b, chatID: chatID}
	status, rows := RestrictionStatusText(b.Shared, b.BotID(), uid)
	if status == "" {
		status = "✅ 当前没有生效中的限制。"
	}
	p.status = status
	p.sb.WriteString("🔎 <b>复查</b> " + userLink(uid) + "\n\n" + status)
	for _, row := range rows {
		p.kb = tg.KBAppend(p.kb, [][2]string{row})
	}
	p.msgID = sendGroup(b, chatID, p.sb.String(), p.kb)
	return p
}

// step 追加一个小节并编辑消息。
func (p *checkProgress) step(section string) {
	if p.msgID == 0 {
		return
	}
	p.sb.WriteString("\n\n" + section)
	p.b.EditNoPreview(p.chatID, p.msgID, p.sb.String(), p.kb)
}

// modelShort 把模型全名 <上游名>/<模型ID> 剥成模型 ID：复查卡片是发给
// 群里看的，上游名是内部路由信息。全名本身仍按原口径记进流水与资料卡
// （换模型校准阈值时要知道是谁答的）。上游名不含 /（ValidUpstreamName
// 校验保证），剥的总是第一段；没有 / 的名字原样返回。
func modelShort(full string) string {
	if _, id, ok := strings.Cut(full, "/"); ok {
		return id
	}
	return full
}

// judgeStep 把一级判定结论编辑进进度消息。stage 为 "so"（初判）或
// "llm"（复判），结论与错误都是该级的原始值（judgeBoth 在回调之后才合并）。
//
// 复判的理由原样附上（截断）：这是复查的价值所在 —— 两级结论一致与否、
// 复判依据什么，管理员要在群里当场看到。错误只给笼统文案：/check 对全群
// 开放，上游名称与响应体不能回进群里（见 publicError）。
func (p *checkProgress) judgeStep(stage string, v adVerdict, err error) {
	title := "② <b>初判</b>"
	if stage == "llm" {
		title = "③ <b>复判</b>"
	}
	var sb strings.Builder
	sb.WriteString(title)
	if err != nil {
		sb.WriteString("：" + html.EscapeString(publicError(err)))
		p.step(sb.String())
		return
	}
	if v.Model != "" {
		sb.WriteString("（" + html.EscapeString(modelShort(v.Model)) + "）")
	}
	sb.WriteString("：" + html.EscapeString(adWord(v.IsAd)))
	if v.IsAd && v.Kind != "" && v.Kind != "none" {
		sb.WriteString(" · " + html.EscapeString(adKindLabel(v.Kind)))
	}
	fmt.Fprintf(&sb, "，置信度 %.0f%%", v.Confidence*100)
	if v.Severity > 0 {
		fmt.Fprintf(&sb, "，危害度 %.1f", v.Severity)
	}
	if stage == "llm" && v.Reason != "" {
		sb.WriteString("\n<i>" + html.EscapeString(core.TruncateRunes(v.Reason, 120)) + "</i>")
	}
	p.step(sb.String())
}

// finish 追加最终小节、换上最终按钮，并安排到期撤回。这是进度消息的
// 唯一出口：提前结束（规则命中、通道繁忙、判定失败）也必须走它，否则
// 这条消息永远没人撤回。
func (p *checkProgress) finish(section string, kb map[string]any, ttl time.Duration) {
	if p.msgID == 0 {
		return
	}
	if kb != nil {
		p.kb = kb
	}
	p.sb.WriteString("\n\n" + section)
	p.b.EditNoPreview(p.chatID, p.msgID, p.sb.String(), p.kb)
	scheduleAlertCleanup(p.b, p.chatID, p.msgID, ttl)
}

// busy 是判定通道满载时的收尾。
func (p *checkProgress) busy(ttl time.Duration) {
	p.finish("⏳ 判定通道繁忙，请稍后再试。", nil, ttl)
}

// leaderHitSection 是硬规则（冒用国家领导人）命中时的小节：规则检查是
// 复查的第一步，命中即封禁出群、不再送模型，两级判定小节整体缺席。
func leaderHitSection(hit, where string, dryrun bool) string {
	text := fmt.Sprintf("① <b>规则检查</b>：命中硬规则（冒用国家领导人：%s 出现在%s）。",
		html.EscapeString(hit), html.EscapeString(where))
	if dryrun {
		return text + "\n🧪 演练：未真正执行。"
	}
	return text + "已直接封禁出群。"
}
