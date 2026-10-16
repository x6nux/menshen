package core_test

import (
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// TestLookupChatRefRendersNameAndLink：群引用以群名为准；公开群把群名包成
// t.me 链接，私有群只有群名，群配置查不到才退回数字 id；群名做 HTML 转义。
func TestLookupChatRefRendersNameAndLink(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insert := func(chatID int64, title, username string) {
		t.Helper()
		if _, err := b.Store.Write.Exec(`INSERT INTO bot_chats
			(bot_id,chat_id,title,username,enabled,dryrun,group_alert,created_at)
			VALUES (?,?,?,?,1,0,0,0)`, b.BotID(), chatID, title, username); err != nil {
			t.Fatal(err)
		}
	}
	insert(-100, "公开群<A>", "pubgroup")
	insert(-200, "私有群", "")
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()

	pub := core.LookupChatRef(snap, -100)
	if got := pub.Display(); got != "公开群<A>" {
		t.Errorf("Display = %q，应为群名", got)
	}
	if got := pub.URL(); got != "https://t.me/pubgroup" {
		t.Errorf("URL = %q，应为公开群链接", got)
	}
	if got := pub.HTML(); got != `<a href="https://t.me/pubgroup">公开群&lt;A&gt;</a>` {
		t.Errorf("HTML = %q，公开群应为转义后的链接群名", got)
	}

	priv := core.LookupChatRef(snap, -200)
	if priv.URL() != "" || priv.HTML() != "私有群" {
		t.Errorf("私有群应只有群名，得到 url=%q html=%q", priv.URL(), priv.HTML())
	}

	unknown := core.LookupChatRef(snap, -999)
	if unknown.Display() != "-999" || unknown.HTML() != "-999" {
		t.Errorf("未知群应退回 chat_id，得到 %q / %q", unknown.Display(), unknown.HTML())
	}
}
