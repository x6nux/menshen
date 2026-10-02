package antiad

import (
	"testing"

	"menshen/internal/testutil"
)

// TestEmptyChatTitleRefreshesOnMessage：先加配置、后把 bot 拉进群的群标题
// 是空的；收到消息说明 bot 现在能看到它了，要自动补回标题（按群限频）。
func TestEmptyChatTitleRefreshesOnMessage(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	// 模拟「先加配置、后入群」：标题空着。
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET title='' WHERE bot_id=? AND chat_id=?`,
		b.BotID(), int64(-100)); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	fake.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			if id, _ := payload["chat_id"].(float64); id == -100 {
				return `{"ok":true,"result":{"id":-100,"type":"supergroup",` +
					`"title":"补回来的群名"}}`, true
			}
			return `{"ok":true,"result":{"id":777,"type":"private",` +
				`"first_name":"某人"}}`, true
		}
		return "", false
	}
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 1, "普通消息"))
	waitIdle(t, b)

	var title string
	if err := b.Store.Read.QueryRow(`SELECT title FROM bot_chats
		WHERE bot_id=? AND chat_id=?`, b.BotID(), int64(-100)).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "补回来的群名" {
		t.Errorf("收到消息后应把群名补回库，得到 %q", title)
	}
	if conf, ok := b.Cache.Snap().ChatConf(b.BotID(), -100); !ok || conf.Title != "补回来的群名" {
		t.Errorf("缓存里的群名也要刷新，得到 %+v", conf)
	}
}
