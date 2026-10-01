package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

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
	did := ReleaseUser(b, row)
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
	did := ReleaseUser(b, row)
	if !strings.Contains(did, "解封") {
		t.Errorf("封禁记录应走解封，得到 %q", did)
	}
	un := fake.LastCall("unbanChatMember")
	if un == nil || un["only_if_banned"] != true {
		t.Errorf("解封必须带 only_if_banned，得到 %v", un)
	}
}
