package panel

import (
	"fmt"
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

// TestAdChatHealthCached：群权限自检要发 getChatMember，而它跑在 bot 的
// 串行更新路径上（TG 慢时最坏 40 秒不收新消息）。同一群的重复渲染必须
// 命中缓存。
func TestAdChatHealthCached(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	for i := 0; i < 3; i++ {
		if got := adChatHealth(b, -100); !strings.Contains(got, "权限正常") {
			t.Fatalf("应识别为管理员，得到 %q", got)
		}
	}
	if n := fake.CountCalls("getChatMember"); n != 1 {
		t.Errorf("三次渲染只该查一次，实际 %d 次", n)
	}
}

// TestChatDetailShowsPunishDuration：群详情要写明禁言的时长与实际处罚，
// 并在未开启封禁时给出改法。只写「禁言」会让人以为永久封禁已生效。
func TestChatDetailShowsPunishDuration(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	showChatDetail(b, 777, 0, b.BotID(), -100)
	text := fmt.Sprint(fake.LastCall("sendMessage")["text"])
	if !strings.Contains(text, "禁言 24 小时") || !strings.Contains(text, "要改成永久禁言") {
		t.Fatalf("应写明禁言时长与改法:\n%s", text)
	}

	// 禁言时长 0 = 永久禁言：人留在群里、发不了言。
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_hours", "0"); err != nil {
		t.Fatal(err)
	}
	showChatDetail(b, 777, 0, b.BotID(), -100)
	text = fmt.Sprint(fake.LastCall("sendMessage")["text"])
	if !strings.Contains(text, "永久禁言") || strings.Contains(text, "要改成永久禁言") {
		t.Fatalf("配成 0 后应显示永久禁言且不再提示改法:\n%s", text)
	}

	// 开启封禁后显示永久封禁出群（优先级高于禁言时长）。
	if err := b.PutBotSetting(b.BotID(), "antiad_ban", "1"); err != nil {
		t.Fatal(err)
	}
	showChatDetail(b, 777, 0, b.BotID(), -100)
	text = fmt.Sprint(fake.LastCall("sendMessage")["text"])
	if !strings.Contains(text, "封禁出群（永久）") {
		t.Fatalf("开启封禁后应显示永久封禁:\n%s", text)
	}
	if strings.Contains(text, "要改成永久") {
		t.Error("已开启封禁时不该再提示改法")
	}
}
