package antiad

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/store"
	"menshen/internal/testutil"
)

// TestLLMDoesNotReceiveSystemonePrior：复判的输入里不带 systemone 的结论——
// 初判结论会锚定复判，使其顺着 prior 编造证据，两级独立互证也会变成复述。
// 内容哈希那一档是本服务账本，仍照传。
func TestLLMDoesNotReceiveSystemonePrior(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var body atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body.Store(string(raw))
		fakeAIOK(w, r)
	})
	// userContent 取出请求里那条待判定的 JSON（system 提示词不在核对范围：
	// 它本来就说明 prior_verdict 字段的语义）。
	userContent := func() string {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(body.Load().(string)), &req); err != nil {
			t.Fatalf("请求体解析失败: %v", err)
		}
		for _, m := range req.Messages {
			if m.Role == "user" {
				return m.Content
			}
		}
		return ""
	}

	soPrior := adVerdict{IsAd: true, Confidence: 0.97, Kind: "porn", Decider: "systemone"}
	if _, err := judgeLLM(b, b.Cache.Snap(), testAdState(), soPrior, llmSystemPrompt); err != nil {
		t.Fatal(err)
	}
	if got := userContent(); strings.Contains(got, "prior_verdict") ||
		strings.Contains(got, "0.97") || strings.Contains(got, "porn") {
		t.Errorf("systemone 的结论不该进复判载荷: %s", got)
	}

	hashPrior := adVerdict{IsAd: true, Confidence: 1, Kind: "scam", Decider: deciderHash}
	if _, err := judgeLLM(b, b.Cache.Snap(), testAdState(), hashPrior, llmSystemPrompt); err != nil {
		t.Fatal(err)
	}
	if got := userContent(); !strings.Contains(got, "prior_verdict") ||
		!strings.Contains(got, "hash") {
		t.Errorf("内容哈希的 prior 应照传: %s", got)
	}
}

// TestBuildSystemOneReqScoreCriteriaIsList 锁住 systemone 的请求契约。
//
// score 题的 criteria 必须是有序档位列表（下标即分值），缺了或写成 map
// 上游直接 422。4xx 不重试，于是每一次主判都失败、静默回落成只有大模型，
// 判定照常出结论、面板毫无异样。
func TestBuildSystemOneReqScoreCriteriaIsList(t *testing.T) {
	q := buildSystemOneReq(adState{}, soInstructions)["questions"].(map[string]any)
	sev := q["severity"].(map[string]any)
	levels, ok := sev["criteria"].([]string)
	if !ok || len(levels) != 4 {
		t.Fatalf("severity.criteria 应为 4 档列表，实际 %#v", sev["criteria"])
	}
}

// TestPromptsSeparateQuotedFromBio：两级消息判定的提示词都必须写明区分
// 转发、个人简介与引用外部聊天的载荷，否则模型会凭空判广告。
func TestPromptsSeparateQuotedFromBio(t *testing.T) {
	for name, p := range map[string]string{"so": soInstructions, "llm": llmSystemPrompt} {
		if !strings.Contains(p, "没有 quoted 字段") {
			t.Errorf("%s 提示词未区分 quoted 与 bio / is_forwarded", name)
		}
	}
}

// TestPromptsCoverServiceListAd：资料里列着售卖中的服务并附联系方式即属招揽。
// 四份提示词都要有这个口径，否则冷判定与消息判定会各自漏判。
func TestPromptsCoverServiceListAd(t *testing.T) {
	for name, p := range map[string]string{
		"so": soInstructions, "llm": llmSystemPrompt,
		"cold": coldInstructions, "coldLLM": coldLLMPrompt,
		"prewarm": prewarmInstructions, "prewarmLLM": prewarmLLMPrompt,
	} {
		if !strings.Contains(p, "业务清单") || !strings.Contains(p, "没有标价也算") {
			t.Errorf("%s 提示词缺少「资料写成业务清单也是招揽」的口径", name)
		}
	}
}

// TestPromptsExplainKnownAdPatterns：不说明的话，模型会把样本库里的广告原文
// 当成本条消息（或此人资料）里的内容，凭空判广告。
func TestPromptsExplainKnownAdPatterns(t *testing.T) {
	if !strings.Contains(llmSystemPrompt, "known_ad_patterns") ||
		!strings.Contains(llmSystemPrompt, "参考样本") {
		t.Error("复判提示词未说明 known_ad_patterns 只是参考样本")
	}
	for name, p := range map[string]string{"cold": coldInstructions, "coldLLM": coldLLMPrompt} {
		if !strings.Contains(p, "known_ad_patterns") || !strings.Contains(p, "不是此人的资料") {
			t.Errorf("%s 提示词未说明 known_ad_patterns 不是此人的资料", name)
		}
	}
}

// TestPromptsExplainMatchedRules：启用但未强制的规则靠 `matched_rules`
// 进判定：两级提示词都必须给出采信口径（强证据，可结合语境推翻但要写理由），
// 否则它只能混在摘要里、被模型当弱提示忽略。
func TestPromptsExplainMatchedRules(t *testing.T) {
	for name, p := range map[string]string{
		"systemone": soInstructions,
		"llm":       llmSystemPrompt,
	} {
		if !strings.Contains(p, "matched_rules") {
			t.Errorf("%s 提示词未说明 matched_rules", name)
		}
		if !strings.Contains(p, "强证据") {
			t.Errorf("%s 提示词未写明 matched_rules 是强证据", name)
		}
		if !strings.Contains(p, "可以判正常") {
			t.Errorf("%s 提示词未给出结合语境推翻的出口", name)
		}
		// 推翻出口必须写明：note 是样本的完整形态，缺引流载荷不算呈现该形态；
		// 群主题内的同形内容按正常处理。否则模型只会照抄规则结论。
		if !strings.Contains(p, "不算呈现该形态") {
			t.Errorf("%s 提示词未说明形态只命中一部分时不算呈现", name)
		}
		if !strings.Contains(p, "正常讨论的") || !strings.Contains(p, "这类内容") {
			t.Errorf("%s 提示词未说明群主题内的同形内容按正常处理", name)
		}
	}
}

// TestAICallRetriesTransientFailure：5xx 是瞬时故障，重试代价低，漏判代价高。
func TestAICallRetriesTransientFailure(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fakeAIOK(w, r)
	})

	if _, err := judgeLLM(b, b.Cache.Snap(), testAdState(), adVerdict{}, llmSystemPrompt); err != nil {
		t.Fatalf("一次 502 之后应重试成功: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("上游被请求 %d 次，期望 2 次（首次 + 一次重试）", n)
	}
}

// TestAICallDoesNotRetry4xx：模型名错、鉴权错会持续失败，
// 重试只会重复同一错误、多花开销。
func TestAICallDoesNotRetry4xx(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	var hits atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})

	if _, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt); err == nil {
		t.Fatal("400 应当直接失败")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("4xx 被请求了 %d 次，期望 1 次（不重试）", n)
	}
}

// TestSystemOneReasonFormat：初判理由的格式是给人看的：不带 "systemone"
// 前缀（来源在记录卡片上另有字段），is_ad 选项转成中文描述，置信度/危害度
// 带冒号。
func TestSystemOneReasonFormat(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	choice := "ad"
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"answers":{"is_ad":{"choice":"` + choice + `","confidence":1},` +
			`"ad_kind":{"choice":"scam"},"ad_scope":{"choice":"message"},` +
			`"severity":{"score":2.2}}}`))
	})

	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if want := "判定 广告 置信度:100%，危害度:2.2"; v.Reason != want {
		t.Errorf("广告初判理由 = %q，期望 %q", v.Reason, want)
	}
	choice = "clean"
	v, err = judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if want := "判定 正常 置信度:100%，危害度:2.2"; v.Reason != want {
		t.Errorf("正常初判理由 = %q，期望 %q", v.Reason, want)
	}
}

// TestLLMVerdictCarriesSeverity：复判要同时给出理由与危害度。理由留给
// 记录卡片、查看页这些看详情的地方；危害度供群内那半行短结论与短撤回用。
func TestLLMVerdictCarriesSeverity(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"is_ad\":true,\"confidence\":0.95,\"kind\":\"scam\",\"scope\":\"message\",\"severity\":2.2,\"reason\":\"洗钱引流\",\"evidence\":[\"测试群\"]}"}}]}`))
	})
	v, err := judgeLLM(b, b.Cache.Snap(), testAdState(), adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if v.Severity != 2.2 {
		t.Errorf("复判的危害度应带回来，得到 %v", v.Severity)
	}
	if v.Reason != "洗钱引流" {
		t.Errorf("理由要保留（详情页用），得到 %q", v.Reason)
	}
}

// TestLLMAdWithoutKindIsClean：复判给出 is_ad=true 却 kind=none 是自相矛盾；
// 按模型结论定档会照样删除并把正文记成哈希。没说出广告类别就不算广告。
func TestLLMAdWithoutKindIsClean(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"is_ad\":true,\"confidence\":0.15,\"kind\":\"none\",\"scope\":\"message\",\"severity\":0,\"profile_ok_hours\":48,\"reason\":\"按正常交流处理\"}"}}]}`))
	})
	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsAd {
		t.Error("kind=none 的广告结论应按正常处理")
	}
	if v.ProfileOKHours != 48 {
		t.Errorf("按正常处理后资料放行要生效，得到 %d", v.ProfileOKHours)
	}
}

// TestSystemOneAdWithoutKindIsClean：初判（systemone）同样不能采信
// is_ad=ad 却 ad_kind=none 的自相矛盾结论。按模型结论定档只看 is_ad，
// 否则这条会对新人删消息、禁言并连带删除；与复判路径同一口径。
func TestSystemOneAdWithoutKindIsClean(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAIWith(t, b, soReply("ad", 0.99, "none", "account"), ``)
	v, err := judgeSystemOne(b, b.Cache.Snap(), adState{}, soInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if v.IsAd {
		t.Error("systemone 给出 ad_kind=none 时应按正常处理")
	}
}

// TestVerdictCarriesModel 确认模型名一路带到 verdict 上：Decider 只标明走了
// 几级判定，不指示具体模型；换模型后校准阈值需要知道结论出自哪个模型。
func TestVerdictCarriesModel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, nil)
	snap := b.Cache.Snap()

	so, err := judgeSystemOne(b, snap, adState{}, soInstructions)
	if err != nil || so.Model != "so-model" {
		t.Errorf("systemone: Model = %q, err = %v, 期望 so-model", so.Model, err)
	}
	llm, err := judgeLLM(b, snap, adState{}, adVerdict{}, llmSystemPrompt)
	if err != nil || llm.Model != "llm-model" {
		t.Errorf("大模型: Model = %q, err = %v, 期望 llm-model", llm.Model, err)
	}
}

// TestJudgeEscalatesWithPerBotLLMModel：复判模型可以只按 bot 配、全局留空。
// 判断要不要升级复判时若只看全局设置，这个 bot 的低置信结论会被原样采信，
// 按 bot 配置的复判模型不会生效。
func TestJudgeEscalatesWithPerBotLLMModel(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			// 低于默认采信线 80%，应当升级复判。
			w.Write([]byte(`{"answers":{"is_ad":{"choice":"ad","confidence":0.5}}}`))
			return
		}
		fakeAIOK(w, r)
	})
	if err := b.PutSetting("antiad_llm_model", ""); err != nil {
		t.Fatalf("清空全局复判模型失败: %v", err)
	}
	if _, err := b.Store.Write.Exec(`UPDATE bots SET llm_model='bot-llm' WHERE bot_id=?`,
		b.BotID()); err != nil {
		t.Fatalf("按 bot 配复判模型失败: %v", err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	snap := b.Cache.Snap()
	so, err := judgeFirst(b, snap, adState{})
	if err != nil {
		t.Fatalf("judgeFirst: %v", err)
	}
	if !needReview(b, snap, so, adAction{}) {
		t.Fatal("低置信且按 bot 配了复判模型，应当复判")
	}
	v := review(b, snap, adState{}, so, llmSystemPrompt)
	if v.Decider != "systemone+llm" || v.Model != "bot-llm" {
		t.Errorf("Decider = %q, Model = %q；期望升级到按 bot 配的复判模型 bot-llm",
			v.Decider, v.Model)
	}
}

// TestJudgeUsesPerBotTrustLine：采信线归 owner 按 bot 调。读成全局值的话，
// owner 对自己的 bot 调整采信线不会生效。
func TestJudgeUsesPerBotTrustLine(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAI(t, b, nil) // systemone 判 93%
	if err := b.PutBotSetting(b.BotID(), "antiad_so_trust", "95"); err != nil {
		t.Fatalf("按 bot 调采信线失败: %v", err)
	}

	snap := b.Cache.Snap()
	so, err := judgeFirst(b, snap, adState{})
	if err != nil {
		t.Fatalf("judgeFirst: %v", err)
	}
	if !needReview(b, snap, so, adAction{}) {
		t.Error("systemone 93% 低于本 bot 的采信线 95%，应升级复判")
	}
}

// TestPromptsExplainPayloadLines：方括号前缀行是本条附带的非文字载荷，
// 不说明的话模型分不清那是卡片、按钮还是本人说的话。
func TestPromptsExplainPayloadLines(t *testing.T) {
	for name, p := range map[string]string{"so": soInstructions, "llm": llmSystemPrompt} {
		if !strings.Contains(p, "［联系人卡片］") || !strings.Contains(p, "［隐藏链接］") {
			t.Errorf("%s 提示词未说明方括号前缀的载荷行", name)
		}
	}
}

// TestReviewFailureDemotesWeakVerdict：复判失败时，低于采信线的初判不按
// 模型结论处置，避免低置信结论触发删消息、临时禁言与连带删除；达到采信线
// 的初判仍然采信。
//
// 用 50% 区间：低于下限线（20%）的一档不先动手，但仍送复判，由
// TestSoFloorSkipsPreAction 覆盖。
func TestReviewFailureDemotesWeakVerdict(t *testing.T) {
	// 弱初判（50%）：复判失败 → 按未定放行，不追加处置。
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.50, "promo", "account"), ``) // 复判无响应=失败
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "我活了"))
	waitIdle(t, b)

	var verdict, action, reason string
	if err := b.Store.Read.QueryRow(`SELECT verdict,action,reason FROM antiad_log
		WHERE user_id=42 ORDER BY id DESC LIMIT 1`).Scan(&verdict, &action, &reason); err != nil {
		t.Fatal(err)
	}
	if verdict != "clean" || strings.Contains(action, "ban") ||
		strings.Contains(action, "muted") {
		t.Errorf("低于采信线的初判不该照处置：verdict=%q action=%q", verdict, action)
	}
	if !strings.Contains(reason, "低于采信线") {
		t.Errorf("流水里要写明原因：%q", reason)
	}
	// 50% 低于初判线（75%）：连先行动作都不该有——消息没删、也没临时禁言，
	// 因此不需要后续解禁（复判失败后按未定放行）。
	if n := fake.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("低于初判线不该先删消息，得到 %d 次", n)
	}
	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("低于初判线不该先禁言，得到 %d 次", n)
	}

	// 有把握的初判（95%）+ 复判失败：仍采信初判。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	fakeAIWith(t, b2, soReply("ad", 0.95, "scam", "message"), ``)
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 43, 8, "加微信 日入5000"))
	waitIdle(t, b2)
	var action2 string
	if err := b2.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE user_id=43 ORDER BY id DESC LIMIT 1`).Scan(&action2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(action2, "muted") && !strings.Contains(action2, "banned") {
		t.Errorf("有把握的初判在复判失败时仍该处置，得到 action=%q", action2)
	}
}

// TestPatternClauseGuardsKeywordMisjudgment：摘要里的关键词形态必须按上下文
// 复核。摘要可能从广告样本里归纳出某种表面特征，而该特征在正常账号的隐私
// 提示中同样常见，模型照摘要执行会误判为广告。
func TestPatternClauseGuardsKeywordMisjudgment(t *testing.T) {
	for _, want := range []string{"归纳提示", "不是判决", "双向机器人", "隐私保护", "按上下文复核"} {
		if !strings.Contains(patternClause, want) {
			t.Errorf("patternClause 应保留口径 %q：\n%s", want, patternClause)
		}
	}
	// 四级提示词（两级消息判定 + 两级冷判定）都要带上这条口径。
	for name, prompt := range map[string]string{
		"soInstructions":   soInstructions,
		"llmSystemPrompt":  llmSystemPrompt,
		"coldInstructions": coldInstructions,
		"coldLLMPrompt":    coldLLMPrompt,
	} {
		if !strings.Contains(prompt, "归纳提示") {
			t.Errorf("%s 缺少形态口径", name)
		}
	}
	// 摘要生成器要求形态带上下文条件，不许写成单个关键词。
	if !strings.Contains(digestSystemPrompt, "每条形态必须写清上下文条件") {
		t.Error("摘要生成提示词应要求写清上下文条件")
	}
}

// TestPreActLineSkipsLowConfidence：初判线（默认 75%）以下的初判不先动手，
// 只送复判。按模型结论定档不看置信度，先行动作会把低置信结论直接变成删消息
// 与临时禁言。高于线的先删先禁。
func TestPreActLineSkipsLowConfidence(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	// 初判 70%（低于线），复判确认是广告：终判照常处置，但之前不动手。
	fakeAIWith(t, b, soReply("ad", 0.70, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "加微信 日入5000"))
	waitIdle(t, b)

	mutes := fake.Calls("restrictChatMember")
	if len(mutes) != 1 {
		t.Fatalf("低于初判线只该有终判的正式禁言，得到 %d 次", len(mutes))
	}
	if d := untilOf(mutes[0]); d <= int64(tempMute/time.Second)+5 {
		t.Errorf("那一次应是正式禁言（按本群时长），得到 %d 秒", d)
	}
	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("消息应在终判时删一次，得到 %d 次", n)
	}
	if _, _, reason := logRow(t, b); strings.Contains(reason, "初判先行") {
		t.Errorf("低于初判线不该记「初判先行」：%q", reason)
	}

	// 高于初判线：仍先删先禁（先删后判不变）。
	b2, fake2 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	fakeAIWith(t, b2, soReply("ad", 0.95, "scam", "message"),
		llmReply(true, 0.95, "scam", "message"))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 43, 8, "加微信 日入5000"))
	waitIdle(t, b2)
	if n := len(fake2.Calls("restrictChatMember")); n != 2 {
		t.Errorf("高于初判线应先临时禁言、再正式禁言，得到 %d 次", n)
	}
}

// TestPreActLineConfigurable：初判线可配，设成 0 表示初判一出结论就动手。
// 禁言置信度下限同样在这条路上生效（临时禁言也是禁言），所以这里把两道线
// 一起放到底，单独验证初判线 0 时低置信也先动手这一行为。
func TestPreActLineConfigurable(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	for k, v := range map[string]string{"antiad_pre_act_conf": "0", "antiad_mute_conf": "0"} {
		if err := b.PutBotSetting(b.BotID(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	fakeAIWith(t, b, soReply("ad", 0.30, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 44, 9, "加微信 日入5000"))
	waitIdle(t, b)
	if n := len(fake.Calls("restrictChatMember")); n != 2 {
		t.Errorf("线设成 0 时应先临时禁言、再正式禁言，得到 %d 次", n)
	}
}

// TestSoFloorSkipsPreAction：初判下限（默认 20%）以下的广告结论不据以先
// 动手，但仍交复判定案 —— 早期版本在这里直接放行，实测会把低置信的真广告
// 漏掉（邀请码返利一类初判常只给个位数置信度）；只有没配复判模型时，下限
// 才回到「放行线」的含义。
func TestSoFloorSkipsPreAction(t *testing.T) {
	// 初判 10%（低于下限）+ 复判正常：不先动手，终判放行。
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.10, "promo", "message"),
		llmReply(false, 0.9, "none", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "看我的邀请码"))
	waitIdle(t, b)

	if soN.Load() != 1 {
		t.Errorf("初判应照常跑一次，得到 %d 次", soN.Load())
	}
	if llmN.Load() != 1 {
		t.Errorf("低于下限也应送复判，跑了 %d 次", llmN.Load())
	}
	if n := fake.CountCalls("deleteMessage") + fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("低于下限不该有先行动作，得到 %d 次调用", n)
	}
	if verdict, action, reason := logRow(t, b); verdict != "clean" || action != "none" ||
		!strings.Contains(reason, "低于下限线") {
		t.Errorf("复判正常应放行并写明下限原因：verdict=%q action=%q reason=%q",
			verdict, action, reason)
	}

	// 初判 10% + 复判确认广告：低置信的真广告不再漏掉。
	b2, fake2 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	fakeAIWith(t, b2, soReply("ad", 0.10, "promo", "message"),
		llmReply(true, 0.9, "promo", "message"))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 43, 8, "快来看看 M8P5J1 https://muse.ai/join"))
	waitIdle(t, b2)
	if verdict, action, _ := logRow(t, b2); verdict != "ad" || !strings.Contains(action, "deleted") {
		t.Errorf("低置信广告经复判确认后应处置：verdict=%q action=%q", verdict, action)
	}
	// 低于下限不先动手：消息只由终判删一次。
	if n := fake2.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("消息应只在终判时删一次，得到 %d 次", n)
	}

	// 没配复判模型：下限回到放行线，低置信初判不处罚。
	b3, fake3 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b3, -100)
	fakeAIWith(t, b3, soReply("ad", 0.10, "promo", "message"), "")
	setGlobal(t, b3, "antiad_llm_model", "")
	HandleGroupMessage(b3, testutil.GroupMsg(-100, 44, 9, "看我的邀请码"))
	waitIdle(t, b3)
	if verdict, action, _ := logRow(t, b3); verdict != "clean" || action != "none" {
		t.Errorf("无复判模型时低置信初判应放行：verdict=%q action=%q", verdict, action)
	}
	if n := fake3.CountCalls("deleteMessage") + fake3.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("无复判模型时不该有处置，得到 %d 次", n)
	}
}

// TestTrustLineDefault95：采信线默认 95——只有很有把握的初判才免复判。
// 老成员只删不禁（不会强制复判），因此这一档可以单独观察采信线。
func TestTrustLineDefault95(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if got := b.Cache.Snap().BotSettingInt(b.BotID(), "antiad_so_trust",
		store.DefaultSoTrust); got != 95 {
		t.Fatalf("采信线默认应为 95，得到 %d", got)
	}

	// 90% 的广告：低于采信线 → 转复判。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	if _, err := b2.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count) VALUES (-100,42,1,1,500)`); err != nil {
		t.Fatal(err)
	}
	_, llmN := fakeAIWith(t, b2, soReplySev("ad", 0.90, "promo", "message", 1),
		llmReply(true, 0.9, "promo", "message"))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 42, 7, "加微信 日入5000"))
	waitIdle(t, b2)
	if llmN.Load() != 1 {
		t.Errorf("90%% 的初判应转复判（采信线 95），复判跑了 %d 次", llmN.Load())
	}

	// 96% 的广告：达到采信线 → 直接采信初判，不再调用复判。
	b3, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b3, -100)
	if _, err := b3.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count) VALUES (-100,43,1,1,500)`); err != nil {
		t.Fatal(err)
	}
	_, llmN3 := fakeAIWith(t, b3, soReplySev("ad", 0.96, "promo", "message", 1),
		llmReply(true, 0.9, "promo", "message"))
	HandleGroupMessage(b3, testutil.GroupMsg(-100, 43, 8, "加微信 日入5000"))
	waitIdle(t, b3)
	if llmN3.Load() != 0 {
		t.Errorf("96%% 的初判应直接采信，复判跑了 %d 次", llmN3.Load())
	}
}
