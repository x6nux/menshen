package antiad

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// TestGuestBotJudgedAsCaller：bot 一律豁免对访客 bot 不成立，否则随意 @ 几个
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

// TestAdExemptSpecialSenders：匿名管理员与自动转发是群主一方，任何开关下
// 都不判；普通成员 bot 默认照判（有管理员权限的 bot 由末尾的群管理员判断
// 豁免），关掉 `判定普通成员 bot` 才全豁免。
func TestAdExemptSpecialSenders(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	snap := b.Cache.Snap()
	if !adExempt(b, snap, -100, &tg.TGUser{ID: groupAnonymousBotID, IsBot: true}, false) {
		t.Error("匿名管理员应豁免")
	}
	if adExempt(b, snap, -100, &tg.TGUser{ID: 7, IsBot: true}, false) {
		t.Error("没有管理员权限的 bot 默认应照判")
	}
	if err := b.PutBotSetting(b.BotID(), "antiad_judge_bots", "0"); err != nil {
		t.Fatal(err)
	}
	snap = b.Cache.Snap()
	if !adExempt(b, snap, -100, &tg.TGUser{ID: 7, IsBot: true}, false) {
		t.Error("关闭判定普通成员 bot 后应豁免")
	}
	if !adExempt(b, snap, -100, &tg.TGUser{ID: groupAnonymousBotID, IsBot: true}, false) {
		t.Error("匿名管理员仍应豁免")
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

// TestEditedMessageRejudged：编辑成广告要重新送检；正文没变的编辑不触发
// 调用；编辑不计发言数；编辑一条已发命令不再执行。
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

// TestResolveLinkBotAbout：bot 的简介只有公开预览页给得到（getChat 对别的
// bot 只返回名字与用户名），没设置简介时预览页给的默认文案不能当成简介；
// 本服务判过它广告、或它还在联合封禁名单里时 ad_known=true。
//
// 名字本身分不出正常 bot 与广告 bot，需结合简介判断。
func TestResolveLinkBotAbout(t *testing.T) {
	pages := map[string]string{
		"/CleanBot": `<html><head><meta property="og:title" content="管家 Bot">` +
			`<meta property="og:description" content="只做私聊答疑，随手记点笔记。"></head></html>`,
		"/PlainBot": `<meta property="og:title" content="Plain">` +
			`<meta property="og:description" content="You can contact @PlainBot right away.">`,
		"/AdBot": `<meta property="og:description" content="日入5000 &amp; 私聊领福利，加群上号">`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := pages[r.URL.Path]; ok {
			io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	oldURL := botAboutURL
	botAboutURL = srv.URL + "/"
	defer func() { botAboutURL = oldURL }()

	b, fake := testutil.NewTestBot(t, 1)
	ids := map[string]int64{"CleanBot": 9001, "PlainBot": 9002, "AdBot": 9003}
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method != "getChat" {
			return "", false
		}
		handle := strings.TrimPrefix(fmt.Sprint(p["chat_id"]), "@")
		id, ok := ids[handle]
		if !ok {
			return `{"ok":false}`, true
		}
		return fmt.Sprintf(`{"ok":true,"result":{"id":%d,"type":"private",
			"first_name":"Child","last_name":"killer"}}`, id), true
	}
	// AdBot 在本服务有广告案底：它自己发过的消息被判过广告。
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id)
		VALUES (-100,9003,1,'加群上号','ad',0.99,'llm','scam','deleted','测试用案底',
		 0,0,0,1700000000,?)`,
		b.BotID()); err != nil {
		t.Fatal(err)
	}

	clean := resolveLink(b, "CleanBot")
	if clean.Kind != "bot" || clean.Title != "Child killer" {
		t.Errorf("bot 应认出来并带标题，得到 %+v", clean)
	}
	if clean.About != "只做私聊答疑，随手记点笔记。" {
		t.Errorf("简介要取自预览页，得到 %q", clean.About)
	}
	if clean.AdKnown {
		t.Error("没有案底的 bot 不该标成广告账号")
	}

	plain := resolveLink(b, "PlainBot")
	if plain.About != "" {
		t.Errorf("没设置简介时预览页的默认文案不该当简介，得到 %q", plain.About)
	}

	ad := resolveLink(b, "AdBot")
	if !ad.AdKnown {
		t.Error("判过广告的 bot 该标 ad_known")
	}
	if ad.About != "日入5000 & 私聊领福利，加群上号" {
		t.Errorf("简介要解 HTML 实体，得到 %q", ad.About)
	}

	// 名单里的账号也算案底（不只看流水）。
	if err := GbanAdd(b.Shared, 9004, "测试", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	ids["ListedBot"] = 9004
	pages["/ListedBot"] = `<meta property="og:description" content="私人助理">`
	if !resolveLink(b, "ListedBot").AdKnown {
		t.Error("还在联合封禁名单里的账号该标 ad_known")
	}
}

// TestBioLinksClauseKeepsContactNormal：提示词里那几条口径：名字不算证据、
// 引导到自己 bot 的联系方式算正常、案底看 ad_known。
func TestBioLinksClauseKeepsContactNormal(t *testing.T) {
	for _, want := range []string{
		"名字本身不构成证据", "有问题联系我的管家", "ad_known", "Chat killer",
	} {
		if want == "Chat killer" {
			want = "Child killer"
		}
		if !strings.Contains(bioLinksClause, want) {
			t.Errorf("bioLinksClause 应保留口径 %q：\n%s", want, bioLinksClause)
		}
	}
}
