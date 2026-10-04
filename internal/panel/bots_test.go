package panel

import (
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// markMainBot 把 registry 里那个测试 bot 标成主 bot。
func markMainBot(t *testing.T, b *core.Bot) {
	t.Helper()
	testutil.RegisterMainTestBot(t, b.Shared, testutil.TestToken, testutil.TestBotID, 777)
}

// TestMainBotChatCannotBeAdded 确认服务端兜底：即使有人手工拼出
// 「给主 bot 添加群」的入口，也不落库 —— 它加进去也会被 ensureMainBot
// 在下次启动时清掉，当场拒绝更诚实。
func TestMainBotChatCannotBeAdded(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)

	if err := b.AddChat(b.BotID(), -100777, ""); err == nil {
		t.Error("给主 bot 添加生效群应被拒绝")
	}
	if _, ok := b.Cache.Snap().ChatConf(b.BotID(), -100777); ok {
		t.Error("被拒绝后不该落库")
	}
}

// TestMainBotDetailIsReadOnly 确认主 bot 详情页只剩说明与返回：
// 没有添加群组、没有阈值/豁免/拦截记录这些工作 bot 才有的入口。
func TestMainBotDetailIsReadOnly(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)
	fake := b.TG.(*testutil.FakeTG)

	showBotDetail(b, 777, 0, 777, b.BotID())

	last := fake.LastCall("sendMessage")
	if last == nil {
		t.Fatal("详情页应发一条消息")
	}
	text, _ := last["text"].(string)
	if !strings.Contains(text, "主 bot") {
		t.Errorf("详情页应说明这是主 bot，得到:\n%s", text)
	}
	for _, bad := range []string{"添加群组", "阈值与参数", "拦截记录", "停用", "移除"} {
		if strings.Contains(text, bad) {
			t.Errorf("主 bot 详情页不该出现 %q:\n%s", bad, text)
		}
	}
}

// TestMainBotCallbackActionsRejected 守的是 callback_data 这条不可信输入：
// 按钮收起来了，但任何人都能手工拼一个「停用主 bot」的回调。
func TestMainBotCallbackActionsRejected(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)
	fake := b.TG.(*testutil.FakeTG)

	HandleAdminCallback(b, &tg.CallbackQuery{
		ID: "cb1", Data: "a:mb:" + itoa(b.BotID()) + ":on",
		From: &tg.TGUser{ID: 777},
		Message: &tg.Message{MessageID: 5,
			Chat: &tg.Chat{ID: 777, Type: "private"}},
	})

	if rec := b.Cache.Snap().Bots[b.BotID()]; rec == nil || !rec.Enabled {
		t.Errorf("主 bot 的启停状态不该被回调改动: %+v", rec)
	}
	ans := fake.LastCall("answerCallbackQuery")
	if ans == nil {
		t.Fatal("应回一个 callback 响应")
	}
	if text, _ := ans["text"].(string); !strings.Contains(text, "主 bot") {
		t.Errorf("回调响应应说明主 bot 不能这样操作，得到 %q", text)
	}
}

// TestMainBotListBadge 确认列表页把主 bot 与工作 bot 区分开：
// 标上「主 bot」，且不显示会让人误会的「生效群 0/0」。
func TestMainBotListBadge(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)
	fake := b.TG.(*testutil.FakeTG)

	showMyBots(b, 777, 0, 777)

	last := fake.LastCall("sendMessage")
	if last == nil {
		t.Fatal("列表页应发一条消息")
	}
	text, _ := last["text"].(string)
	if !strings.Contains(text, "主 bot") {
		t.Errorf("列表里应标出主 bot:\n%s", text)
	}
	if strings.Contains(text, "生效群") {
		t.Errorf("主 bot 不该显示生效群计数:\n%s", text)
	}
}
