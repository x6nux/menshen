package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// groupSends 数一下发到群里的消息（私聊回执不算）。
func groupSends(fake *testutil.FakeTG) int {
	n := 0
	for _, c := range fake.Calls("sendMessage") {
		if c["chat_id"] == float64(-100) {
			n++
		}
	}
	return n
}

// TestGroupSilentSendsNothing：群内静默打开后，群里一条 bot 消息都不发，
// 但判定与处置照常（限制记录、兑换、流水都在）。
func TestGroupSilentSendsNothing(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	setGlobal(t, b, "antiad_group_silent", "1")

	// 冷判定限制通知：不发，但 join_mutes 记录照写。
	conf := testutil.ChatConfOf(t, b, -100)
	applyJoinMute(b, conf, &tg.TGUser{ID: 555, FirstName: "引流号"}, adVerdict{
		IsAd: true, Confidence: 0.95, Decider: "llm", Reason: "资料含引流"},
		"简介里有联系方式")
	if n := groupSends(fake); n != 0 {
		t.Errorf("静默时不该发任何群消息，发了 %d 条", n)
	}
	if _, ok := loadJoinMute(b.Store, -100, 555); !ok {
		t.Error("静默不该影响限制记录落库")
	}

	// 解禁码群内兑换：动作照做，但不发群消息。
	code := "MSU-7K2Q-9XFM"
	insertCodedAppeal(t, b, 555, code, time.Now().Add(time.Hour).Unix())
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`
	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 901, code))
	if n := groupSends(fake); n != 0 {
		t.Errorf("静默时兑换也不该在群里说话，发了 %d 条", n)
	}
	var wl int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_whitelist WHERE user_id=555`).Scan(&wl)
	if wl == 0 {
		t.Error("静默只影响发言，兑换本身要照常生效")
	}
	if n := fake.CountCalls("deleteMessage"); n == 0 {
		t.Error("静默时那条解禁码消息仍应删掉")
	}

	// 命令回复：/check 用法提示也不发。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 902, "/check"))
	if n := groupSends(fake); n != 0 {
		t.Errorf("静默时命令回复也不该发，发了 %d 条", n)
	}
}
