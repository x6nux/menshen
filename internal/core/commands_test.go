package core_test

import (
	"testing"

	"menshen/internal/testutil"
)

// TestMainBotSkipsGroupCommands 确认主 bot 不注册群命令菜单（/check /ban /white），
// 并清掉升级前注册过的那一份：它不入群，这份菜单只会短暂出现在被拉进的
// 群里，成为一组点不动的命令。
func TestMainBotSkipsGroupCommands(t *testing.T) {
	b, fake, _ := testutil.NewTestMainBotDispatch(t, 777, 777, nil)
	b.RegisterCommands()

	for _, c := range fake.Calls("setMyCommands") {
		if scope, _ := c["scope"].(map[string]any); scope["type"] == "all_group_chats" {
			t.Error("主 bot 不该注册群命令菜单")
		}
	}
	if fake.CountCalls("deleteMyCommands") == 0 {
		t.Error("主 bot 应清掉历史注册的群命令菜单")
	}

	// 对照组：工作 bot 照旧注册群命令菜单。
	w, wfake, _ := testutil.NewTestBotDispatch(t, 777, 777, nil)
	w.RegisterCommands()
	found := false
	for _, c := range wfake.Calls("setMyCommands") {
		if scope, _ := c["scope"].(map[string]any); scope["type"] == "all_group_chats" {
			found = true
		}
	}
	if !found {
		t.Error("工作 bot 仍应注册群命令菜单")
	}
}
