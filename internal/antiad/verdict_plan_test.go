package antiad

import (
	"encoding/json"
	"testing"

	"menshen/internal/testutil"
)

// TestLLMPlanParsed：复判输出解析成处置意见（delete / mute），不再有置信度；
// 认不出的禁言时长按不禁言处理，判为正常时处置意见一律作废。
func TestLLMPlanParsed(t *testing.T) {
	cases := []struct {
		name       string
		isAd       bool
		del        bool
		mute       string
		wantDelete bool
		wantMute   string
	}{
		{"删+5分钟", true, true, "5m", true, muteShort},
		{"删+永久", true, true, "forever", true, muteForever},
		{"判广告但不动手", true, false, "none", false, muteNone},
		{"认不出的时长按不禁言", true, true, "MUTE_7D", true, muteNone},
		{"判正常时处置作废", false, true, "forever", false, muteNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := llmReplyDec(c.isAd, "promo", "message", c.del, c.mute)
			v, _, err := parseLLMReply(aiReply{Raw: json.RawMessage(raw)})
			if err != nil {
				t.Fatalf("parseLLMReply: %v", err)
			}
			if !v.Planned {
				t.Error("复判结论必须标记 Planned")
			}
			if v.Confidence != 0 {
				t.Errorf("复判不该再带置信度，得到 %v", v.Confidence)
			}
			if v.Delete != c.wantDelete || v.Mute != c.wantMute {
				t.Errorf("处置意见 = delete:%v mute:%q，想要 delete:%v mute:%q",
					v.Delete, v.Mute, c.wantDelete, c.wantMute)
			}
		})
	}
}

// TestPlannedAction：复判给的处置意见直接决定动作与时长；老人免禁言兜底与
// 高危害例外照旧生效。
func TestPlannedAction(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	snap := b.Cache.Snap()
	ad := func(del bool, mute, kind string, sev float64) adVerdict {
		return adVerdict{IsAd: true, Planned: true, Delete: del, Mute: mute,
			Kind: kind, Severity: sev, Scope: "message"}
	}

	// 新人 + 5 分钟：删 + 限时禁言。
	if act := decideAction(b, snap, true, ad(true, muteShort, "promo", 1)); act.Name != "deleted_muted" ||
		act.MuteFor != tempMute || !act.Delete {
		t.Errorf("5 分钟档不对: %+v", act)
	}
	// 新人 + 永久：删 + 永久禁言。
	if act := decideAction(b, snap, true, ad(true, muteForever, "promo", 1)); act.MuteFor != mutePermanent {
		t.Errorf("永久档应给 mutePermanent，得到 %v", act.MuteFor)
	}
	// 老人 + 普通广告：免禁言兜底，只删。
	if act := decideAction(b, snap, false, ad(true, muteForever, "promo", 1)); act.Mute ||
		act.Name != "deleted" {
		t.Errorf("老人应只删不禁: %+v", act)
	}
	// 老人 + 高危害：例外，仍禁言。
	if act := decideAction(b, snap, false, ad(true, muteForever, "scam", 2)); !act.Mute {
		t.Errorf("高危害不该被老人豁免: %+v", act)
	}
	// 判广告但不动手：仅告警。
	if act := decideAction(b, snap, true, ad(false, muteNone, "promo", 1)); act.Name != "alerted" ||
		act.Delete || act.Mute {
		t.Errorf("不动手时应仅告警: %+v", act)
	}
	// 账号本身就是广告号：模型给了 5m 也按永久处理。
	acc := ad(true, muteShort, "promo", 1)
	acc.Scope = "account"
	if act := decideAction(b, snap, true, acc); act.MuteFor != mutePermanent {
		t.Errorf("账号级广告该死禁永久，得到 %v", act.MuteFor)
	}
}

// TestStagesStoredSeparately：初判与复判各自的原始结论分开落库，复判不再
// 覆盖初判；confidence 列落初判置信度，面板的置信度列不至于变成 0%。
func TestStagesStoredSeparately(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.88, "scam", "message"),
		llmReplyDec(true, "scam", "message", true, muteShort))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万 私聊"))
	waitIdle(t, b)

	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("读流水 id: %v", err)
	}
	row, ok := LoadAdLog(b.Store, id)
	if !ok {
		t.Fatal("流水读不回来")
	}
	so, llm := row.SoStage(), row.LlmStage()
	if so == nil || !so.IsAd || so.Confidence != 0.88 {
		t.Errorf("初判级结论没单独存: %+v", so)
	}
	if llm == nil || !llm.IsAd || !llm.Delete || llm.Mute != muteShort {
		t.Errorf("复判级结论没单独存: %+v", llm)
	}
	if row.Confidence != 0.88 {
		t.Errorf("confidence 列应落初判置信度，得到 %v", row.Confidence)
	}
	if row.Decider != "systemone+llm" {
		t.Errorf("decider = %q", row.Decider)
	}
}
