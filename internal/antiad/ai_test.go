package antiad

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/store"
	"menshen/internal/testutil"
)

// TestBuildSystemOneReqScoreCriteriaIsList 锁住 jev 的请求契约。
//
// score 题的 criteria 必须是有序档位列表（下标即分值），缺了或写成 map
// 上游直接 422。4xx 不重试，于是每一次主判都失败、静默回落成「只有大模型」，
// 判定照常出结论、面板毫无异样。
func TestBuildSystemOneReqScoreCriteriaIsList(t *testing.T) {
	q := buildSystemOneReq(adState{}, soInstructions)["questions"].(map[string]any)
	sev := q["severity"].(map[string]any)
	levels, ok := sev["criteria"].([]string)
	if !ok || len(levels) != 4 {
		t.Fatalf("severity.criteria 应为 4 档列表，实际 %#v", sev["criteria"])
	}
}

// TestPromptsSeparateQuotedFromBio：模型曾把「整条转发」「个人简介」说成
// 「引用外部聊天的载荷」，凭空判广告。两级消息判定的提示词都必须写明。
func TestPromptsSeparateQuotedFromBio(t *testing.T) {
	for name, p := range map[string]string{"so": soInstructions, "llm": llmSystemPrompt} {
		if !strings.Contains(p, "没有 quoted 字段") {
			t.Errorf("%s 提示词未区分 quoted 与 bio / is_forwarded", name)
		}
	}
}

// TestPromptsCoverServiceListAd：资料里列着售卖中的服务并附联系方式，就是在
// 招揽 —— 线上真实漏过「抗投诉服务器、VPS、CDN、域名证书申请 + 唯一大号」的
// 简介被冷判定当成「业务介绍、无价格无招揽」放行，管理员 /check 反被续期。
// 四份提示词都要有这个口径，否则冷判定与消息判定会各漏各的。
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

// TestPromptsExplainMatchedRules：「启用但未强制」的规则靠 matched_rules
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
		// 实测误判（节点列表命中规则、模型照「强证据」跟判）：推翻出口必须
		// 写明「note 是样本的完整形态，缺引流载荷不算呈现」与「群主题内同形
		// 内容按正常处理」，否则模型只会照抄规则结论。
		if !strings.Contains(p, "不算呈现该形态") {
			t.Errorf("%s 提示词未说明形态只命中一部分时不算呈现", name)
		}
		if !strings.Contains(p, "正常讨论的") || !strings.Contains(p, "这类内容") {
			t.Errorf("%s 提示词未说明群主题内的同形内容按正常处理", name)
		}
	}
}

// TestAICallRetriesTransientFailure：5xx 是瞬时故障，重试便宜，漏判不便宜。
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

// TestAICallDoesNotRetry4xx：模型名错、鉴权错，重来一次同样会错，
// 重试只是把同一个错误再花四遍钱。
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
// 前缀（来源在记录卡片上另有字段），is_ad 选项翻成人话，置信度/危害度
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

// TestLLMAdWithoutKindIsClean：复判吐出 is_ad=true 却 kind=none 是自相矛盾 ——
// 线上 #18020：理由写着「按正常交流处理」，按模型结论定档照样删了，正文还
// 记成哈希，另一个人发同一句话被直接删。没说出广告类别就不算广告。
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
// 缺口在的话这条会对新人删消息 + 禁言 + 连带删除；与复判路径同一口径。
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

// TestVerdictCarriesModel 确认模型名一路带到 verdict 上：换模型后校准阈值，
// 第一件事就是知道眼前这条结论出自哪个模型。Decider 只说走了几级，说不出是谁。
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
// 复判模型配了等于没配，而面板上一切正常。
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
// owner 在面板上改了自己 bot 的采信线却毫无效果。
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

// TestReviewFailureDemotesWeakVerdict：复判失败时，低于采信线的初判不再
// 照着处置 —— 「13% 的广告」删消息、临时禁言、连带删除就是这么来的。
// 有把握的初判照旧采信（扔掉等于白判一次）。
//
// 用 50% 这个区间：低于下限线（30%）的那一档现在连复判都不跑，由
// TestSoFloorSkipsReview 覆盖。
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
	// 50% 低于初判线（75%）：连先行动作都不该有 —— 消息没删、也没临时禁言，
	// 自然没有「解开」这一说（复判失败后按未定放行）。
	if n := fake.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("低于初判线不该先删消息，得到 %d 次", n)
	}
	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("低于初判线不该先禁言，得到 %d 次", n)
	}

	// 有把握的初判（95%）+ 复判失败：照旧采信初判（不能白判一次）。
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
// 复核 —— 实测误杀：摘要从真实色情招揽样本里归纳出「简介出现双向机器人＝
// 色情推广」，而中文群里大量正常账号写「为了双方账号安全，请通过双向 bot
// 私聊我」（隐私保护），判定模型照摘要执行，把人判成色情广告号。
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
// 只送复判 —— 低置信初判本来就拿不准，而按模型结论定档不看置信度，先行动作
// 会把「拿不准」直接变成删消息 + 临时禁言（实测 jev 对「资料挂频道/bot」
// 这类会给 3%~32% 的广告）。高于线的照旧先删先禁。
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

	// 高于初判线：照旧先删先禁（先删后判不变）。
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

// TestPreActLineConfigurable：初判线可配，设成 0 等于退回「初判一出结论就动手」。
// 禁言置信度下限同样在这条路上生效（临时禁言也是禁言），所以这里把两道线
// 一起放到底，单独验证「初判线 0 = 低置信也先动手」这一行为。
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

// TestSoFloorSkipsReview：初判下限（默认 30%）以下的「广告」直接放行，
// 连复判都不跑 —— 那种结论是噪声，跑复判只是花钱买一个必然被推翻的结果。
func TestSoFloorSkipsReview(t *testing.T) {
	// 初判 20%（低于下限）：不复判、不处置、不删消息。
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.20, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "我回来了"))
	waitIdle(t, b)

	if llmN.Load() != 0 {
		t.Errorf("低于下限不该跑复判，跑了 %d 次", llmN.Load())
	}
	if soN.Load() != 1 {
		t.Errorf("初判应照常跑一次，得到 %d 次", soN.Load())
	}
	if n := fake.CountCalls("deleteMessage") + fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("低于下限不该有任何处置，得到 %d 次调用", n)
	}
	if verdict, action, reason := logRow(t, b); verdict != "clean" || action != "none" ||
		!strings.Contains(reason, "低于下限线") {
		t.Errorf("应记成未定放行并写明原因：verdict=%q action=%q reason=%q", verdict, action, reason)
	}

	// 初判 40%（高于下限、低于初判线）：照常复判，只是不先动手。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	_, llmN2 := fakeAIWith(t, b2, soReply("ad", 0.40, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 43, 8, "加微信 日入5000"))
	waitIdle(t, b2)
	if llmN2.Load() != 1 {
		t.Errorf("高于下限应照常复判，跑了 %d 次", llmN2.Load())
	}

	// 下限设成 0：关闭这条，20% 也照常复判。
	b3, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b3, -100)
	if err := b3.PutBotSetting(b3.BotID(), "antiad_so_floor", "0"); err != nil {
		t.Fatal(err)
	}
	_, llmN3 := fakeAIWith(t, b3, soReply("ad", 0.20, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))
	HandleGroupMessage(b3, testutil.GroupMsg(-100, 44, 9, "加微信 日入5000"))
	waitIdle(t, b3)
	if llmN3.Load() != 1 {
		t.Errorf("下限为 0 时应照常复判，跑了 %d 次", llmN3.Load())
	}
}

// TestTrustLineDefault95：采信线默认 95 —— 只有很有把握的初判才免复判。
// 老成员只删不禁（不会强制复判），所以这一档能干净地看出采信线。
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

	// 96% 的广告：达到采信线 → 直接采信初判，不花复判的钱。
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
