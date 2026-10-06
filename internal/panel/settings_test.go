package panel

import (
	"slices"
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

// TestSpecsInSectionsOrder：所有设置项都要出现在分组里且只出现一次，
// 主题顺序固定、同类选项相邻；按作用域过滤后顺序不变。
func TestSpecsInSectionsOrder(t *testing.T) {
	groups := specsInSections(nil)
	seen := map[string]int{}
	total := 0
	var names []string
	for _, g := range groups {
		names = append(names, g.Name)
		for _, sp := range g.Specs {
			seen[sp.key]++
			total++
		}
	}
	if total != len(settingSpecs) {
		t.Errorf("分组共 %d 项，settingSpecs 共 %d 项", total, len(settingSpecs))
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s 出现了 %d 次", k, n)
		}
	}
	want := []string{"处置与分档", "判定与模型", "进群与验证", "通知与展示", "护栏与成本", "学习与名单"}
	if !slices.Equal(names, want) {
		t.Errorf("分组顺序 = %v，期望 %v", names, want)
	}

	// 处置相关的选项必须排在一起（用户要求「同类、关联的在一起」）。
	idx := func(k string) int {
		for i, sp := range groups[0].Specs {
			if sp.key == k {
				return i
			}
		}
		return -1
	}
	a, b2 := idx("antiad_mute_minutes"), idx("antiad_ban")
	c, d := idx("antiad_short_mute"), idx("antiad_bool_verdict")
	if a < 0 || b2 < 0 || c < 0 || d < 0 || !(a < b2 && b2 < c && c < d) {
		t.Errorf("禁言时长/改为封禁/短禁言/定档应相邻，得到 %d %d %d %d", a, b2, c, d)
	}

	// 按作用域过滤（bot 页）后不该混进纯全局项，顺序保持主题分组。
	var bot []string
	for _, g := range specsInSections(func(sp settingSpec) bool {
		return sp.group == "antiad" || sp.group == "both"
	}) {
		for _, sp := range g.Specs {
			bot = append(bot, sp.key)
		}
	}
	if slices.Contains(bot, "log_retention_days") {
		t.Error("bot 参数页不该出现纯全局项")
	}
	if !slices.Contains(bot, "antiad_cold") {
		t.Error("both 作用域的项两个页面都要有")
	}
}
