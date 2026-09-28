package panel

import (
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func privMsg(uid int64, text string) *tg.Message {
	return &tg.Message{MessageID: 1, Date: 1700000000, Text: text,
		From: &tg.TGUser{ID: uid}, Chat: &tg.Chat{ID: uid, Type: "private"}}
}

func privCallback(uid int64, data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{ID: "cb", Data: data, From: &tg.TGUser{ID: uid},
		Message: &tg.Message{MessageID: 5, Chat: &tg.Chat{ID: uid, Type: "private"}}}
}

func containsSpec(specs []settingSpec, key string) bool {
	for _, sp := range specs {
		if sp.key == key {
			return true
		}
	}
	return false
}

// TestColdJudgeIsGlobalWithPerBotOverride：进群冷判定的默认值在全局设置里
// 配一次，所有 bot 跟随；单个 bot 可覆盖，填 "-" 撤销覆盖、真正回到全局
// （以后全局再改，它也继续跟着变）。
func TestColdJudgeIsGlobalWithPerBotOverride(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	botID := b.BotID()

	if !containsSpec(specsInGroup(""), "antiad_cold") {
		t.Error("全局设置页应包含进群冷判定")
	}
	if !containsSpec(specsInGroup("antiad"), "antiad_cold") {
		t.Error("bot 参数页应包含进群冷判定")
	}

	// 全局改成 1：走面板回调 + 文本输入，顺带验证 "both" 没被权限校验挡下。
	HandleAdminCallback(b, privCallback(777, "a:st:e:antiad_cold"))
	p, ok := b.TakePending(777)
	if !ok || p.Op != "st_edit" {
		t.Fatalf("全局编辑应进入待输入，得到 %+v ok=%v", p, ok)
	}
	HandlePendingInput(b, privMsg(777, "1"), p)
	if got := b.Cache.Snap().Setting("antiad_cold"); got != "1" {
		t.Fatalf("全局值应为 1，得到 %q", got)
	}
	if got := b.Cache.Snap().BotSettingInt(botID, "antiad_cold", 0); got != 1 {
		t.Fatalf("没有覆盖时 bot 应跟随全局，得到 %d", got)
	}

	// 单 bot 覆盖为 0
	HandleAdminCallback(b, privCallback(777, "a:st:b:"+itoa(botID)+":antiad_cold"))
	p, ok = b.TakePending(777)
	if !ok {
		t.Fatal("bot 编辑应进入待输入")
	}
	HandlePendingInput(b, privMsg(777, "0"), p)
	if got := b.Cache.Snap().BotSettingInt(botID, "antiad_cold", 0); got != 0 {
		t.Fatalf("覆盖后应为 0，得到 %d", got)
	}

	// 填 "-" 撤销覆盖：覆盖行会被删掉，回到跟随全局。
	HandleAdminCallback(b, privCallback(777, "a:st:b:"+itoa(botID)+":antiad_cold"))
	p, ok = b.TakePending(777)
	if !ok {
		t.Fatal("bot 编辑应进入待输入")
	}
	HandlePendingInput(b, privMsg(777, "-"), p)
	snap := b.Cache.Snap()
	if _, overridden := snap.BotSettings[botID]["antiad_cold"]; overridden {
		t.Error("填 - 后不该留下覆盖行")
	}
	if got := snap.BotSettingInt(botID, "antiad_cold", 0); got != 1 {
		t.Fatalf("撤销覆盖后应回到全局值 1，得到 %d", got)
	}
}
