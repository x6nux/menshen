package antiad

// 判词-输入一致性门的测试。判词引用了输入中不存在的证据时必须被拦下；
// 模型声明了真实证据的判词必须放行，包括对原文做变形还原的写法
// （只核对 evidence 与硬 token，不核对判词措辞）。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

func gateState(text string) adState {
	return adState{
		Chat:    adChatInfo{ID: -100, Title: "测试群"},
		Message: adMessageInfo{Text: text, Length: len([]rune(text))},
		Sender:  senderProfile{UserID: 555, FirstName: "路人", Username: "someone"},
	}
}

func gateVerdict(reason string, evidence ...string) adVerdict {
	return adVerdict{IsAd: true, Confidence: 0.9, Kind: "porn",
		Decider: "llm", Reason: reason, Evidence: evidence}
}

// 引用了输入中不存在证据的判词，进入门后必须被降为未定。
func TestEvidenceGateCatchesFabricatedReasons(t *testing.T) {
	// 正文极短，判词引用了不存在的域名与外部引用贴纸，且未给出真实证据。
	v := evidenceGate(gateState("适合你自己的"), gateVerdict(
		"正文为典型的规避形态：本人正文几乎为空，载荷全在外部引用的贴纸里"+
			"（裸聊约炮招揽、大尺度学校视频、访问色情网站 ldd26 .xyz 并加 LINE 账号），"+
			"是色情网站引流，按广告论处"))
	if v.IsAd {
		t.Fatalf("编造引用载荷的判词应被拦下")
	}
	if !strings.Contains(v.Reason, "ldd26") || !strings.Contains(v.Reason, "没有 quoted") {
		t.Errorf("前缀应说明不符项，得到 %q", v.Reason)
	}

	// 正文中不含 `约炮`，模型却把它声明为正文原文中的证据。
	v = evidenceGate(gateState("现在不怕CF封了  无限邮箱 小号整起！"), gateVerdict(
		"正文「约炮」等招揽话术并附联系方式，属色情招揽", "约炮", "联系方式"))
	if v.IsAd {
		t.Fatalf("声明的证据不存在时应被拦下")
	}
	if !strings.Contains(v.Reason, "约炮") {
		t.Errorf("前缀应指出不存在的证据，得到 %q", v.Reason)
	}

	// 判为广告却没有声明任何证据：同样拦。
	v = evidenceGate(gateState("来看看"), gateVerdict("属于推广话术"))
	if v.IsAd {
		t.Fatal("没有 evidence 的广告结论应被拦下")
	}
}

// 零宽字符与异形标点拆词的原文，evidence 摘归一化后的写法，算同一份原文。
func TestEvidenceGateToleratesObfuscatedPunctuation(t *testing.T) {
	st := gateState("　急\u200c招\u200c‧拍·照\u200d📷\u2060　\u200b日\u2060结百左右")
	v := evidenceGate(st, gateVerdict("正文『急招·拍照·日结百左右』是兼职招揽",
		"急招·拍照·日结百左右"))
	if !v.IsAd {
		t.Fatalf("标点归一后的证据不该被拦：%q", v.Reason)
	}
	// 但换字不行：原文用词被替换成另一词后仍算不存在。
	v = evidenceGate(gateState("看煮页 日结"), gateVerdict("引导看主页", "看主页"))
	if v.IsAd {
		t.Fatal("换字改写的证据应被拦下")
	}
}

// 证据逐字摘自原文：放行。判词措辞可以概括、可以变形还原。
func TestEvidenceGatePassesGroundedReasons(t *testing.T) {
	// 提示词要求先做变形还原再判断：判词措辞可与原文不同，
	// 只要 evidence 摘自原文就不算不一致。
	st := gateState("看煮页 日入5000 私聊我")
	v := evidenceGate(st, gateVerdict("正文引导看主页、日入话术，属诈骗引流",
		"看煮页", "日入5000 私聊我"))
	if !v.IsAd {
		t.Fatalf("证据逐字来自原文的判词不该被拦：%q", v.Reason)
	}

	// 引入的外部引用里的内容：quoted 原文即证据来源。
	st = gateState("u")
	st.Quoted = &adQuotedInfo{Text: "上门服务 https://t.me/+abc123xyz", From: "某频道", IsExternal: true}
	v = evidenceGate(st, gateVerdict("正文极短、载荷全在外部引用里，典型规避形态",
		"上门服务", "t.me/+abc123xyz"))
	if !v.IsAd {
		t.Fatalf("有真实证据的判词不该被拦：%q", v.Reason)
	}

	// 零宽字符拆词的原文，evidence 摘还原后的写法：归一化后算同一份原文。
	v = evidenceGate(gateState("首\u200c发\u200b优惠 私聊我"),
		gateVerdict("正文在推违规优惠", "首发优惠"))
	if !v.IsAd {
		t.Fatalf("零宽还原后的证据不该被拦：%q", v.Reason)
	}

	// 证据本身带引号：剥掉引号再比对。
	v = evidenceGate(gateState("约炮加我"), gateVerdict("正文招揽", "「约炮」"))
	if !v.IsAd {
		t.Fatalf("带引号的证据不该被拦：%q", v.Reason)
	}
}

// 引用语境的放行条件：state 里真有 quoted，或历史条目带引用段标记。
func TestEvidenceGateQuoteContext(t *testing.T) {
	// 判词提引用而本条没有 quoted、历史里也没有引用段：拦。
	v := evidenceGate(gateState("u"), gateVerdict("载荷全在引用里，按广告论处", "u"))
	if v.IsAd {
		t.Fatal("无引用却谈引用载荷，应拦")
	}

	// 本条真有外部引用：放行。
	st := gateState("u")
	st.Quoted = &adQuotedInfo{Text: "日入5000 私聊我", IsExternal: true}
	if v := evidenceGate(st, gateVerdict("载荷全在外部引用里，典型规避形态", "日入5000")); !v.IsAd {
		t.Fatalf("真有 quoted 时不应拦：%q", v.Reason)
	}

	// 复查历史里带 `［引用·别人的话］` 标记：判词提引用是在说历史，放行。
	st = gateState("u")
	st.ReviewHistory = append(st.ReviewHistory,
		core.CtxMsg{Name: "路人", Text: "［引用·别人的话］日入5000 私聊我"})
	if v := evidenceGate(st, gateVerdict("历史里多次引用广告载荷，按账号广告处理", "日入5000 私聊我")); !v.IsAd {
		t.Fatalf("历史含引用段时不应拦：%q", v.Reason)
	}
}

// 提示词原文里的 token（t.me/joinchat、ping0.cc）是规则复述，不算捏造。
func TestEvidenceGateSkipsPromptVocab(t *testing.T) {
	v := evidenceGate(gateState("你好"), gateVerdict(
		"没有 t.me/joinchat 链接也不该出现 ping0.cc 之外的引流，判为广告", "你好"))
	if !v.IsAd {
		t.Fatalf("提示词复述不该被拦：%q", v.Reason)
	}
}

// 版本号、模型名不是域名，不参与核对；clean 结论原样通过。
func TestEvidenceGateIgnoresNonEvidence(t *testing.T) {
	v := evidenceGate(gateState("升级了"), gateVerdict("版本 4.6.1 与 3.1.0 的差异说明其在推广，判广告", "升级了"))
	if !v.IsAd {
		t.Fatalf("版本号不该当域名核对：%q", v.Reason)
	}
	clean := adVerdict{IsAd: false, Confidence: 0.9, Kind: "none", Reason: "正常交流"}
	if got := evidenceGate(gateState("你好"), clean); got.Reason != clean.Reason || got.IsAd {
		t.Fatal("clean 结论应原样通过")
	}
}

// 判词引了不存在的 @用户名：拦。
func TestEvidenceGateCatchesFabricatedHandle(t *testing.T) {
	v := evidenceGate(gateState("快来看看"), gateVerdict("正文引导联系 @fake_promoter_bot 购买，判广告"))
	if v.IsAd {
		t.Fatal("编造 @用户名应被拦")
	}
}

// 改正重试的提示词要点名不符项，并把逐字摘录、不得变形还原写死。
func TestEvidenceRetryNote(t *testing.T) {
	if !strings.Contains(evidenceRetryNote, "%s") ||
		strings.Count(evidenceRetryNote, "%s") != 1 {
		t.Error("改正提示应恰好有一个不符项占位符")
	}
	for _, want := range []string{"逐字", "evidence", "变形还原", "判定对象"} {
		if !strings.Contains(evidenceRetryNote, want) {
			t.Errorf("改正提示应包含 %q", want)
		}
	}
}

// parseLLMReply 解析 evidence 数组：多项、空项丢弃、超限截断。
func TestParseLLMReplyEvidence(t *testing.T) {
	// 模型常见的输出形态：JSON 被 ```json 围栏包着。
	content := "```json\n" + `{"is_ad":true,"confidence":0.9,"kind":"scam",` +
		`"scope":"message","severity":2,"reason":"刷单引流",` +
		`"evidence":[" 日入5000 ","","私聊我"],"profile_ok_hours":0}` + "\n```"
	raw, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, obj, err := parseLLMReply(aiReply{Raw: raw, Model: "test/model"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(v.Evidence) != 2 || v.Evidence[0] != "日入5000" || v.Evidence[1] != "私聊我" {
		t.Errorf("evidence 应去掉空项并 trim，得到 %#v", v.Evidence)
	}
	if !strings.HasPrefix(obj, "{") {
		t.Errorf("应返回 JSON 原文供改正重试回传，得到 %q", obj)
	}
	if misses := evidenceMisses(gateState("日入5000 私聊我"), v); len(misses) != 0 {
		t.Errorf("逐字证据不该判为不符：%v", misses)
	}
}

// 改正重试：第一轮证据编造 → 原提示词重发 + 错误说明，第二轮改正后放行；
// 请求体里必须同时带上原提示词与不符项说明。
func TestEvidenceRetryCorrectsVerdict(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	var calls atomic.Int32
	var retryBody string
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			w.Write([]byte(soReply("ad", 0.9, "scam", "message")))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			// 第一轮：声明了判定对象里根本不存在的证据。
			w.Write([]byte(llmRawJSON(`{"is_ad":true,"confidence":0.9,"kind":"scam",` +
				`"scope":"message","severity":2,"evidence":["日入5000 私聊我"],` +
				`"reason":"刷单引流"}`)))
			return
		}
		retryBody = string(body)
		// 第二轮：改正——证据不存在，按正常处理。
		w.Write([]byte(llmRawJSON(`{"is_ad":false,"confidence":0.8,"kind":"none",` +
			`"scope":"message","severity":0,"profile_ok_hours":24,"evidence":[],` +
			`"reason":"正文只是日常问候，无广告证据"}`)))
	})

	v, err := judgeLLM(b, b.Cache.Snap(), gateState("你好呀"), adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("应恰好调用两次（一次判定 + 一次改正），实际 %d", calls.Load())
	}
	if v.IsAd {
		t.Errorf("改正后应按正常处理，得到 %v", v)
	}
	for _, want := range []string{
		"你是 Telegram 群组的反广告审核员", // 原提示词随重发带上
		"未通过证据核对",               // 附带的错误说明
		"日入5000 私聊我",            // 上一轮的输出与不符项都要点名
		"逐字",                    // 二次强调摘录要求
	} {
		if !strings.Contains(retryBody, want) {
			t.Errorf("改正重试的请求应带上原提示词与不符项（缺 %q）", want)
		}
	}
}

// 改正重试仍编造：按未定放行，理由里写明不符项。
func TestEvidenceRetryStillFabricated(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			w.Write([]byte(soReply("ad", 0.9, "porn", "message")))
			return
		}
		// 两轮都给同一个不存在的证据。
		w.Write([]byte(llmRawJSON(`{"is_ad":true,"confidence":0.9,"kind":"porn",` +
			`"scope":"message","severity":3,"evidence":["约炮加我"],` +
			`"reason":"正文招揽"}`)))
	})

	v, err := judgeLLM(b, b.Cache.Snap(), gateState("早上好"), adVerdict{}, llmSystemPrompt)
	if err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	if v.IsAd {
		t.Fatal("两轮都编造证据的结论应按未定放行")
	}
	if !strings.Contains(v.Reason, "判词与输入不符") || !strings.Contains(v.Reason, "约炮加我") {
		t.Errorf("理由里应写明不符项，得到 %q", v.Reason)
	}
}

// llmRawJSON 把内层 JSON 包成 chat 补全响应。
func llmRawJSON(inner string) string {
	out, _ := json.Marshal(map[string]any{"choices": []any{
		map[string]any{"message": map[string]any{"content": inner}}}})
	return string(out)
}

// /check 判正常解除原判禁言的门槛：只认未解除、未到期的消息级禁言。
func TestFormalMessageMuteActive(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	ins := func(action string, age time.Duration) {
		b.Store.Write.Exec(`INSERT INTO antiad_log
			(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
			 action,reason,created_at,bot_id,lifted_at)
			VALUES (-100,555,7,'x','ad',0.9,'llm','scam',?, 'r',?, ?, ?)`,
			action, time.Now().Add(-age).Unix(), b.BotID(), 0)
	}

	// 全局默认 1440 分钟：22 小时前的禁言还在生效中。
	ins("deleted_muted", 22*time.Hour)
	if !formalMessageMuteActive(b, -100, 555) {
		t.Fatal("22 小时前的禁言应视为生效中")
	}
	// 25 小时前：默认时长下已自然到期，不算生效。
	b.Store.Write.Exec(`UPDATE antiad_log SET created_at=? WHERE user_id=555`,
		time.Now().Add(-25*time.Hour).Unix())
	if formalMessageMuteActive(b, -100, 555) {
		t.Fatal("过期的限时禁言不应视为生效")
	}
	// 永久禁言（antiad_mute_minutes=0）永远算生效。
	if err := b.PutSetting("antiad_mute_minutes", "0"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	if !formalMessageMuteActive(b, -100, 555) {
		t.Fatal("永久禁言应视为生效")
	}
	// 已解除的不算。
	b.Store.Write.Exec(`UPDATE antiad_log SET lifted_at=? WHERE user_id=555`,
		time.Now().Unix())
	if formalMessageMuteActive(b, -100, 555) {
		t.Fatal("已解除的禁言不应视为生效")
	}
	// 别的群、别的人不算。
	ins("muted", time.Hour)
	if formalMessageMuteActive(b, -100, 666) {
		t.Fatal("其他人的禁言不应算过来")
	}
}

// 流水理由中要带上复判声明的证据，便于管理员事后回看判定依据。
func TestLogAdKeepsEvidence(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	m := testutil.GroupMsg(-100, 555, 7, "日入5000 私聊我")
	id := logAd(b, m, gateVerdict("刷单引流", "日入5000", "私聊我"), "deleted_muted", "已删除")
	if id == 0 {
		t.Fatal("流水应写入")
	}
	row, ok := LoadAdLog(b.Store, id)
	if !ok {
		t.Fatal("记录应能读回")
	}
	if !strings.Contains(row.Reason, "证据：日入5000、私聊我") {
		t.Errorf("理由里应附证据，得到 %q", row.Reason)
	}
}
