package antiad

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

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

	if _, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt); err != nil {
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
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"is_ad\":true,\"confidence\":0.95,\"kind\":\"scam\",\"scope\":\"message\",\"severity\":2.2,\"reason\":\"洗钱引流\"}"}}]}`))
	})
	v, err := judgeLLM(b, b.Cache.Snap(), adState{}, adVerdict{}, llmSystemPrompt)
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
func TestReviewFailureDemotesWeakVerdict(t *testing.T) {
	// 弱初判（13%）：复判失败 → 按未定放行，不追加处置，且临时禁言要解开。
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.13, "none", "account"), ``) // 复判无响应=失败
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
	// 临时禁言要解开：复判失败了，没人确认过那是广告。
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("临时禁言应被解开")
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
