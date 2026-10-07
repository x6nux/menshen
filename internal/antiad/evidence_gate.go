package antiad

// ---- 判词-输入一致性门 ----
//
// 实测误判（2026-10，antiad_log #21276 与 #26266）：复判模型在 prior 的
// 引导下给 is_ad=true 配上了凭空编造的理由——留底正文只有「适合你自己的」
// 「现在不怕CF封了 无限邮箱 小号整起！」，判词却写着「载荷全在外部引用的
// 贴纸里（裸聊约炮招揽、ldd26.xyz、LINE 账号）」「正文「约炮」并附联系
// 方式」。这些内容在库内任何输入字段里都不存在（全库搜索只命中判词自身），
// 只能出自模型生成；提示词 4.1/6 明令不得编造，但没有程序化校验，
// 高置信度的编造照样直接删消息、禁言。
//
// 这里在复判结论落锤前做一次机械核对：判词里当**证据**引用的东西——
// 域名、t.me 引用、@用户名、电话模样的长数字串、「」『』引号段——
// 必须能在判定对象的字段（正文、quoted、资料、bio_links、上下文、
// 命中规则）里找到原文；判词说「载荷在引用里」时 state 里必须有 quoted。
// 对不上说明判词在描写一个不存在的输入，结论按未定放行——
// 宁可漏判，不按编造的证据处罚。放行的条目在流水与复查卡片里
// 带前缀说明，管理员看得见为什么。
//
// 已知取舍：提示词要求「变形还原后再判断」，理由若引用还原后的写法
// （原文写「看煮页」、判词引「看主页」）会被误拦；那类条目按未定放行、
// 留给人工复核，与「复判失败放行」同一个失败方向。

import (
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

// evidenceTokenRe 抽取判词里的硬证据 token：链接引用、@用户名、域名、
// 长数字串。域名一段刻意要求末级标签是 2~12 个字母，版本号（4.6.1）与
// 模型名（gpt-6.1）不会误中。
var evidenceTokenRe = regexp.MustCompile(
	`(?i)t\.me/[+%]?[a-z0-9_-]+|@[a-z][a-z0-9_]{3,31}|` +
		`[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.[a-z]{2,12}|\+?\d{7,}`)

// evidenceQuoteRe 抽取判词里用引号当原文引的段。
var evidenceQuoteRe = regexp.MustCompile(`[「『]([^「」『』]{2,60})[」』]`)

// stripInvisible 去掉空白与零宽字符：广告文案爱用零宽字符拆词
// （「首‌发」），判词引用时通常已剥掉；两边都归一化后才比得上。
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || (r >= 0x200b && r <= 0x200f) ||
			r == 0x2060 || (r >= 0xfe00 && r <= 0xfe0f) {
			return -1
		}
		return r
	}, s)
}

// evidenceKey 把 token 归一成比对用的形态：t.me/xxx 只留目标、@xxx 只留
// 用户名、统一小写。
func evidenceKey(tok string) string {
	t := strings.ToLower(stripInvisible(tok))
	t = strings.TrimPrefix(t, "+")
	switch {
	case strings.HasPrefix(t, "t.me/"):
		t = strings.TrimPrefix(strings.TrimPrefix(t[5:], "+"), "%")
	case strings.HasPrefix(t, "@"):
		t = t[1:]
	}
	return t
}

// promptEvidenceVocab 是提示词自身出现的 token。判词有时会复述提示词里的
// 规则原文（「t.me/joinchat 开头的私密群邀请链接几乎只用于引流」），
// 这些 token 不构成「输入中不存在」的证据，核对时跳过。
var promptEvidenceVocab = func() map[string]bool {
	m := map[string]bool{}
	for _, tok := range evidenceTokenRe.FindAllString(stripInvisible(
		soInstructions+llmSystemPrompt+pornClause+payloadClause+evasionClause+
			fpExamplesClause+bioLinksClause+serviceListClause+patternClause+
			matchedRulesClause), -1) {
		if k := evidenceKey(tok); k != "" {
			m[k] = true
		}
	}
	return m
}()

// evidenceCorpus 把判定对象的全部字段拼成一块文本，供核对。刻意不含
// known_ad_patterns / known_false_positives：提示词 4.1 禁止把样本库当
// 本条内容引用，判词引用了样本原文本身就是不一致，不该放行。
func evidenceCorpus(st adState) string {
	var sb strings.Builder
	sb.WriteString(st.Chat.Title)
	sb.WriteString("\n")
	sb.WriteString(st.Message.Text)
	if st.Quoted != nil {
		sb.WriteString("\n")
		sb.WriteString(st.Quoted.From)
		sb.WriteString("\n")
		sb.WriteString(st.Quoted.Text)
	}
	p := st.Sender
	sb.WriteString("\n")
	sb.WriteString(p.Username)
	sb.WriteString("\n")
	sb.WriteString(p.FirstName)
	sb.WriteString("\n")
	sb.WriteString(p.LastName)
	sb.WriteString("\n")
	sb.WriteString(p.Bio)
	for _, l := range p.BioLinks {
		sb.WriteString("\n")
		sb.WriteString(l.Handle)
		sb.WriteString("\n")
		sb.WriteString(l.Title)
		sb.WriteString("\n")
		sb.WriteString(l.About)
	}
	for _, c := range st.RecentContext {
		sb.WriteString("\n")
		sb.WriteString(c.Text)
	}
	for _, c := range st.ReviewHistory {
		sb.WriteString("\n")
		sb.WriteString(c.Text)
	}
	for _, r := range st.MatchedRules {
		sb.WriteString("\n")
		sb.WriteString(r.Name)
		sb.WriteString("\n")
		sb.WriteString(r.Category)
		sb.WriteString("\n")
		sb.WriteString(r.Note)
	}
	return sb.String()
}

// evidenceGate 核对判词引用的证据是否真的在判定对象里。相符时原样返回；
// 不符时把结论降为未定（IsAd=false），reason 前缀写明对不上的地方。
func evidenceGate(st adState, v adVerdict) adVerdict {
	if !v.IsAd || strings.TrimSpace(v.Reason) == "" {
		return v
	}
	var misses []string

	// 门一：判词把载荷归到「引用」头上，判定对象却根本没有 quoted 字段。
	// recent_context / review_history 里带「［引用·别人的话］」时另说：
	// 那是历史条目的引用段，判词提它不算捏造。
	corpus := stripInvisible(evidenceCorpus(st))
	corpusNorm := strings.ToLower(corpus)
	lowReason := strings.ToLower(v.Reason)
	if st.Quoted == nil && !strings.Contains(corpus, "引用") &&
		(strings.Contains(lowReason, "引用") || strings.Contains(lowReason, "quoted")) {
		misses = append(misses, "判词把载荷归到引用里，本条没有 quoted 字段")
	}

	// 门二：硬证据 token 必须能在判定对象里找到。
	for _, tok := range evidenceTokenRe.FindAllString(stripInvisible(v.Reason), -1) {
		key := evidenceKey(tok)
		if key == "" || promptEvidenceVocab[key] {
			continue
		}
		if !strings.Contains(corpusNorm, key) {
			misses = append(misses, "判词引用了「"+tok+"」，判定对象中不存在")
		}
	}

	// 门三：引号里当原文引的段同样必须在判定对象里（与门二互补：
	// 「约炮」这类纯中文捏造不走门二）。
	for _, m := range evidenceQuoteRe.FindAllStringSubmatch(v.Reason, -1) {
		seg := stripInvisible(m[1])
		if len([]rune(seg)) < 2 {
			continue
		}
		if !strings.Contains(corpusNorm, strings.ToLower(seg)) {
			misses = append(misses, "判词引用了「"+m[1]+"」，判定对象中不存在")
		}
	}

	if len(misses) == 0 {
		return v
	}
	shown := strings.Join(misses, "；")
	if len(misses) > 3 {
		shown = strings.Join(misses[:3], "；") + " 等"
	}
	slog.Warn("反广告：复判理由与输入不符，按未定放行",
		"chat", st.Chat.ID, "uid", st.Sender.UserID, "不符项", misses)
	v.IsAd, v.Kind, v.Scope, v.Severity = false, "none", "message", 0
	v.Reason = "（判词与输入不符，按未定放行待人工复核：" + shown + "）" + v.Reason
	return v
}
