package antiad

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/upstream"
)

// adVerdict 是一次判定的结果。
type adVerdict struct {
	IsAd       bool
	Confidence float64 // 0..1；初判（systemone）与本地合成结论会给，复判不给
	Kind       string
	Reason     string
	Decider    string // systemone / llm / systemone+llm / rule:<id> / hash / manual …
	// Model 是给出这条结论的那个模型名。Decider 只说走了几级，说不出
	// 具体是谁 —— 而换模型后校准阈值，第一件事就是知道眼前这条判定
	// 出自哪个模型。两级都跑时记最终拍板的那个。
	Model string
	// Scope 是广告出在哪：account = 账号本身就是广告号（资料写着广告），
	// message = 只有这条消息是。前者连带删除此人近期的全部消息；后者的内容
	// 会记成哈希，同样的内容再出现直接删（见 hash.go）。
	Scope string
	// Severity 是危害度（0-3，可带小数）：初判给分值，复判也可给。
	Severity float64
	// Planned 表示这条结论自带处置意见（复判给的 delete / mute），定档直接
	// 照它，不再走置信度矩阵。初判与本地合成结论不给，仍按置信度定档。
	Planned bool
	// Delete 是复判给的处置意见：是否删除这条消息。无消息可删的路径
	// （进群资料检查、前置号复核）恒为 false，只走 Mute。
	Delete bool
	// Mute 是复判给的禁言时长：muteNone / muteShort（5 分钟）/ muteForever。
	// 空串按 muteNone 处理。
	Mute string
	// ProfileOKHours 是复判给的资料临时放行时长（1~72 小时，0 = 不放行）。
	// 只有复判判为正常、且可疑的只是资料本身时才会给（见 profile_ok.go）。
	ProfileOKHours int
	// Evidence 是复判模型声明判广告的证据：从判定对象原文逐字摘出的
	// 关键词/短语，可多项。一致性门（evidence_gate.go）拿它与判定对象
	// 核对，流水与复查卡片也展示它——管理员不用再从判词里猜依据。
	Evidence []string
	// Prior 是升级复判前那一级的原始结论（初判 / 规则命中 / 内容哈希）。
	// 只用于两级分开落库与展示（见 verdictStage）；定档只用最终这一级。
	Prior *verdictStage
	Usage billing.Usage
	Cost  int64 // quota
}

// 复判给的禁言时长取值。
const (
	muteNone    = "none"
	muteShort   = "5m"
	muteForever = "forever"
)

// decisionText 渲染一条结论里的处置意见（复判给的 delete / mute）。判为广告
// 但未动手时给「不处置」；非复判结论没有处置意见，返回空串。
func decisionText(v adVerdict) string {
	if !v.Planned {
		return ""
	}
	if d := stageDecision(v.Delete, v.Mute); d != "" {
		return d
	}
	return "不处置"
}

// stageDecision 渲染处置意见；没删也没禁言返回空串。
func stageDecision(del bool, mute string) string {
	var parts []string
	if del {
		parts = append(parts, "删除消息")
	}
	switch mute {
	case muteShort:
		parts = append(parts, "禁言 5 分钟")
	case muteForever:
		parts = append(parts, "永久禁言")
	}
	return strings.Join(parts, " + ")
}

// verdictStage 是某一级判定的原始结论。初判与复判各存一份，避免复判覆盖掉
// 初判 —— antiad_log 的 so_verdict / llm_verdict 两列据此落库，面板与复查
// 卡片据此对照两级。
type verdictStage struct {
	Stage      string   `json:"stage"` // so | llm | rule | hash | manual | shape | leader
	IsAd       bool     `json:"is_ad"`
	Confidence float64  `json:"confidence,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Severity   float64  `json:"severity,omitempty"`
	Delete     bool     `json:"delete,omitempty"`
	Mute       string   `json:"mute,omitempty"`
	Decider    string   `json:"decider,omitempty"`
	Model      string   `json:"model,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// stage 把这一级结论转成可落库、可展示的形态。stage 名由调用方按来源给。
func (v adVerdict) stage(stage string) verdictStage {
	return verdictStage{
		Stage: stage, IsAd: v.IsAd, Confidence: v.Confidence, Kind: v.Kind,
		Scope: v.Scope, Severity: v.Severity, Delete: v.Delete, Mute: v.Mute,
		Decider: v.Decider, Model: v.Model, Evidence: v.Evidence, Reason: v.Reason,
	}
}

// buildSystemOneReq 构造 jev 请求体。
//
// instructions 由调用方给：消息判定与进群冷判定看的是同一份 state 结构，
// 但问题不同（消息判定 vs 进群冷判定），提示词必须分开。
//
// 不带 model 字段：模型名是 aiCall 按当前尝试填的（要剥掉上游前缀）。
func buildSystemOneReq(st adState, instructions string) map[string]any {
	return map[string]any{
		"state": st,
		"questions": map[string]any{
			// criteria 的值不要留 nil：那等于让模型自己猜两个选项的边界在哪，
			// 而账号资料就是广告位、变形词还原后才成立这两类恰恰是
			// 它猜不到、必须明写的。
			"is_ad": map[string]any{
				"type":         "choice",
				"instructions": instructions,
				"criteria": map[string]any{
					"ad": "promotion, traffic-driving, solicitation or scam wording; or a nickname/bio " +
						"carrying promo copy, earnings promises or traffic-driving lines, or a linked " +
						"channel/group that is itself an ad; or explicit pornographic content (sexual " +
						"description, sexual violence, incest, involving minors) even without contact " +
						"details or ad copy; or anything that de-obfuscates to any of the above",
					"clean": "normal conversation, including sharing technical links, discussing product " +
						"prices, forwarding news",
				},
			},
			"ad_kind": map[string]any{
				"type":         "choice",
				"instructions": "If it is an ad, which category is it? Category must not be none when judging advertising; choose none only when it is not an ad.",
				"criteria": map[string]any{
					"none":   "not an ad",
					"crypto": "cryptocurrency, coin trading, airdrops, exchange referrals",
					"porn":   "porn solicitation: hookups, nude chat, door-to-door services, porn site or porn group promotion",
					"porn_bait": "pornographic content: explicit sexual description, sexual violence, incest, " +
						"pornographic text involving minors or clickbait titles (「看完整版」「点击观看」), " +
						"even with no contact detail or ad copy",
					"gambling":   "gambling, betting, lottery",
					"scam":       "scams, including part-time brushing, 「日入」 pitches, pig-butchering, fake customer service, repayment on one's behalf",
					"promo":      "goods or service promotion, group-invite traffic-driving, driving traffic to DMs or a profile",
					"spam_flood": "flooding with no substantive content",
				},
			},
			// 决定删一条还是删光：账号本身就是广告号的，此人近期发的都是铺垫。
			"ad_scope": map[string]any{
				"type":         "choice",
				"instructions": "If it is an ad, does the problem lie in the account or only in this message?",
				"criteria": map[string]any{
					"account": "the account itself is an ad account: username, nickname or bio carries " +
						"promo copy, earnings promises, traffic-driving lines or porn solicitation, or a " +
						"linked channel/group is itself an ad (see bio_links)",
					"message": "only this message's content is an ad and the account profile itself is " +
						"normal; choose this also when it is not an ad",
				},
			},
			"severity": map[string]any{
				"type":         "score",
				"instructions": "Harm level: 0 = harmless, 3 = scam, explicit porn or mass flooding.",
				// score 题的 criteria 必须是有序列表，下标即分值；缺失或写成
				// map 上游直接 422。4xx 不重试，主判随之静默回落成只有大模型。
				"criteria": []string{"harmless", "mild promotion", "clear traffic-driving", "scam, explicit porn or mass flooding"},
			},
		},
	}
}

// soAnswer 是 jev answers 里单个问题的通用形状。
type soAnswer struct {
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Score      float64 `json:"score"`
}

// judgeSystemOne 用 jev 做主判定。
//
// 模型走 modelsFor：主管理员配一份全局默认，也可以为单个 bot 单独指定。
// 次级管理员调不动它 —— 模型直接决定判定质量与花掉多少钱。
func judgeSystemOne(b *core.Bot, snap *store.Snapshot, st adState, instructions string) (adVerdict, error) {
	soModels, _ := snap.ModelsFor(b.BotID())
	if len(soModels) == 0 {
		return adVerdict{}, fmt.Errorf("反广告：未配置判定模型（antiad_so_models）")
	}
	// 微调初判模型（如 laya）不吃长口径：开关打开时把 is_ad 的 instructions
	// 换成一句话（soInstructionsSimple）。只动初判载荷，复判提示词不受影响；
	// both 作用域，单 bot 可覆盖 —— 微调模型通常只挂在个别 bot 上。
	if snap.BotSettingInt(b.BotID(), "antiad_so_simple_instructions", 0) == 1 {
		instructions = soInstructionsSimple
	}
	// 送检前统一剥掉不可见字符：载荷与证据核对语料用同一份（见 sanitized）。
	st = st.sanitized()

	reply, err := aiCall(b.Shared, upstream.EPSystemOne, soModels,
		buildSystemOneReq(st, instructions), upstreamNotifier(b))
	if err != nil {
		return adVerdict{}, err
	}

	var resp struct {
		Answers map[string]soAnswer `json:"answers"`
	}
	if err := json.Unmarshal(reply.Raw, &resp); err != nil {
		return adVerdict{}, fmt.Errorf("反广告：systemone 响应无法解析: %w", err)
	}
	isAd, ok := resp.Answers["is_ad"]
	if !ok || isAd.Choice == "" {
		// 模型换版本、模型名配错都会走到这里。必须报错而不是当成 clean，
		// 否则整个功能静默失效，运维只看到一条都没拦到。
		return adVerdict{}, fmt.Errorf("反广告：systemone 未返回 is_ad")
	}

	v := adVerdict{
		IsAd:       isAd.Choice == "ad",
		Confidence: isAd.Confidence,
		Kind:       resp.Answers["ad_kind"].Choice,
		Scope:      resp.Answers["ad_scope"].Choice,
		Severity:   resp.Answers["severity"].Score,
		Decider:    "systemone",
		Model:      reply.Model,
		Usage:      reply.Usage,
		Cost:       reply.Cost,
	}
	v.Reason = fmt.Sprintf("判定 %s 置信度:%.0f%%，危害度:%.1f",
		soChoiceLabel(isAd.Choice), isAd.Confidence*100, v.Severity)
	// is_ad=true 却把 ad_kind 选成 none，是模型自相矛盾（问题里明写
	// `判为广告时不得选 none`）。按模型结论定档只看 is_ad，照它会删消息、禁言、
	// 连带删除并把正文记成哈希；说不出广告类别就不算广告，与 judgeLLM
	// 里同一条兜底保持同一口径。
	if v.IsAd && v.Kind == "none" {
		slog.Info("反广告：systemone 返回 is_ad=ad 但 ad_kind=none，按正常处理",
			"置信度", v.Confidence)
		v.IsAd = false
	}
	return v, nil
}

// soChoiceLabel 把 systemone 的 is_ad 选项翻成中文标签。decider 不写进
// 这句：来源在记录卡片上另有字段，理由里重复只是冗余。模型返回名单外的
// 值时原样带出 —— 归为正常会掩盖异常。
func soChoiceLabel(choice string) string {
	switch choice {
	case "ad":
		return "广告"
	case "clean":
		return "正常"
	}
	return choice
}

// judgeLLM 用大模型复判，sysPrompt 由调用方给（消息判定与进群冷判定各一份）。
//
// prior 是上一级的结论（systemone 初判 / 命中规则 / 内容哈希）。只有内容
// 哈希那一档会进载荷：另外两档是别的判定的输出，传过去会把复判锚定在它
// 上面（见下面 prior_verdict 的注释）。调用方无论哪一档都传入上一级结论，
// 由这里统一决定是否放行 —— 策略只写在一处。
func judgeLLM(b *core.Bot, snap *store.Snapshot, st adState, prior adVerdict,
	sysPrompt string) (adVerdict, error) {

	_, llmModels := snap.ModelsFor(b.BotID())
	if len(llmModels) == 0 {
		return adVerdict{}, fmt.Errorf("反广告：未配置复判模型（antiad_llm_models）")
	}
	// 送检前统一剥掉不可见字符：载荷与下面的证据核对语料用同一份（见
	// sanitized）—— 否则模型摘的是归一化后的原文，核对拿未归一化的语料
	// 去比，正确结论会被证据门打回。
	st = st.sanitized()

	payload := map[string]any{
		"state": st,
	}
	// prior_verdict 只传内容哈希那一档：它不是模型的意见，是本服务自己的
	// 账本（同样的内容此前已判为广告），对复判是硬证据。
	//
	// systemone（jev）与命中规则的结论不传：那是另一个判定的输出，
	// 传过去只会把复判锚定在它上面 —— 会让模型顺着 prior 编造理由，
	// 也使两模型独立互证变成复判复述初判，复查失去交叉验证的意义。
	// 规则命中的证据本来就以 matched_rules 出现在 state 里，不需要再借
	// prior 说一遍。
	if prior.Decider == deciderHash {
		payload["prior_verdict"] = map[string]any{
			"is_ad": prior.IsAd, "confidence": prior.Confidence,
			"kind": prior.Kind, "by": prior.Decider,
		}
	}
	userContent, err := json.Marshal(payload)
	if err != nil {
		return adVerdict{}, err
	}

	req := map[string]any{
		// temperature 必须为 0：判定要可复现，同一条消息两次判出不同结果
		// 会让管理员完全无法校准阈值。模型不接受该参数时，BuildBody 会按
		// 模型去掉它（见 upstream/temperature.go）。
		"temperature": 0,
		// 流式才能按首字判断上游是否卡住（见 aiAttempt）；include_usage
		// 让上游在流末附上用量，否则开销无从计算。
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": string(userContent)},
		},
	}

	reply, err := aiCall(b.Shared, upstream.EPChat, llmModels, req, upstreamNotifier(b))
	if err != nil {
		return adVerdict{}, err
	}
	v, obj, err := parseLLMReply(reply)
	if err != nil {
		return adVerdict{Usage: reply.Usage, Cost: reply.Cost}, err
	}

	misses := evidenceMisses(st, v)
	if len(misses) == 0 {
		return v, nil
	}

	// 证据与判定对象对不上：给模型一次改正机会 —— 原提示词原样重发，
	// 附上它上一轮的输出与错误说明，把 evidence 必须逐字摘自判定对象的要求
	// 二次强调一遍。上游抖动导致的偶发编造与提示词漂移都在这一轮里有
	// 一次自纠的机会；重试仍不符才按未定放行（见 evidenceDemote）。
	retryReq := map[string]any{
		"temperature":    0,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages": append(slices.Clone(req["messages"].([]map[string]string)),
			map[string]string{"role": "assistant", "content": obj},
			map[string]string{"role": "user",
				"content": fmt.Sprintf(evidenceRetryNote, strings.Join(misses, "; "))}),
	}
	reply2, err2 := aiCall(b.Shared, upstream.EPChat, llmModels, retryReq, upstreamNotifier(b))
	if err2 != nil {
		// 重试没跑成不改变失败方向：仍按未定放行，但把这次尝试的花费一并记上。
		slog.Warn("反广告：证据改正重试失败，按未定放行",
			"chat", st.Chat.ID, "uid", st.Sender.UserID,
			"不符项", misses, "err", err2)
		v.Cost += reply2.Cost
		v.Usage = billing.MergeUsage(v.Usage, reply2.Usage)
		return evidenceDemote(v, misses), nil
	}
	fixed, _, err2 := parseLLMReply(reply2)
	if err2 != nil {
		slog.Warn("反广告：证据改正重试无法解析，按未定放行",
			"chat", st.Chat.ID, "uid", st.Sender.UserID, "不符项", misses, "err", err2)
		v.Cost += reply2.Cost
		v.Usage = billing.MergeUsage(v.Usage, reply2.Usage)
		return evidenceDemote(v, misses), nil
	}
	// 两次调用的钱都是判这条消息花的。
	fixed.Cost += v.Cost
	fixed.Usage = billing.MergeUsage(fixed.Usage, v.Usage)

	if m2 := evidenceMisses(st, fixed); len(m2) > 0 {
		slog.Warn("反广告：证据改正重试仍不符，按未定放行",
			"chat", st.Chat.ID, "uid", st.Sender.UserID, "不符项", m2)
		return evidenceDemote(fixed, m2), nil
	}
	return fixed, nil
}

// evidenceRetryNote 是改正重试附在末尾的用户消息：一次把要求说死。
const evidenceRetryNote = "Your previous output failed evidence verification: %s.\n" +
	"Output the complete JSON again (same fields and requirements). Pay special attention: every " +
	"entry of the evidence array must be **quoted verbatim** from the judgement targets (message.text, " +
	"quoted, sender's nickname/username/bio/bio_links, recent_context, review_history, matched_rules); " +
	"never fabricate, rewrite, summarize or de-obfuscate (if the source writes 「看煮页」 quote " +
	"「看煮页」). Judging advertising requires at least one genuinely present piece of evidence. Text " +
	"in the sample libraries (known_ad_patterns / known_false_positives) is not a judgement target and " +
	"must not be quoted into evidence."

// parseLLMReply 把上游响应解析成复判结论。返回值里的 obj 是模型这次输出的
// JSON 原文，改正重试要把它作为上一轮的 assistant 消息回传。
//
// 只解析、不做证据核对：核对与改正重试的编排在 judgeLLM 里，那里才知道
// 还能不能再给模型一次机会。
func parseLLMReply(reply aiReply) (adVerdict, string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(reply.Raw, &resp); err != nil || len(resp.Choices) == 0 {
		return adVerdict{}, "", fmt.Errorf("反广告：大模型响应无法解析")
	}
	obj := extractJSONObject(resp.Choices[0].Message.Content)
	if obj == "" {
		return adVerdict{}, "", fmt.Errorf("反广告：大模型未返回 JSON")
	}
	var out struct {
		IsAd     bool    `json:"is_ad"`
		Kind     string  `json:"kind"`
		Scope    string  `json:"scope"`
		Severity float64 `json:"severity"`
		// Delete / Mute 是复判的处置意见：是否删这条消息、禁言多久。
		Delete bool   `json:"delete"`
		Mute   string `json:"mute"`
		Reason string `json:"reason"`
		// Evidence 是模型声明的判广告证据（原文逐字摘录，见 adVerdict）。
		Evidence []string `json:"evidence"`
		// ProfileOKHours 是资料临时放行时长：只在判为正常、且可疑的
		// 只是资料本身时有意义。判为广告时一律忽略。
		ProfileOKHours int `json:"profile_ok_hours"`
	}
	if err := json.Unmarshal([]byte(obj), &out); err != nil {
		return adVerdict{}, "", fmt.Errorf("反广告：大模型 JSON 解析失败: %w", err)
	}
	// is_ad=true 却说不出广告类别是模型自相矛盾。按模型结论定档只看
	// is_ad，照它会删消息、记哈希，故按正常处理。
	if out.IsAd && out.Kind == "none" {
		out.IsAd = false
	}
	// 判为正常时处置意见一律作废：不回删、不禁言，只可能给资料放行时长。
	del, mute := out.Delete, normalizeMute(out.Mute)
	if !out.IsAd {
		del, mute = false, muteNone
	}
	okHours := 0
	if !out.IsAd {
		okHours = clampProfileHours(out.ProfileOKHours)
	}
	// evidence 上限：最多 5 项、每项 60 字。它是核对与展示用的原文摘录，
	// 不是自由作文；空项丢掉。
	ev := make([]string, 0, len(out.Evidence))
	for _, e := range out.Evidence {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if r := []rune(e); len(r) > 60 {
			e = string(r[:60])
		}
		ev = append(ev, e)
		if len(ev) == 5 {
			break
		}
	}
	return adVerdict{
		IsAd: out.IsAd, Kind: out.Kind, Scope: out.Scope, Severity: out.Severity,
		// 复判自带处置意见：定档直接照 delete/mute，不再看置信度。
		Planned: true, Delete: del, Mute: mute,
		Reason: out.Reason, Decider: "llm", Model: reply.Model,
		ProfileOKHours: okHours, Evidence: ev, Usage: reply.Usage, Cost: reply.Cost,
	}, obj, nil
}

// normalizeMute 把复判给的禁言时长收敛到三档。模型偶尔会写近似值或中文，
// 认不出的按不禁言处理 —— 宁可漏一次禁言，也不把不能识别的字符串当处罚。
func normalizeMute(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case muteShort, "5", "5min", "5分钟":
		return muteShort
	case muteForever, "permanent", "forever_mute", "永久", "永久禁言":
		return muteForever
	}
	return muteNone
}

// extractJSONObject 从模型输出中提取首个完整的 JSON 对象。
// 模型常用 ```json 包裹、并在前面加一句说明，严格解析会失败。
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

// judgeBoth 是 /check 复查用的判定：两个模型都跑，不看采信线。
//
// 与自动判定（低置信或需处罚才升级）刻意不同 —— 复查是人主动发起的二次
// 判断，目的就是拿到两方结论互相印证；省去那一次调用等于把复查降级成
// 重判一遍。消息复查与资料复查共用，提示词由调用方按口径各给一份。
//
// report 在每级跑完后被调一次（stage 为 "so"/"llm"，带该级结论或错误），
// 调用方用它把中间结论编辑进进度消息；为 nil 时静默。回调之后才有合并
// 与兜底，所以两级看到的是各自的原始结论。
func judgeBoth(b *core.Bot, snap *store.Snapshot, st adState,
	soInstr, llmPrompt string, report func(stage string, v adVerdict, err error)) (adVerdict, error) {

	so, soErr := judgeSystemOne(b, snap, st, soInstr)
	if report != nil {
		report("so", so, soErr)
	}
	// 即使 soErr 非空也照样跑大模型：so 此时是零值，judgeLLM 会自动
	// 略过 prior_verdict，退化成只有大模型的判定而不是整体失败。
	llm, llmErr := judgeLLM(b, snap, st, so, llmPrompt)
	if report != nil {
		report("llm", llm, llmErr)
	}

	switch {
	case soErr != nil && llmErr != nil:
		return adVerdict{}, fmt.Errorf("systemone: %v; llm: %v", soErr, llmErr)
	case llmErr != nil:
		// 复查同理：低于采信线的初判不因为大模型没跑成就变成可处置的。
		if demoteUnconfirmed(b, snap, &so, "大模型复查失败："+llmErr.Error()) {
			slog.Warn("反广告：大模型复查失败且初判低于采信线，按未定放行",
				"置信度", so.Confidence, "err", llmErr)
			return so, nil
		}
		so.Reason = "大模型复查失败（" + llmErr.Error() + "），仅 systemone 结论：" + so.Reason
		return so, nil
	case soErr != nil:
		llm.Reason = "systemone 失败（" + soErr.Error() + "），仅大模型结论：" + llm.Reason
		return llm, nil
	}

	// 以大模型为准（两模型独立判定，复判不再看到初判的结论 —— 见
	// judgeLLM 的 prior_verdict 说明），但两方结论都要留在
	// reason 里：复查的价值正在于让人看见两个模型是否一致。
	final := llm
	final.Decider = "systemone+llm"
	// 初判那一级挂上来，与复判分开落库/展示（见 verdictStage）。
	stg := so.stage("so")
	final.Prior = &stg
	final.Cost = llm.Cost + so.Cost
	final.Usage = billing.MergeUsage(llm.Usage, so.Usage)
	agree := "一致"
	if so.IsAd != llm.IsAd {
		agree = "分歧"
	}
	final.Reason = fmt.Sprintf("两模型%s｜systemone: %s %.0f%%｜大模型: %s %.0f%% — %s",
		agree, adWord(so.IsAd), so.Confidence*100,
		adWord(llm.IsAd), llm.Confidence*100, llm.Reason)
	return final, nil
}

func adWord(isAd bool) string {
	if isAd {
		return "广告"
	}
	return "正常"
}

// 自动判定分两段：judgeFirst 初判，needReview 决定要不要再交给大模型复判，
// review 复判。三条回落路径都必须走通，缺一条就会在某种配置下整体瘫痪：
//   - 没配 systemone → 直接用大模型
//   - systemone 低置信但没配大模型 → 采信低置信结果
//   - 大模型解析失败 → 回退采信 systemone 的初判

// judgeFirst 是初判：systemone 主判；它不可用时大模型顶上，那就已是终判。
func judgeFirst(b *core.Bot, snap *store.Snapshot, st adState) (adVerdict, error) {
	so, soErr := judgeSystemOne(b, snap, st, soInstructions)
	if soErr == nil {
		return so, nil
	}
	// 主判定器不可用：大模型顶上，不能因此整条链路瘫痪。
	//
	// 这条回落是静默的——判定照常给出结论，面板上看不出异常，
	// systemone 可能长期不可用而无人发现。因此至少留一行日志，
	// 并把回落写进 reason，让流水里可见。
	slog.Warn("反广告：systemone 不可用，回落到大模型", "err", soErr)
	v, err := judgeLLM(b, snap, st, adVerdict{}, llmSystemPrompt)
	if err != nil {
		return adVerdict{}, fmt.Errorf("systemone: %v; llm: %v", soErr, err)
	}
	v.Reason = "（systemone 不可用，仅大模型结论）" + v.Reason
	return v, nil
}

// hasLLM 报告这个 bot 配了复判模型没有。按 bot 读：复判模型可以只按 bot 配、
// 全局留空，只看全局的话配了等于没配，而面板上一切正常。
func hasLLM(b *core.Bot, snap *store.Snapshot) bool {
	_, llm := snap.ModelsFor(b.BotID())
	return len(llm) > 0
}

// needReview 报告初判之后要不要交给大模型复判：初判低于采信线，或者要罚
// （禁言/封禁）。先临时禁言、复判确认了才转正式——禁言设得很长、封禁是
// 永久的，值得第二个模型把关。
//
// 采信线按 bot 覆盖：读成全局值的话，owner 改了自己 bot 的采信线却毫无效果。
func needReview(b *core.Bot, snap *store.Snapshot, v adVerdict, act adAction) bool {
	if v.Decider != "systemone" || !hasLLM(b, snap) {
		return false // 大模型已经判过（systemone 不可用时它顶上），或没配复判模型
	}
	trust := float64(snap.BotSettingInt(b.BotID(), "antiad_so_trust", store.DefaultSoTrust)) / 100
	return v.Confidence < trust || act.Mute || act.Ban
}

// review 是大模型复判，以它为准：同一份 state 交给它独立判断（初判不随
// 载荷传给它，见 judgeLLM），它只是信息更全的一级。prior 是上一级结论
// （systemone 初判或内容哈希，见 hashHit）：复判失败时用它回退采信，
// 最终 Decider 也由它与 llm 拼成。
func review(b *core.Bot, snap *store.Snapshot, st adState, prior adVerdict, sysPrompt string) adVerdict {
	llm, err := judgeLLM(b, snap, st, prior, sysPrompt)
	if err != nil {
		// 复判失败时回退采信初判 —— 但只在初判有把握时。低于采信线的
		// 初判本就要靠复判定生死（采信线是我们设的该结论不可靠的门槛），
		// 复判没跑成还照它删消息、禁言、连带删除，等于把最不可靠的一路
		// 当成了最终结论。哈希命中不是模型拿不准：它是同一条内容此前已被
		// 判定为广告的硬证据，不该被采信线降级（复判失败时仍然采信它）。
		if prior.Decider != deciderHash &&
			demoteUnconfirmed(b, snap, &prior, "大模型复判失败："+err.Error()) {
			slog.Warn("反广告：大模型复判失败且初判低于采信线，按未定放行",
				"置信度", prior.Confidence, "err", err)
			prior.Cost += llm.Cost
			prior.Usage = billing.MergeUsage(prior.Usage, llm.Usage)
			return prior
		}
		// 有把握的初判仍采信：丢弃它等于浪费一次判定。但必须可见，否则
		// 面板上只剩一条低置信结论，复判故障长期无法发现。
		slog.Warn("反广告：大模型复判失败，仅采信初判", "err", err)
		prior.Reason = "（大模型复判失败：" + core.TruncateRunes(err.Error(), 120) +
			"；仅采信初判）" + prior.Reason
		prior.Cost += llm.Cost
		prior.Usage = billing.MergeUsage(prior.Usage, llm.Usage)
		return prior
	}
	llm.Decider = prior.Decider + "+llm"
	// 初判那一级的原始结论挂上来：定档仍以复判为准，但两级要分开落库与
	// 展示（见 verdictStage），复判不得把它覆盖掉。
	stg := prior.stage(initialStageName(prior.Decider))
	llm.Prior = &stg
	llm.Cost += prior.Cost
	llm.Usage = billing.MergeUsage(llm.Usage, prior.Usage)
	return llm
}

// initialStageName 给"初判级"结论起 stage 名：systemone 记 so，其余用它自己的
// Decider（rule:<id> / hash / manual / phash / prewarm_shell …）。
func initialStageName(decider string) string {
	if decider == "" || decider == "systemone" {
		return "so"
	}
	return decider
}

// demoteUnconfirmed 把复判没跑成、初判又没把握的结论降级成未定。
//
// 采信线（antiad_so_trust）是初判不可靠、要再问一次的门槛：低于它的
// 广告结论本要靠复判定生死。复判失败时照它处置，等于把最不可靠的一路
// 当成最终结论 —— 而按模型结论定档（antiad_bool_verdict）又恰恰不看
// 置信度，两者叠加就会出现低置信广告被删消息并禁言的情况。
// 返回是否降级（降级后 IsAd=false，按未定走：留流水、不处置）。
func demoteUnconfirmed(b *core.Bot, snap *store.Snapshot, v *adVerdict, why string) bool {
	trust := float64(snap.BotSettingInt(b.BotID(), "antiad_so_trust", store.DefaultSoTrust)) / 100
	if !v.IsAd || v.Confidence >= trust {
		return false
	}
	v.IsAd, v.Kind, v.Scope, v.Severity = false, "none", "message", 0
	// 处置意见一并作废：结论既已降为未定，就不该再留着 delete/mute。
	v.Planned, v.Delete, v.Mute = false, false, muteNone
	v.Reason = "（" + why + "，且初判置信度 " +
		fmt.Sprintf("%.0f%%", v.Confidence*100) + " 低于采信线，按未定放行）" + v.Reason
	return true
}
