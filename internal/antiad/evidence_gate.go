package antiad

// ---- 判词-输入一致性门 ----
//
// 复判模型可能在 prior 引导下给 is_ad=true 配上凭空编造的理由：判词描写的
// 载荷在判定对象的任何输入字段里都不存在，只能出自模型生成。提示词虽明令
// 不得编造，但没有程序化校验，高置信度的编造会直接导致删消息、禁言。
//
// 防线有两层。第一层是提示词（17 条 / 冷判定 11 条）：判为广告必须给
// evidence 数组——从判定对象原文逐字摘出的关键词，可多项。第二层是
// 这里的机械核对：证据必须能在判定对象字段（正文、quoted、资料、
// bio_links、上下文、命中规则）里逐字找到，判词称载荷在引用里时 state
// 里必须有 quoted，判词里出现的域名/@用户名/链接引用也得真实存在。对不上
// 说明判词在描写一个不存在的输入，结论按未定放行，不按编造的证据处罚。
// 放行的条目在流水与复查卡片里带前缀说明，管理员能看到原因。
//
// reason 本身可以概括、可以变形还原（那是给人看的说明）：核对只咬
// evidence 与硬 token 两条通道，不咬判词措辞——只要 evidence 摘的是
// 判定对象原文，措辞的变形还原不算不一致。

import (
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

// evidenceTokenRe 抽取判词里的硬证据 token：链接引用、@用户名、域名、
// 长数字串。域名一段刻意要求末级标签是 2~12 个字母，版本号与模型名
// 不会误中。
var evidenceTokenRe = regexp.MustCompile(
	`(?i)t\.me/[+%]?[a-z0-9_-]+|@[a-z][a-z0-9_]{3,31}|` +
		`[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.[a-z]{2,12}|\+?\d{7,}`)

// invisibleRune 报告 r 是不显示、但能被用来拆词或藏载荷的字符：Unicode
// 格式类（Cf：零宽空格/连接符、双向控制、软连字符、TAG 字符等）与变体
// 选择符（含补充区 U+E0100–E01EF），外加几个已废弃的填充符。
//
// 只判 Cf 不够：变体选择符是 Mn（非空白标记），而实际攻击里用得最多的
// 正是 U+E00xx 的 TAG 与 U+E01xx 的补充区变体选择符 —— 这两段必须显式列出，
// 否则它们会原样进入送检载荷与证据核对语料。
func invisibleRune(r rune) bool {
	switch {
	case r >= 0xfe00 && r <= 0xfe0f, // 变体选择符
		r >= 0xe0100 && r <= 0xe01ef, // 变体选择符补充区
		r == 0x034f,                  // 组合字素连接符
		r == 0x180e,                  // 蒙古文元音分隔符
		r == 0x3164, r == 0xffa0:     // 韩文空白填充符
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// sanitizeVisible 剥掉不显示的字符、保留空白：交给 AI 的文本先过它。
// 攻击者惯用不可见字符拆词（「看简介」写成夹着零宽字符的形态），模型
// 既可能读漏，也可能在逐字摘录证据时抄错一位，使正确结论被证据门打回。
// 先归一化再送检，模型看到的是正常文本，摘录也就对得上。
func sanitizeVisible(s string) string {
	if !strings.ContainsFunc(s, invisibleRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if invisibleRune(r) {
			return -1
		}
		return r
	}, s)
}

// stripInvisible 去掉空白与不显示的字符：证据核对两侧都归一化后才比得上。
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || invisibleRune(r) {
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
// 规则原文，这些 token 不构成输入中不存在的证据，核对时跳过。
//
// 这里用 sanitizeVisible、不用 stripInvisible：提示词是英文，空格被剥掉后
// 相邻单词会粘成长串，域名与 t.me 链接会连上前一个词一起被抽出来
// （checker ping0.cc 抽成 checkerping0.cc），提示词里的 token 就进不了词表。
var promptEvidenceVocab = func() map[string]bool {
	m := map[string]bool{}
	for _, tok := range evidenceTokenRe.FindAllString(sanitizeVisible(
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

	// 门一：判词把载荷归到引用头上，判定对象却根本没有 quoted 字段。
	// recent_context / review_history 里带 `［引用·别人的话］` 时另说：
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
	// 可多项）。判为广告却给不出证据、或给出的原文在判定对象里找不到，
	// 都说明判词在描写一个不存在的输入。
	//
	// 判词正文（reason）可以概括、变形还原，那是给人看的说明，不逐字核对；
	// 判广告的依据是否真实存在，由 evidence 这条通道保证。
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
		// 严格比对不上再走一遍只留字符的宽松口径：广告文案靠零宽字符与
		// 特殊标点拆词，模型摘录时通常归一成常规写法。容忍标点与表情差异、
		// 要求字符顺序一致——换字仍然对不上。
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
	// 结论降为未定，处置意见一并作废：留着只会在展示与区分级 JSON 里
	// 出现「正常但删消息/禁言」的自相矛盾。
	v.Planned, v.Delete, v.Mute = false, false, muteNone
	v.Reason = "（判词与输入不符，按未定放行待人工复核：" + shown + "）" + v.Reason
	return v
}
