package panel

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestMainMenuWithOnlyMainBot 守的是主 bot 隔离之后的主菜单文案：
// 「只有主 bot」是设计如此，不该提示「机器人名下还没有群组」——
// 那是一条永远消不掉的假告警，反而把真正的下一步藏了起来。
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

// TestMainMenuWantsChatForWorkerBot 对照组：有工作 bot 但没挂群时，
// 才提示「工作 bot 名下还没有群组」。
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
