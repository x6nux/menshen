package panel

import (
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// cb 造一条回调。
func cb(from int64, data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{ID: "q", From: &tg.TGUser{ID: from}, Data: data,
		Message: &tg.Message{MessageID: 9, Chat: &tg.Chat{ID: from, Type: "private"}}}
}

// seedLog 直接落一条流水。
func seedLog(t *testing.T, b *core.Bot, uid int64, text, action string) int64 {
	t.Helper()
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,text,
		verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,?,7,?,'ad',0.95,'systemone','scam',?,'',0,?)`, uid, text, action, b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestRecordCardRespectsTenancy：汇总里的「📋 #id」进记录卡片，带原文，只发到私聊；
// callback_data 是客户端发上来的，别人 bot 的记录不能看。
func TestRecordCardRespectsTenancy(t *testing.T) {
	b, fake, sh := testutil.NewTestBotOwned(t, 1, 100)
	if err := sh.AddAdmin(200, "另一个次管", 1); err != nil {
		t.Fatal(err)
	}
	id := seedLog(t, b, 42, "广告原文", "deleted")

	HandleAdminCallback(b, cb(200, "a:ad:rec:"+itoa(id)))
	if fake.CountCalls("sendMessage") != 0 {
		t.Error("别人名下的次管不该看到这条记录")
	}
	HandleAdminCallback(b, cb(100, "a:ad:rec:"+itoa(id)))
	p := fake.LastCall("sendMessage")
	if p == nil || !strings.Contains(p["text"].(string), "广告原文") {
		t.Fatalf("归属人应看到记录卡片: %v", p)
	}
}

// TestFalsePositiveForgetsHashAndUnbans：误判要撤掉内容哈希（否则同样的内容会
// 一直被直接删下去），封禁过的要解封且带 only_if_banned。
func TestFalsePositiveForgetsHashAndUnbans(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, 42, "日入过万", "deleted_banned")
	b.Store.Write.Exec(`INSERT INTO ad_hashes (bot_id,hash,log_id,created_at,last_hit_at)
		VALUES (?,?,?,0,0)`, b.BotID(), hashOf("日入过万"), id)

	HandleAdminCallback(b, cb(1, "a:ad:fp:"+itoa(id)))

	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_hashes`).Scan(&n)
	if n != 0 {
		t.Error("误判后内容哈希应撤掉")
	}
	p := fake.LastCall("unbanChatMember")
	if p == nil || p["only_if_banned"] != true {
		t.Errorf("封禁过的误判应解封且带 only_if_banned: %v", p)
	}
}

// TestManualMuteChannel：频道身份没有成员权限可改，人工禁言只能 banChatSenderChat。
func TestManualMuteChannel(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, -1009, "x", "deleted")
	HandleAdminCallback(b, cb(1, "a:ad:mute:"+itoa(id)))
	if fake.CountCalls("restrictChatMember") != 0 || fake.CountCalls("banChatSenderChat") != 1 {
		t.Error("频道身份的人工禁言应走 banChatSenderChat")
	}
}

// TestChatPunishCycles：每群处罚方式 跟随 → 禁言 → 封禁 → 跟随。
func TestChatPunishCycles(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	for _, want := range []int64{0, 1, -1} {
		HandleAdminCallback(b, cb(1, "a:mb:"+itoa(b.BotID())+":c:-100:pn"))
		if got := testutil.ChatConfOf(t, b, -100).Punish; got != want {
			t.Fatalf("punish = %d，期望 %d", got, want)
		}
	}
}
