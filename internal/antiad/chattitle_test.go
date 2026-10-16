package antiad

import (
	"testing"

	"menshen/internal/testutil"
)

// TestEmptyChatTitleRefreshesOnMessage：先加配置、后把 bot 拉进群的群标题
// 与公开 @ 名都是空的；收到消息说明 bot 现在能看到它了，要自动补回。
// 消息本身带着标题与 @ 名时直接写回（不花 getChat）；消息里没有标题时
// 才退回按群限频查一次 getChat。
func TestEmptyChatTitleRefreshesOnMessage(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	emptyMeta := func(t *testing.T) {
		t.Helper()
		if _, err := b.Store.Write.Exec(
			`UPDATE bot_chats SET title='', username='' WHERE bot_id=? AND chat_id=?`,
			b.BotID(), int64(-100)); err != nil {
			t.Fatal(err)
		}
		if err := b.Cache.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	emptyMeta(t)

	fake.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			if id, _ := payload["chat_id"].(float64); id == -100 {
				return `{"ok":true,"result":{"id":-100,"type":"supergroup",` +
					`"title":"getChat 补回的群名","username":"grouphandle"}}`, true
			}
			return `{"ok":true,"result":{"id":777,"type":"private",` +
				`"first_name":"某人"}}`, true
		}
		return "", false
	}
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))

	// 消息里带着标题与 @ 名：直接写回，不走 getChat。
	msg := testutil.GroupMsg(-100, 777, 1, "普通消息")
	msg.Chat.Title = "消息里的群名"
	msg.Chat.Username = "msgmhandle"
	HandleGroupMessage(b, msg)
	waitIdle(t, b)

	conf, ok := b.Cache.Snap().ChatConf(b.BotID(), -100)
	if !ok || conf.Title != "消息里的群名" || conf.Username != "msgmhandle" {
		t.Fatalf("收到消息后应把群名与 @ 名补回缓存，得到 %+v", conf)
	}

	// 消息里没有标题：退回 getChat 补一次。
	emptyMeta(t)
	msg = testutil.GroupMsg(-100, 777, 2, "普通消息")
	msg.Chat.Title = ""
	msg.Chat.Username = ""
	HandleGroupMessage(b, msg)
	waitIdle(t, b)

	conf, ok = b.Cache.Snap().ChatConf(b.BotID(), -100)
	if !ok || conf.Title != "getChat 补回的群名" || conf.Username != "grouphandle" {
		t.Fatalf("消息里没有标题时应退回 getChat，得到 %+v", conf)
	}
}
