package core_test

import (
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// memberUpdate 造一条 bot 自身成员状态变化的更新（my_chat_member）。
func memberUpdate(chatID int64, chatType, status string, isMember bool, at int64) *tg.ChatMemberUpdated {
	return &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: chatID, Type: chatType, Title: "测试群"},
		Date: at,
		NewChatMember: &tg.ChatMemberInfo{
			User:     &tg.TGUser{ID: testutil.TestBotID},
			Status:   status, IsMember: isMember,
		},
	}
}

func chatSeenRows(t *testing.T, b *core.Bot, chatID int64) (int, int64) {
	t.Helper()
	var n int
	var at int64
	err := b.Store.Read.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(first_seen),0) FROM bot_chat_seen
		WHERE bot_id=? AND chat_id=?`, b.BotID(), chatID).Scan(&n, &at)
	if err != nil {
		t.Fatal(err)
	}
	return n, at
}

func TestNoteChatPresenceTracksJoin(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	joined := time.Now().Add(-time.Hour).Unix()

	// 进群：记下首次出现的时刻。
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true, joined))
	if n, at := chatSeenRows(t, b, -100999); n != 1 || at != joined {
		t.Fatalf("进群应记 1 行 first_seen=%d，得到 n=%d at=%d", joined, n, at)
	}

	// 改权限也会推 my_chat_member：不许覆盖首次时刻，否则时限被一次次顺延。
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "administrator", true,
		time.Now().Unix()))
	if n, at := chatSeenRows(t, b, -100999); n != 1 || at != joined {
		t.Fatalf("改权限不该更新首次时刻，得到 n=%d at=%d", n, at)
	}

	// 离群：删行，退了再进按新时刻记。
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "left", false, joined+1))
	if n, _ := chatSeenRows(t, b, -100999); n != 0 {
		t.Fatalf("离群应删掉痕迹，还剩 %d 行", n)
	}

	// restricted 且 is_member=false 是被踢走后留下的受限记录，不算在群。
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "restricted", false, joined+2))
	if n, _ := chatSeenRows(t, b, -100999); n != 0 {
		t.Fatalf("restricted 且已不在群不该留痕迹，还剩 %d 行", n)
	}
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "restricted", true, joined+3))
	if n, _ := chatSeenRows(t, b, -100999); n != 1 {
		t.Fatalf("restricted 但仍在群应留痕迹，得到 %d 行", n)
	}

	// 私聊不存在占用 webhook 的问题，不记。
	b.NoteChatPresence(memberUpdate(777, "private", "member", true, joined+4))
	if n, _ := chatSeenRows(t, b, 777); n != 0 {
		t.Fatalf("私聊不该留痕迹，得到 %d 行", n)
	}
}

func TestNoteChatPresenceMainBotKeepsNoRows(t *testing.T) {
	b, _, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
		time.Now().Unix()))
	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM bot_chat_seen`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("主 bot 当场退群，不该留痕迹，得到 %d 行", n)
	}
}

func TestSweepUnconfiguredChatsLeaves(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 777, 777)
	// 加群已 2 小时，远超默认 60 分钟。
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
		time.Now().Add(-2*time.Hour).Unix()))

	b.SweepUnconfiguredChats(time.Now())

	last := fake.LastCall("leaveChat")
	if last == nil || last["chat_id"] != float64(-100999) {
		t.Fatalf("超时未配置应退群，leaveChat 调用 = %v", last)
	}
	if n, _ := chatSeenRows(t, b, -100999); n != 0 {
		t.Fatalf("退群后应删掉痕迹，还剩 %d 行", n)
	}
	// 归属人要收到解释：bot 自己退群没有说明就像故障。
	if p := fake.LastCall("sendMessage"); p == nil || p["chat_id"] != float64(777) {
		t.Errorf("退群后应私聊归属人说明，得到 %v", p)
	}
}

func TestSweepUnconfiguredChatsWaitsForDeadline(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 777, 777)
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
		time.Now().Add(-10*time.Minute).Unix()))

	b.SweepUnconfiguredChats(time.Now())

	if n := fake.CountCalls("leaveChat"); n != 0 {
		t.Fatalf("未到时限不该退群，leaveChat 调了 %d 次", n)
	}
	if n, _ := chatSeenRows(t, b, -100999); n != 1 {
		t.Fatalf("未到时限痕迹应保留，得到 %d 行", n)
	}
}

func TestSweepUnconfiguredChatsKeepsConfigured(t *testing.T) {
	b, fake, sh := testutil.NewTestBotOwned(t, 777, 777)
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
		time.Now().Add(-2*time.Hour).Unix()))
	// 挂到名下（新群默认演练、这里未启用也无所谓）：配置过就不该退。
	if err := sh.AddChat(b.BotID(), -100999, "测试群"); err != nil {
		t.Fatal(err)
	}

	b.SweepUnconfiguredChats(time.Now())

	if n := fake.CountCalls("leaveChat"); n != 0 {
		t.Fatalf("已配置的群不该退，leaveChat 调了 %d 次", n)
	}
	if n, _ := chatSeenRows(t, b, -100999); n != 0 {
		t.Fatalf("已配置的群痕迹应清掉，还剩 %d 行", n)
	}
}

func TestSweepUnconfiguredChatsDisabled(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 777, 777)
	if err := b.PutSetting(core.SettingAutoLeaveMinutes, "0"); err != nil {
		t.Fatal(err)
	}
	b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
		time.Now().Add(-2*time.Hour).Unix()))

	b.SweepUnconfiguredChats(time.Now())

	if n := fake.CountCalls("leaveChat"); n != 0 {
		t.Fatalf("设置 0 = 关闭，不该退群，leaveChat 调了 %d 次", n)
	}
	if n, _ := chatSeenRows(t, b, -100999); n != 1 {
		t.Fatalf("关闭时痕迹应保留（等配置或离群事件清理），得到 %d 行", n)
	}
}

func TestSweepUnconfiguredChatsFailureHandling(t *testing.T) {
	t.Run("TG 拒绝则下轮重试", func(t *testing.T) {
		b, fake, _ := testutil.NewTestBotOwned(t, 777, 777)
		fake.Err["leaveChat"] = &tg.APIError{Method: "leaveChat", Code: 500,
			Body: `{"ok":false,"description":"internal"}`, Desc: "internal"}
		b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
			time.Now().Add(-2*time.Hour).Unix()))

		b.SweepUnconfiguredChats(time.Now())

		if n, _ := chatSeenRows(t, b, -100999); n != 1 {
			t.Fatalf("退群失败应保留痕迹等下轮重试，得到 %d 行", n)
		}
		if n := fake.CountCalls("sendMessage"); n != 0 {
			t.Errorf("退群失败不该给归属人发已退群的通知，发了 %d 条", n)
		}
	})
	t.Run("查无此群视为已结案", func(t *testing.T) {
		b, fake, _ := testutil.NewTestBotOwned(t, 777, 777)
		fake.Err["leaveChat"] = testutil.TGNotFound("leaveChat", "Bad Request: chat not found")
		b.NoteChatPresence(memberUpdate(-100999, "supergroup", "member", true,
			time.Now().Add(-2*time.Hour).Unix()))

		b.SweepUnconfiguredChats(time.Now())

		if n, _ := chatSeenRows(t, b, -100999); n != 0 {
			t.Fatalf("群里本来就不在了，痕迹该删掉，还剩 %d 行", n)
		}
	})
}
