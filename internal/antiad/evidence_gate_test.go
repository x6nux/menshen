package antiad

// 判词-输入一致性门的测试。两起线上误判（#21276、#26266）的判词原样
// 进来必须被拦下；正常判词（含引用语境、变形还原、提示词复述）必须放行。

import (
	"strings"
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

func gateVerdict(reason string) adVerdict {
	return adVerdict{IsAd: true, Confidence: 0.9, Kind: "porn",
		Decider: "llm", Reason: reason}
}

// 两起线上误判的判词，进门前必须被降为未定。
func TestEvidenceGateCatchesFabricatedReasons(t *testing.T) {
	// #21276：正文只有六个字，判词却引了不存在的域名与外部引用贴纸。
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

	// #26266：判词把「约炮」引成正文原文，正文里并没有。
	v = evidenceGate(gateState("现在不怕CF封了  无限邮箱 小号整起！"), gateVerdict(
		"正文「约炮」等招揽话术并附联系方式，属色情招揽"))
	if v.IsAd {
		t.Fatalf("引号捏造的判词应被拦下")
	}
	if !strings.Contains(v.Reason, "约炮") {
		t.Errorf("前缀应指出捏造的引文，得到 %q", v.Reason)
	}
}

// 判词引用的证据确实在输入里：原样放行。
func TestEvidenceGatePassesGroundedReasons(t *testing.T) {
	st := gateState("约炮加我 看主页 ldd26.xyz")
	st.Quoted = &adQuotedInfo{Text: "上门服务 https://t.me/+abc123xyz", From: "某频道", IsExternal: true}
	v := evidenceGate(st, gateVerdict(
		"正文「约炮」等招揽话术并附联系方式 ldd26.xyz，引用里还有 https://t.me/+abc123xyz"))
	if !v.IsAd {
		t.Fatalf("有真实证据的判词不该被拦： %q", v.Reason)
	}

	// 零宽字符拆词的原文，判词剥掉后引用：同一内容，放行。
	v = evidenceGate(gateState("首\u200c发\u200b优惠 私聊我"), gateVerdict("正文「首发优惠」属招揽话术"))
	if !v.IsAd {
		t.Fatalf("零宽还原后的引用不该被拦：%q", v.Reason)
	}
}

// 引用语境的放行条件：state 里真有 quoted，或历史条目带引用段标记。
func TestEvidenceGateQuoteContext(t *testing.T) {
	// 判词提「引用」而本条没有 quoted、历史里也没有引用段：拦。
	v := evidenceGate(gateState("u"), gateVerdict("载荷全在引用里，按广告论处"))
	if v.IsAd {
		t.Fatal("无引用却谈引用载荷，应拦")
	}

	// 本条真有外部引用：放行。
	st := gateState("u")
	st.Quoted = &adQuotedInfo{Text: "日入5000 私聊我", IsExternal: true}
	if v := evidenceGate(st, gateVerdict("载荷全在外部引用里，典型规避形态")); !v.IsAd {
		t.Fatalf("真有 quoted 时不应拦：%q", v.Reason)
	}

	// 复查历史里带「［引用·别人的话］」标记：判词提引用是在说历史，放行。
	st = gateState("u")
	st.ReviewHistory = append(st.ReviewHistory,
		core.CtxMsg{Name: "路人", Text: "［引用·别人的话］日入5000 私聊我"})
	if v := evidenceGate(st, gateVerdict("历史里多次引用广告载荷，按账号广告处理")); !v.IsAd {
		t.Fatalf("历史含引用段时不应拦：%q", v.Reason)
	}
}

// 提示词原文里的 token（t.me/joinchat、ping0.cc）是规则复述，不算捏造。
func TestEvidenceGateSkipsPromptVocab(t *testing.T) {
	v := evidenceGate(gateState("你好"), gateVerdict(
		"没有 t.me/joinchat 链接也不该出现 ping0.cc 之外的引流，判为广告"))
	if !v.IsAd {
		t.Fatalf("提示词复述不该被拦：%q", v.Reason)
	}
}

// 版本号、模型名不是域名，不参与核对；clean 结论原样通过。
func TestEvidenceGateIgnoresNonEvidence(t *testing.T) {
	v := evidenceGate(gateState("升级了"), gateVerdict("版本 4.6.1 与 3.1.0 的差异说明其在推广，判广告"))
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
