package panel

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestMainMenuWithOnlyMainBot 验证仅有主 bot 时主菜单的文案：仅有主 bot
// 属于设计预期，不提示`名下还没有群组`，避免产生永远消不掉的假告警。
func TestMainMenuWithOnlyMainBot(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)
	fake := b.TG.(*testutil.FakeTG)

	ShowMainMenu(b, 777, 0, 777)

	text := fake.LastCall("sendMessage")["text"].(string)
	if !strings.Contains(text, "工作 bot") {
		t.Errorf("只有主 bot 时应提示接入工作 bot:\n%s", text)
	}
	if strings.Contains(text, "名下还没有群组") {
		t.Errorf("主 bot 没有群是设计如此，不该提示没有群组:\n%s", text)
	}
	if !strings.Contains(text, "主 bot 1 · 工作 bot 0") {
		t.Errorf("计数行应区分主 bot 与工作 bot:\n%s", text)
	}
}

// TestMainMenuWantsChatForWorkerBot 验证有工作 bot 但未绑定群组时
// 提示`工作 bot 名下还没有群组`。
func TestMainMenuWantsChatForWorkerBot(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	markMainBot(t, b)
	testutil.RegisterTestBot(t, b.Shared, testToken2, 43, 777)
	fake := b.TG.(*testutil.FakeTG)

	ShowMainMenu(b, 777, 0, 777)

	text := fake.LastCall("sendMessage")["text"].(string)
	if !strings.Contains(text, "工作 bot 名下还没有群组") {
		t.Errorf("有工作 bot 但没挂群时应提示没有群组:\n%s", text)
	}
}
