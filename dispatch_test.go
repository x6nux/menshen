package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// mainBotInvite 造一条「bot 自身成员状态变化」的更新。
func mainBotInvite(chatID int64, chatType, status string, isMember bool) *tg.Update {
	return &tg.Update{MyChatMember: &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: chatID, Type: chatType, Title: "测试群"},
		NewChatMember: &tg.ChatMemberInfo{
			User:   &tg.TGUser{ID: testutil.TestBotID},
			Status: status, IsMember: isMember,
		},
	}}
}

// TestMainBotLeavesGroupOnInvite 守的是「主 bot 不加群」：
// 被拉进群/频道时自动退出，且静默 —— 不通知、不回复。
func TestMainBotLeavesGroupOnInvite(t *testing.T) {
	cases := []struct {
		name     string
		chatType string
		status   string
	}{
		{"普通群成员", "supergroup", "member"},
		{"被设为管理员", "supergroup", "administrator"},
		{"频道管理员", "channel", "administrator"},
		{"受限但仍在群", "supergroup", "restricted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
			dispatch(b, mainBotInvite(-100999, c.chatType, c.status, true))

			last := fake.LastCall("leaveChat")
			if last == nil {
				t.Fatal("主 bot 被拉进群/频道时应调用 leaveChat")
			}
			if got := last["chat_id"]; got != float64(-100999) {
				t.Errorf("leaveChat 的 chat_id = %v，期望 -100999", got)
			}
			if n := fake.CountCalls("sendMessage"); n != 0 {
				t.Errorf("静默退出不该发任何消息，发了 %d 条", n)
			}
		})
	}
}

// TestMainBotDoesNotLeaveWhenNotInChat 确认 left/kicked 以及
// 「受限但已不在群」不会被误判成需要退出。
func TestMainBotDoesNotLeaveWhenNotInChat(t *testing.T) {
	for _, status := range []string{"left", "kicked"} {
		b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
		dispatch(b, mainBotInvite(-100999, "supergroup", status, false))
		if n := fake.CountCalls("leaveChat"); n != 0 {
			t.Errorf("status=%s 时不该调 leaveChat，调了 %d 次", status, n)
		}
	}

	b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	dispatch(b, mainBotInvite(-100999, "supergroup", "restricted", false))
	if n := fake.CountCalls("leaveChat"); n != 0 {
		t.Errorf("restricted 且 is_member=false 时不该调 leaveChat，调了 %d 次", n)
	}
}

// TestWorkerBotInviteDoesNotLeave 对照组：工作 bot 被拉进群照旧走反广告
// 的权限告警，不会退出 —— 它就是要待在群里干活的。
func TestWorkerBotInviteDoesNotLeave(t *testing.T) {
	b, fake, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	if err := b.PutSetting("antiad_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	dispatch(b, mainBotInvite(-100999, "supergroup", "member", true))
	if n := fake.CountCalls("leaveChat"); n != 0 {
		t.Errorf("工作 bot 不该退出群，leaveChat 调了 %d 次", n)
	}
	if n := fake.CountCalls("sendMessage"); n == 0 {
		t.Error("工作 bot 被拉进群但没给管理员权限时应私聊告警")
	}
}

// TestMainBotIgnoresGroupMessage 守的是「主 bot 不判定」：即使它名下还
// 挂着群配置（升级残留）或者退群失败留在群里，群消息也不得留底、不得
// 产生判定任务、不得产生任何 TG 调用。
func TestMainBotIgnoresGroupMessage(t *testing.T) {
	b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	testutil.EnableAntiad(t, b, -100123)

	dispatch(b, &tg.Update{Message: testutil.GroupMsg(-100123, 555, 900, "买号加我 @spam")})

	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("主 bot 不该给群消息留底，group_messages 有 %d 行", n)
	}
	if b.AdBusy() != 0 {
		t.Error("主 bot 不该提交判定任务")
	}
	if c := fake.CountCalls("deleteMessage"); c != 0 {
		t.Errorf("主 bot 不该删群消息，调了 deleteMessage %d 次", c)
	}
}

// TestWorkerBotStillHandlesGroupMessage 对照组：同样的群配置与消息，
// 工作 bot 照常留底。
func TestWorkerBotStillHandlesGroupMessage(t *testing.T) {
	b, _, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	testutil.EnableAntiad(t, b, -100123)

	dispatch(b, &tg.Update{Message: testutil.GroupMsg(-100123, 555, 900, "")})

	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("工作 bot 应给群消息留底 1 行，得到 %d 行", n)
	}
}

// TestStaffStartWithLogDeepLink：管理员从群内告警点「打开 bot 处理」
// （start=log<记录号>）时直接落到那条记录卡片；普通 /start 仍是主菜单。
func TestStaffStartWithLogDeepLink(t *testing.T) {
	b, fake, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)

	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,
		 ad_kind,action,reason,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.BotID(), -100, 555, 7, "加微信买号", "ad", 0.9, "so",
		"promo", "deleted_muted", "推广", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	dispatch(b, &tg.Update{Message: &tg.Message{
		MessageID: 1, Date: 1700000000,
		Text: fmt.Sprintf("/start log%d", id),
		From: &tg.TGUser{ID: 777, Username: "admin"},
		Chat: &tg.Chat{ID: 777, Type: "private"},
	}})
	last := fake.LastCall("sendMessage")
	if last == nil || !strings.Contains(last["text"].(string), "记录 #") {
		t.Fatalf("深链应渲染记录 #%d 的卡片，得到 %v", id, last)
	}
}

// TestStaffStartWithUnbanDeepLink：管理员点群内冷判定通知的「📝 申诉」
// （ub<记录号>）时，直接落到那条冷判定记录卡片。
func TestStaffStartWithUnbanDeepLink(t *testing.T) {
	b, fake, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(bot_id,chat_id,user_id,message_id,text,verdict,confidence,decider,
		 ad_kind,action,reason,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.BotID(), -100, 555, 0, "资料里有联系方式", "ad", 1, "systemone",
		"scam", "join_muted", "进群冷判定", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	dispatch(b, &tg.Update{Message: &tg.Message{
		MessageID: 1, Date: 1700000000,
		Text: fmt.Sprintf("/start ub%d", id),
		From: &tg.TGUser{ID: 777, Username: "admin"},
		Chat: &tg.Chat{ID: 777, Type: "private"},
	}})
	last := fake.LastCall("sendMessage")
	if last == nil || !strings.Contains(last["text"].(string), "记录 #") {
		t.Fatalf("ub 深链应打开冷判定记录 #%d，得到 %v", id, last)
	}
}

// TestMainBotNonStaffStartSaysNoPermission：主 bot 对外只有一句「没有权限」。
// 它只做配置管理与接入其它 bot，被拉进群会自动退出 —— 「拉进群并设为管理员」
// 这种引导对它就是错的。工作 bot 的同类提示保持不变（它确实要在群里干活）。
func TestMainBotNonStaffStartSaysNoPermission(t *testing.T) {
	msg := func() *tg.Update {
		return &tg.Update{Message: &tg.Message{
			MessageID: 1, Date: 1700000000, Text: "/start",
			From: &tg.TGUser{ID: 12345, Username: "random"},
			Chat: &tg.Chat{ID: 12345, Type: "private"},
		}}
	}

	mainBot, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	dispatch(mainBot, msg())
	p := fake.LastCall("sendMessage")
	if p == nil {
		t.Fatal("非管理员私聊 /start 应得到明确回复")
	}
	if text := p["text"].(string); strings.Contains(text, "拉进群") {
		t.Errorf("主 bot 不该引导别人把它拉进群:\n%s", text)
	} else if !strings.Contains(text, "没有权限") {
		t.Errorf("无权限应如实说明:\n%s", text)
	}

	workBot, fake2, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	dispatch(workBot, msg())
	p2 := fake2.LastCall("sendMessage")
	if p2 == nil {
		t.Fatal("工作 bot 对陌生人也应回复")
	}
	if text := p2["text"].(string); strings.Contains(text, "拉进群") {
		t.Errorf("工作 bot 也不该引导拉群（群要管理员在面板里添加）:\n%s", text)
	} else if !strings.Contains(text, "没有被本 bot 限制") {
		t.Errorf("没有限制时应如实告知，得到:\n%s", text)
	}
}

// TestWorkBotNonStaffStartOnGbanShowsAppeal：被联合封禁的人私聊 /start
// 应直接进申诉入口 —— 普通用户来找 bot 多半就是为了解封。
func TestWorkBotNonStaffStartOnGbanShowsAppeal(t *testing.T) {
	b, fake, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	if err := antiad.GbanAdd(b.Shared, 12345, "赌博引流", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	dispatch(b, &tg.Update{Message: &tg.Message{
		MessageID: 1, Date: 1700000000, Text: "/start",
		From: &tg.TGUser{ID: 12345, Username: "random"},
		Chat: &tg.Chat{ID: 12345, Type: "private"},
	}})
	p := fake.LastCall("sendMessage")
	if p == nil || !strings.Contains(p["text"].(string), "联合封禁") {
		t.Fatalf("被联合封禁的人 /start 应进申诉入口并写明封禁，得到 %v", p)
	}
}

// TestMainBotGlobalGbanAppeal：全局联合封禁的申诉主 bot 也受理 —— 它是
// 全局组对外的入口；专属组的封禁才归对应的工作 bot。
func TestMainBotGlobalGbanAppeal(t *testing.T) {
	mainBot, fake, sh := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	if err := antiad.GbanAdd(sh, 12345, "赌博引流", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	dispatch(mainBot, &tg.Update{Message: &tg.Message{
		MessageID: 1, Date: 1700000000, Text: "/start",
		From: &tg.TGUser{ID: 12345, Username: "random"},
		Chat: &tg.Chat{ID: 12345, Type: "private"},
	}})
	p := fake.LastCall("sendMessage")
	if p == nil || !strings.Contains(p["text"].(string), "联合封禁") {
		t.Fatalf("全局封禁的人 /start 应进申诉入口，得到 %v", p)
	}
}

// TestMainBotStillServesPanel 确认限制只针对「群」，私聊面板不受影响：
// 配置管理与接入其他 bot 正是主 bot 的本职。
func TestMainBotStillServesPanel(t *testing.T) {
	b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	dispatch(b, &tg.Update{Message: &tg.Message{
		MessageID: 1, Date: 1700000000, Text: "/start",
		From: &tg.TGUser{ID: 777, Username: "admin"},
		Chat: &tg.Chat{ID: 777, Type: "private"},
	}})
	if n := fake.CountCalls("sendMessage"); n == 0 {
		t.Error("管理员私聊 /start 时，主 bot 的管理面板应照常响应")
	}
}
