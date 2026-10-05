package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestManualMarkByRecordDropsProfileOK：配置台「标记广告」与群内 /banad 是
// 同一个语义，人工结论落地后先前的资料放行必须一起作废 —— 否则这份放行还能
// 继续挡后续判定的资料一路，等于人工结论被一份过期的自动放行架空。演练群
// 不动真实状态，与 /banad 的分支保持一致（见 TestAdbDropsProfileOK）。
func TestManualMarkByRecordDropsProfileOK(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	p := senderProfile{UserID: 555, FirstName: "广告位"}
	if GrantProfileOK(b, p, 48, "先前的放行") == 0 {
		t.Fatal("预置放行失败")
	}
	ManualMarkByRecord(b, conf, -100, 10, 555, "广告", 1)
	if n := countRows(t, b, `SELECT COUNT(*) FROM profile_ok`); n != 0 {
		t.Errorf("配置台人工标记广告后应撤销资料放行，剩 %d 行", n)
	}

	// 演练群不处置：真实的放行也不该被撤。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b2, -100, true)
	conf2 := testutil.ChatConfOf(t, b2, -100)
	p2 := senderProfile{UserID: 555, FirstName: "广告位"}
	if GrantProfileOK(b2, p2, 48, "先前的放行") == 0 {
		t.Fatal("预置放行失败")
	}
	ManualMarkByRecord(b2, conf2, -100, 10, 555, "广告", 1)
	if n := countRows(t, b2, `SELECT COUNT(*) FROM profile_ok`); n != 1 {
		t.Errorf("演练群不该撤真实放行，剩 %d 行", n)
	}
}

// TestReleaseUserKeepsVerdict：解封是「单纯放人」，判定维持不变 ——
// 记录动作不改写、内容哈希不被撤、命中数不回退，只撤掉生效中的限制、
// 清掉记录，并在理由里留一行痕。
func TestReleaseUserKeepsVerdict(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	// 一条真实处置：删+禁言，另有进群限制与内容哈希。
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,7,'加微信 日入5000','ad',0.95,'llm','scam','deleted_muted',
		 '账号资料写着引流话术',?,?)`, time.Now().Unix(), b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	applyJoinMute(b, conf, &tg.TGUser{ID: 555}, adVerdict{IsAd: true, Confidence: 0.9,
		Decider: "systemone", Reason: "资料"}, "资料")
	rememberAdHash(b, "加微信 日入5000", adVerdict{Kind: "scam", Confidence: 0.9}, id)

	row, ok := LoadAdLog(b.Store, id)
	if !ok {
		t.Fatal("记录应能读回")
	}
	did := ReleaseUser(b, row, 1)
	if !strings.Contains(did, "解除禁言") {
		t.Errorf("应解除禁言，得到 %q", did)
	}

	// 判定维持：动作标签不改写成 undone，理由里留痕。
	after, _ := LoadAdLog(b.Store, id)
	if after.Action != "deleted_muted" {
		t.Errorf("解封不该改写判定动作，得到 %q", after.Action)
	}
	if !strings.Contains(after.Reason, "管理员解封（判定维持）") {
		t.Errorf("理由里应留痕，得到 %q", after.Reason)
	}
	// 内容哈希还在（同样的内容再出现仍会被直接删）。
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_hashes WHERE bot_id=?`,
		b.BotID()).Scan(&n)
	if n != 1 {
		t.Errorf("解封不该撤内容哈希，得到 %d 条", n)
	}
	// 记录清干净：进群限制行没了、处罚流水标成已解除。
	if n := countRows(t, b, `SELECT COUNT(*) FROM join_mutes WHERE user_id=555`); n != 0 {
		t.Errorf("进群限制应被清掉，剩 %d 条", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log
		WHERE id=? AND lifted_at=0`, id); n != 0 {
		t.Errorf("处罚流水应标成已解除，剩 %d 条", n)
	}
	// 解禁言那次调用是「权限全开」。
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("应发一次权限全开的解禁言")
	}
}

// TestReleaseUserUnbansBanRecords：记录上是封禁的走解封（only_if_banned）。
func TestReleaseUserUnbansBanRecords(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,556,8,'加微信','ad',0.99,'llm','scam','deleted_banned','x',?,?)`,
		time.Now().Unix(), b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	row, _ := LoadAdLog(b.Store, id)
	did := ReleaseUser(b, row, 1)
	if !strings.Contains(did, "解封") {
		t.Errorf("封禁记录应走解封，得到 %q", did)
	}
	un := fake.LastCall("unbanChatMember")
	if un == nil || un["only_if_banned"] != true {
		t.Errorf("解封必须带 only_if_banned，得到 %v", un)
	}
}

// TestUndoVerdictPrewarmMuted：误判一条 prewarm_muted 流水也要走解禁并
// 清掉 join_mutes 记录 —— 少了这一步，人还是发不了言，而复查任务还会
// 按残留记录再禁回去。
func TestUndoVerdictPrewarmMuted(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,557,7,'［前置号复核］','ad',0.95,'systemone','promo',
		 'prewarm_muted','疑似批量注册的广告前置号',?,?)`,
		time.Now().Unix(), b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	saveJoinMute(b, -100, 557, kindPrewarm, "疑似批量注册的广告前置号", 0)
	row, ok := LoadAdLog(b.Store, id)
	if !ok {
		t.Fatal("记录应能读回")
	}

	if _, ok2, desc := UndoVerdict(b, row, 1); !ok2 {
		t.Fatalf("误判解禁失败：%s", desc)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM join_mutes WHERE user_id=557`); n != 0 {
		t.Errorf("prewarm 限制记录应被清掉，剩 %d 条", n)
	}
	if after, _ := LoadAdLog(b.Store, id); after.Action != "undone" {
		t.Errorf("判定应改判 undone，得到 %q", after.Action)
	}
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("应发一次权限全开的解禁言")
	}
}

// TestAdHitsCounted：只有当初真的加过 ad_hits 的记录才允许在「误判」时递减。
// 进群类处置、manual-ban 与演练记录都会让无条件递减抹掉真实命中数。
func TestAdHitsCounted(t *testing.T) {
	cases := []struct {
		name string
		r    AdLogRow
		want bool
	}{
		{"自动广告处置", AdLogRow{Verdict: "ad", Decider: "systemone", Action: "deleted_muted"}, true},
		{"禁言档", AdLogRow{Verdict: "ad", Decider: "llm", Action: "muted"}, true},
		{"封禁档", AdLogRow{Verdict: "ad", Decider: "llm", Action: "banned"}, true},
		{"演练不计数", AdLogRow{Verdict: "ad", Decider: "llm", Action: "dryrun:deleted_muted"}, false},
		{"进群资料限制", AdLogRow{Verdict: "ad", Decider: "systemone", Action: "join_muted"}, false},
		{"前置号限制", AdLogRow{Verdict: "ad", Decider: "llm", Action: "prewarm_muted"}, false},
		{"进群检查未处置", AdLogRow{Verdict: "clean", Decider: "systemone", Action: "join_checked"}, false},
		{"人工封禁不计入", AdLogRow{Verdict: "ad", Decider: "manual-ban", Action: "banned"}, false},
		{"护栏未送检", AdLogRow{Verdict: "skipped", Decider: "skipped", Action: "none"}, false},
		{"已误判", AdLogRow{Verdict: "ad", Decider: "systemone", Action: "undone"}, false},
	}
	for _, c := range cases {
		if got := adHitsCounted(c.r); got != c.want {
			t.Errorf("%s: adHitsCounted=%v，期望 %v", c.name, got, c.want)
		}
	}
}
