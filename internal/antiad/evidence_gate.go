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
// 防线有两层。第一层是提示词（17 条 / 冷判定 11 条）：判为广告必须给
// evidence 数组——从判定对象原文**逐字摘出**的关键词，可多项。第二层是
// 这里的机械核对：证据必须能在判定对象字段（正文、quoted、资料、
// bio_links、上下文、命中规则）里逐字找到，判词说「载荷在引用里」时
// state 里必须有 quoted，判词里出现的域名/@用户名/链接引用也得真实存在。
// 对不上说明判词在描写一个不存在的输入，结论按未定放行——
// 宁可漏判，不按编造的证据处罚。放行的条目在流水与复查卡片里
// 带前缀说明，管理员看得见为什么。
//
// reason 本身可以概括、可以变形还原（那是给人看的说明）：核对只咬
// evidence 与硬 token 两条通道，不咬判词措辞——原文写「看煮页」、
// 判词写「看主页」不算不一致，只要 evidence 摘的是「看煮页」。

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
//
// 复判路径（judgeLLM）不走这里 —— 它拿到 misses 后先给模型一次改正机会，
// 重试仍不符才降级（见 judgeLLM 的改正重试）。这个组合入口留给不经重试的
// 调用方与测试。
func evidenceGate(st adState, v adVerdict) adVerdict {
	if misses := evidenceMisses(st, v); len(misses) > 0 {
		slog.Warn("反广告：复判理由与输入不符，按未定放行",
			"chat", st.Chat.ID, "uid", st.Sender.UserID, "不符项", misses)
		return evidenceDemote(v, misses)
	}
	return v
}

// evidenceMisses 返回判词与判定对象对不上的地方；空表示通过。
func evidenceMisses(st adState, v adVerdict) []string {
	if !v.IsAd {
		return nil
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

	// 门二：判词里的硬证据 token（域名、t.me 引用、@用户名、电话模样的
	// 长数字串）必须能在判定对象里找到。
	for _, tok := range evidenceTokenRe.FindAllString(stripInvisible(v.Reason), -1) {
		key := evidenceKey(tok)
		if key == "" || promptEvidenceVocab[key] {
			continue
		}
		if !strings.Contains(corpusNorm, key) {
			misses = append(misses, "判词引用了「"+tok+"」，判定对象中不存在")
		}
	}

	// 门三：核对模型声明的证据（evidence，提示词要求逐字摘自判定对象原文，
	// 可多项）。判为广告却给不出证据、或给出的「原文」在判定对象里找不到，
	// 都说明判词在描写一个不存在的输入。
	//
	// 判词正文（reason）可以概括、变形还原——那是给人看的说明，不再逐字
	// 核对（旧做法从判词里抽引号段核对，会把「原文写看煮页、判词写看主页」
	// 这类还原误拦）；判广告的依据是否真实存在，由 evidence 这条通道保证。
	if len(v.Evidence) == 0 {
		misses = append(misses, "判为广告但没有声明证据（evidence 为空）")
	}
	for _, ev := range v.Evidence {
		// 摘录带不带引号都算同一份原文；零宽与空白照例归一化。
		seg := strings.Trim(strings.ToLower(stripInvisible(ev)),
			"「」『』\"“”‘’'")
		if seg == "" {
			continue
		}
		if strings.Contains(corpusNorm, seg) {
			continue
		}
		// 严格比对不上再走一遍「只留字符」的宽松口径：广告文案靠零宽字符与
		// 怪标点拆词（「　急‌招‌‧拍·照‍📷⁠　​日⁠结百左右」），模型摘录时
		// 通常归一成常规写法。容忍标点与表情差异、要求字符顺序一致——
		// 换字（看煮页→看主页）仍然对不上。
		if sq := squashEvidence(seg); sq != "" &&
			strings.Contains(squashEvidence(corpus), sq) {
			continue
		}
		misses = append(misses, "声明的证据「"+ev+"」在判定对象中不存在")
	}
	return misses
}

// squashEvidence 只留字母、数字与表意文字，去掉标点、表情与空白。
// 用于证据核对的宽松口径（见 evidenceMisses 门三）。
func squashEvidence(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// evidenceDemote 把结论降为未定并写明不符项，理由保留在后便于对照。
func evidenceDemote(v adVerdict, misses []string) adVerdict {
	shown := strings.Join(misses, "；")
	if len(misses) > 3 {
		shown = strings.Join(misses[:3], "；") + " 等"
	}
	v.IsAd, v.Kind, v.Scope, v.Severity = false, "none", "message", 0
	v.Reason = "（判词与输入不符，按未定放行待人工复核：" + shown + "）" + v.Reason
	return v
}
