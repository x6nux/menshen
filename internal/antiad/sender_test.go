package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

const channelBotID = 136817688 // TG 的 Channel_Bot，所有频道共用

// channelMsg 造一条以频道身份发出的群消息。
func channelMsg(chatID, channelID, msgID int64, text string) *tg.Message {
	m := testutil.GroupMsg(chatID, channelBotID, msgID, text)
	m.From.IsBot, m.From.Username = true, "Channel_Bot"
	m.SenderChat = &tg.Chat{ID: channelID, Type: "channel", Title: "日赚频道"}
	return m
}

// TestSenderOf 覆盖实际发言者的几种换法。
func TestSenderOf(t *testing.T) {
	// 频道身份：换成频道（负 ID），频道名进 first_name
	if u := senderOf(channelMsg(-100, -1009, 1, "x")); u.ID != -1009 || u.FirstName != "日赚频道" {
		t.Errorf("频道身份 = %+v", u)
	}
	// 匿名管理员：sender_chat 是本群，保持原样由豁免处理
	m := testutil.GroupMsg(-100, groupAnonymousBotID, 1, "x")
	m.SenderChat = &tg.Chat{ID: -100}
	if senderOf(m).ID != groupAnonymousBotID {
		t.Error("匿名管理员不该被换掉")
	}
	// 关联频道自动转发：from 是 777000，保持原样
	m = testutil.GroupMsg(-100, tgServiceUID, 1, "x")
	m.SenderChat = &tg.Chat{ID: -1005}
	if senderOf(m).ID != tgServiceUID {
		t.Error("自动转发不该被换掉")
	}
	// 访客 bot：换成召唤者
	m = testutil.GroupMsg(-100, 999, 1, "x")
	m.From.IsBot = true
	m.GuestBotCallerUser = &tg.TGUser{ID: 55}
	if senderOf(m).ID != 55 {
		t.Error("访客 bot 代发的应算在召唤者头上")
	}
}

// TestGuestBotJudgedAsCaller：「bot 一律豁免」对访客 bot 不成立，否则随便 @ 几个
// 广告 bot 就能绕过检测。留底、画像记在召唤者名下，正文标出是哪个 bot 代发的。
func TestGuestBotJudgedAsCaller(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, _ := fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))

	m := testutil.GroupMsg(-100, 999, 7, "日入过万私聊")
	m.From.IsBot, m.From.Username = true, "AdsBot"
	m.GuestBotCallerUser = &tg.TGUser{ID: 55, Username: "caller"}
	HandleGroupMessage(b, m)
	waitIdle(t, b)

	if soN.Load() != 1 {
		t.Fatalf("访客 bot 代发的消息应当送检，实际 %d 次", soN.Load())
	}
	var uid int64
	var text string
	b.Store.Read.QueryRow(`SELECT user_id,text FROM group_messages WHERE message_id=7`).Scan(&uid, &text)
	if uid != 55 || !strings.Contains(text, "［访客 bot］@AdsBot") {
		t.Errorf("留底 uid=%d text=%q；应记在召唤者名下并标出访客 bot", uid, text)
	}
}

// TestChannelIdentityJudgedAndBannedAsChannel：以频道身份发言不能按 bot 豁免；
// 处置落在频道上（banChatSenderChat），不能落到 Channel_Bot 这个公共账号。
func TestChannelIdentityJudgedAndBannedAsChannel(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.97, "scam", "message"), llmReply(true, 0.97, "scam", "message"))

	HandleGroupMessage(b, channelMsg(-100, -1009, 8, "日入过万私聊"))
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("频道身份不该 restrictChatMember（%d 次）", n)
	}
	calls := fake.Calls("banChatSenderChat")
	if len(calls) == 0 || int64(calls[len(calls)-1]["sender_chat_id"].(float64)) != -1009 {
		t.Fatalf("应当 banChatSenderChat 这个频道: %v", calls)
	}
	_, _, _, uid, _ := lastLog(t, b)
	if uid != -1009 {
		t.Errorf("流水记在 %d 名下，应为频道 -1009", uid)
	}
}

// TestAdExemptSpecialSenders：匿名管理员与自动转发是群主一方，判定成员 bot
// 打开后也不判；成员 bot 默认豁免，打开 antiad_judge_bots 后才判。
func TestAdExemptSpecialSenders(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	snap := b.Cache.Snap()
	if !adExempt(b, snap, -100, &tg.TGUser{ID: groupAnonymousBotID, IsBot: true}) {
		t.Error("匿名管理员应豁免")
	}
	if !adExempt(b, snap, -100, &tg.TGUser{ID: 7, IsBot: true}) {
		t.Error("成员 bot 默认应豁免")
	}
	if err := b.PutBotSetting(b.BotID(), "antiad_judge_bots", "1"); err != nil {
		t.Fatal(err)
	}
	snap = b.Cache.Snap()
	if adExempt(b, snap, -100, &tg.TGUser{ID: 7, IsBot: true}) {
		t.Error("开启判定成员 bot 后，非管理员 bot 不该豁免")
	}
	if !adExempt(b, snap, -100, &tg.TGUser{ID: groupAnonymousBotID, IsBot: true}) {
		t.Error("开启判定成员 bot 后匿名管理员仍应豁免")
	}
}

// TestLinkedChannelIsChatAdmin：频道当不了群管理员，与之对等的是本群的关联频道。
func TestLinkedChannelIsChatAdmin(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	fake.Resp["getChat"] = `{"ok":true,"result":{"linked_chat_id":-1005}}`
	if !IsChatAdmin(b, -100, -1005) {
		t.Error("本群关联频道应视同管理员")
	}
	if IsChatAdmin(b, -100, -1009) {
		t.Error("别的频道不是管理员")
	}
	if fake.CountCalls("getChatMember") != 0 {
		t.Error("频道身份不该查 getChatMember")
	}
}

// TestUserLinkChannel：频道没有 tg://user 链接，名字也不写。
func TestUserLinkChannel(t *testing.T) {
	if s := userLink(-1009); strings.Contains(s, "tg://user") || !strings.Contains(s, "-1009") {
		t.Errorf("userLink(频道) = %q", s)
	}
}

// TestEditedMessageRejudged：编辑成广告要重新送检；正文没变的「编辑」不花钱；
// 编辑不计发言数；编辑一条旧命令不再执行。
func TestEditedMessageRejudged(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, _ := fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 5, "大家好"))
	waitIdle(t, b)

	same := testutil.GroupMsg(-100, 42, 5, "大家好")
	same.EditDate = 1700000050
	HandleGroupMessage(b, same)
	waitIdle(t, b)
	if soN.Load() != 1 {
		t.Errorf("正文没变的编辑不该再送检，送检 %d 次", soN.Load())
	}

	ed := testutil.GroupMsg(-100, 42, 5, "日入过万 私聊")
	ed.EditDate = 1700000060
	HandleGroupMessage(b, ed)
	waitIdle(t, b)
	if soN.Load() != 2 {
		t.Errorf("编辑成新内容应当重新送检，送检 %d 次", soN.Load())
	}
	if gm, _ := loadMember(b.Store, -100, 42); gm.MsgCount != 1 {
		t.Errorf("编辑不该计发言数，msg_count = %d", gm.MsgCount)
	}
	var text string
	b.Store.Read.QueryRow(`SELECT text FROM group_messages WHERE message_id=5`).Scan(&text)
	if text != "日入过万 私聊" {
		t.Errorf("留底应覆盖为编辑后的正文，得到 %q", text)
	}

	cmd := testutil.GroupMsg(-100, 42, 6, "/check 42")
	cmd.EditDate = 1700000070
	before := fake.CountCalls("sendMessage")
	HandleGroupMessage(b, cmd)
	waitIdle(t, b)
	if fake.CountCalls("sendMessage") != before {
		t.Error("编辑一条旧命令不该再执行")
	}
}

// TestAdCommandAcceptsChannelID：以频道身份发言的记录记在频道名下，/check 要认负 ID。
func TestAdCommandAcceptsChannelID(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "/check -1009"))
	if p := fake.LastCall("sendMessage"); p != nil && strings.Contains(p["text"].(string), "用法") {
		t.Error("负数的频道 ID 不该被当成用法错误")
	}
}
