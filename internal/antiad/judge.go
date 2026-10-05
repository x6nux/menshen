package antiad

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/store"
	"menshen/internal/upstream"
)

// adVerdict 是一次判定的结果。
type adVerdict struct {
	IsAd       bool
	Confidence float64 // 0..1
	Kind       string
	Reason     string
	Decider    string // systemone / llm / systemone+llm
	// Model 是给出这条结论的那个模型名。Decider 只说走了几级，说不出
	// 具体是谁 —— 而换模型后校准阈值，第一件事就是知道眼前这条判定
	// 出自哪个模型。两级都跑时记最终拍板的那个。
	Model string
	// Scope 是广告出在哪：account = 账号本身就是广告号（资料写着广告），
	// message = 只有这条消息是。前者连带删除此人近期的全部消息；后者的内容
	// 会记成哈希，同样的内容再出现直接删（见 hash.go）。
	Scope string
	// Severity 是 systemone 给的危害度（0-3，可带小数）。复判的 JSON 里
	// 没有这一项，所以复判结论恒为 0；群内提醒的短撤回只认初判这条。
	Severity float64
	// ProfileOKHours 是复判给的「资料临时放行」时长（1~72 小时，0 = 不放行）。
	// 只有复判判为正常、且可疑的只是资料本身时才会给（见 profile_ok.go）。
	ProfileOKHours int
	Usage          billing.Usage
	Cost           int64 // quota
}

// buildSystemOneReq 构造 jev 请求体。
//
// instructions 由调用方给：消息判定与进群冷判定看的是同一份 state 结构，
// 但问的是完全不同的问题（「这条消息是不是广告」vs「这个账号是不是
// 广告号」），提示词必须分开。
//
// 不带 model 字段：模型名是 aiCall 按当前尝试填的（要剥掉上游前缀）。
func buildSystemOneReq(st adState, instructions string) map[string]any {
	return map[string]any{
		"state": st,
		"questions": map[string]any{
			// criteria 的值不要留 nil：那等于让模型自己猜两个选项的边界在哪，
			// 而「账号资料就是广告位」「变形词还原后才成立」这两类恰恰是
			// 它猜不到、必须明写的。
			"is_ad": map[string]any{
				"type":         "choice",
				"instructions": instructions,
				"criteria": map[string]any{
					"ad": "推广、引流、招揽、诈骗话术；或昵称/简介写着推广文案、收益承诺、" +
						"引流话术，或挂的频道/群组本身是广告；或露骨色情内容（性描述、性暴力、" +
						"乱伦、涉及未成年人），即使没有联系方式与广告语；或变形还原后属于以上任一种",
					"clean": "正常交流，包括分享技术链接、讨论商品价格、转发新闻",
				},
			},
			"ad_kind": map[string]any{
				"type":         "choice",
				"instructions": "若是广告，属于哪一类？判为广告时不得选 none，不是广告才选 none。",
				"criteria": map[string]any{
					"none":   "不是广告",
					"crypto": "加密货币、炒币、空投、交易所拉新",
					"porn":   "色情招揽：约炮、裸聊、上门服务、色情网站或色情群推广",
					"porn_bait": "色情内容：露骨的性描述、性暴力、乱伦、涉及未成年人的色情文字或" +
						"猎奇标题党（「看完整版」「点击观看」），即使没有任何联系方式或广告语",
					"gambling":   "赌博、博彩、彩票",
					"scam":       "诈骗，含兼职刷单、日入话术、杀猪盘、假客服、代还款",
					"promo":      "商品或服务推广、拉群引流、导流到私聊或主页",
					"spam_flood": "无实质内容的刷屏",
				},
			},
			// 决定删一条还是删光：账号本身就是广告号的，此人近期发的都是铺垫。
			"ad_scope": map[string]any{
				"type":         "choice",
				"instructions": "若是广告，问题出在账号还是只在这条消息？",
				"criteria": map[string]any{
					"account": "账号本身就是广告号：用户名、昵称、简介写着推广文案、收益承诺、" +
						"引流话术或色情招揽，或挂的频道/群组本身是广告（见 bio_links）",
					"message": "只有这条消息的内容是广告，账号资料本身正常；不是广告也选这个",
				},
			},
			"severity": map[string]any{
				"type":         "score",
				"instructions": "危害程度：0 = 无害，3 = 诈骗、露骨色情或大规模刷屏。",
				// score 题的 criteria 必须是有序列表，下标即分值；缺失或写成
				// map 上游直接 422。4xx 不重试，主判随之静默回落成「只有大模型」。
				"criteria": []string{"无害", "轻度推广", "明显引流", "诈骗、露骨色情或大规模刷屏"},
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
		// 否则整个功能静默失效，运维只看到「一条都没拦到」。
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
	// is_ad=true 却把 ad_kind 选成 none，是模型自相矛盾（问题里明写「判为
	// 广告时不得选 none」）。按模型结论定档只看 is_ad，照它会删消息、禁言、
	// 连带删除并把正文记成哈希；说不出广告类别就不算广告，与 judgeLLM
	// 里同一条兜底保持同一口径。
	if v.IsAd && v.Kind == "none" {
		slog.Info("反广告：systemone 返回 is_ad=ad 但 ad_kind=none，按正常处理",
			"置信度", v.Confidence)
		v.IsAd = false
	}
	return v, nil
}

// soChoiceLabel 把 systemone 的 is_ad 选项翻成人话。decider 不进这句：
// 来源在记录卡片上另有字段，理由里再写一遍只是噪音。模型回出名单外的
// 值时原样带出 —— 猜成「正常」会把异常吞掉。
func soChoiceLabel(choice string) string {
	switch choice {
	case "ad":
		return "广告"
	case "clean":
		return "正常"
	}
	return choice
}

// judgeLLM 用大模型复判。prior 是 systemone 的初判（可为零值），
// sysPrompt 由调用方给（消息判定与进群冷判定各一份）。
func judgeLLM(b *core.Bot, snap *store.Snapshot, st adState, prior adVerdict,
	sysPrompt string) (adVerdict, error) {

	_, llmModels := snap.ModelsFor(b.BotID())
	if len(llmModels) == 0 {
		return adVerdict{}, fmt.Errorf("反广告：未配置复判模型（antiad_llm_models）")
	}

	payload := map[string]any{
		"state": st,
	}
	if prior.Decider != "" {
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
		// 会让管理员完全无法校准阈值。
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

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(reply.Raw, &resp); err != nil || len(resp.Choices) == 0 {
		return adVerdict{Usage: reply.Usage, Cost: reply.Cost},
			fmt.Errorf("反广告：大模型响应无法解析")
	}

	obj := extractJSONObject(resp.Choices[0].Message.Content)
	if obj == "" {
		return adVerdict{Usage: reply.Usage, Cost: reply.Cost},
			fmt.Errorf("反广告：大模型未返回 JSON")
	}
	var out struct {
		IsAd       bool    `json:"is_ad"`
		Confidence float64 `json:"confidence"`
		Kind       string  `json:"kind"`
		Scope      string  `json:"scope"`
		Severity   float64 `json:"severity"`
		Reason     string  `json:"reason"`
		// ProfileOKHours 是「资料临时放行」时长：只在判为正常、且可疑的
		// 只是资料本身时有意义。判成广告时一律忽略。
		ProfileOKHours int `json:"profile_ok_hours"`
	}
	if err := json.Unmarshal([]byte(obj), &out); err != nil {
		return adVerdict{Usage: reply.Usage, Cost: reply.Cost},
			fmt.Errorf("反广告：大模型 JSON 解析失败: %w", err)
	}
	// is_ad=true 却说不出广告类别是模型自相矛盾（实测理由写着「按正常交流
	// 处理」）。按模型结论定档只看 is_ad，照它会删消息、记哈希，按正常走。
	if out.IsAd && out.Kind == "none" {
		out.IsAd = false
	}
	okHours := 0
	if !out.IsAd {
		okHours = clampProfileHours(out.ProfileOKHours)
	}

	return adVerdict{
		IsAd: out.IsAd, Confidence: out.Confidence, Kind: out.Kind, Scope: out.Scope,
		Severity: out.Severity,
		Reason:   out.Reason, Decider: "llm", Model: reply.Model,
		ProfileOKHours: okHours, Usage: reply.Usage, Cost: reply.Cost,
	}, nil
}

// extractJSONObject 从模型输出里抠出首个完整的 JSON 对象。
// 模型爱用 ```json 包裹、爱在前面加一句「好的」，严格解析会全军覆没。
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
// 与 judge 的「低置信才升级」刻意不同 —— 复查是人主动发起的二次判断，
// 目的就是拿到两方结论互相印证；省那一次调用等于把复查降级成重判一遍。
func judgeBoth(b *core.Bot, snap *store.Snapshot, st adState) (adVerdict, error) {
	so, soErr := judgeSystemOne(b, snap, st, soInstructions)
	// 即使 soErr 非空也照样跑大模型：so 此时是零值，judgeLLM 会自动
	// 略过 prior_verdict，退化成「只有大模型」的判定而不是整体失败。
	llm, llmErr := judgeLLM(b, snap, st, so, llmSystemPrompt)

	switch {
	case soErr != nil && llmErr != nil:
		return adVerdict{}, fmt.Errorf("systemone: %v; llm: %v", soErr, llmErr)
	case llmErr != nil:
		// 复查同理：低于采信线的初判不因为「大模型没跑成」就变成可处置的。
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

	// 以大模型为准（它看到了 systemone 的初判），但两方结论都要留在
	// reason 里：复查的价值正在于让人看见两个模型是否一致。
	final := llm
	final.Decider = "systemone+llm"
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
	// 这条回落是静默的——判定照常给出结论，面板上看不出任何异样，
	// 于是 systemone 可以坏上几个月都没人发现。至少留一行日志，
	// 并把回落写进 reason，让流水里看得见。
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

// review 是大模型复判，以它为准：它看到了初判，是信息更全的一级。
// prior 可以是 systemone 的初判，也可以是内容哈希（见 hashHit）。
func review(b *core.Bot, snap *store.Snapshot, st adState, prior adVerdict, sysPrompt string) adVerdict {
	llm, err := judgeLLM(b, snap, st, prior, sysPrompt)
	if err != nil {
		// 复判失败时回退采信初判 —— 但只在初判自己有把握时。低于采信线的
		// 初判本来就是要靠复判定生死的（那是我们自己设的「这条不可靠」的
		// 门槛），复判没跑成还照它删消息、禁言、连带删除，等于把最不可靠
		// 的一路当成了最终结论。实测：一条「我活了」被 systemone 判 13%
		// 广告、复判超时，结果删了消息、临时禁言，还连带删了此人近期全部消息。
		// 哈希命中不是「模型拿不准」：它是同一条内容此前已被判定为广告的
		// 硬证据，不该被采信线降级（复判失败时照旧采信它）。
		if prior.Decider != deciderHash &&
			demoteUnconfirmed(b, snap, &prior, "大模型复判失败："+err.Error()) {
			slog.Warn("反广告：大模型复判失败且初判低于采信线，按未定放行",
				"置信度", prior.Confidence, "err", err)
			prior.Cost += llm.Cost
			prior.Usage = billing.MergeUsage(prior.Usage, llm.Usage)
			return prior
		}
		// 有把握的初判照旧采信：扔掉它等于白判一次。但必须看得见，否则
		// 面板上只剩一条低置信结论，复判坏了多久都没人发现。
		slog.Warn("反广告：大模型复判失败，仅采信初判", "err", err)
		prior.Reason = "（大模型复判失败：" + core.TruncateRunes(err.Error(), 120) +
			"；仅采信初判）" + prior.Reason
		prior.Cost += llm.Cost
		prior.Usage = billing.MergeUsage(prior.Usage, llm.Usage)
		return prior
	}
	llm.Decider = prior.Decider + "+llm"
	llm.Cost += prior.Cost
	llm.Usage = billing.MergeUsage(llm.Usage, prior.Usage)
	return llm
}

// demoteUnconfirmed 把「复判没跑成、初判又没把握」的结论降级成未定。
//
// 采信线（antiad_so_trust）就是「这条初判不可靠、要再问一次」的门槛：
// 低于它的广告结论本来要靠复判定生死。复判失败时照它处置，等于把最不
// 可靠的一路当成最终结论 —— 而按模型结论定档（antiad_bool_verdict）
// 又恰恰不看置信度，两件事叠在一起就会出现「13% 的广告删消息+禁言」。
// 返回是否降级（降级后 IsAd=false，按未定走：留流水、不处置）。
func demoteUnconfirmed(b *core.Bot, snap *store.Snapshot, v *adVerdict, why string) bool {
	trust := float64(snap.BotSettingInt(b.BotID(), "antiad_so_trust", store.DefaultSoTrust)) / 100
	if !v.IsAd || v.Confidence >= trust {
		return false
	}
	v.IsAd, v.Kind, v.Scope, v.Severity = false, "none", "message", 0
	v.Reason = "（" + why + "，且初判置信度 " +
		fmt.Sprintf("%.0f%%", v.Confidence*100) + " 低于采信线，按未定放行）" + v.Reason
	return true
}
