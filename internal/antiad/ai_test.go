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
