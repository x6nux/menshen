package panel

import (
	"errors"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// TestSetSettingRules 钉住 TG 面板与 Mini App 共用的设置规则，尤其是
// 两边曾经各写一份、写出分歧的那几条。
func TestSetSettingRules(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	sh := b.Shared
	const main, sub = 777, 888
	if err := sh.AddAdmin(sub, "", main); err != nil {
		t.Fatal(err)
	}
	opKind := func(err error) string {
		var op *core.OpError
		switch {
		case err == nil:
			return "ok"
		case errors.As(err, &op) && op.Denied:
			return "denied"
		case errors.As(err, &op):
			return "bad"
		}
		return "internal: " + err.Error()
	}

	if err := sh.AddUpstream(core.UpstreamPatch{Name: ptr("up"),
		BaseURL: ptr("https://x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.AddModel("up", "m", [4]float64{}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		uid, bot   int64
		key, val   string
		want, read string // read 非空时检查落库值
	}{
		{sub, 0, "antiad_enabled", "1", "denied", ""},
		{main, 0, "antiad_enabled", "2", "bad", ""},
		{main, 0, "antiad_rule_auto", "1", "ok", "1"},
		{main, 0, "tz_name", "-", "ok", "Asia/Shanghai"},
		{main, 0, "tz_name", "Mars/Base", "bad", ""},
		{main, 0, "antiad_group_footer", "-", "ok", ""},
		{main, 0, "antiad_vision_model", "up/none", "bad", ""},
		{main, 0, "antiad_vision_model", "up/m", "ok", "up/m"},
		{main, 0, "antiad_vision_model", "-", "ok", ""}, // 曾经 Mini App 报「模型不存在」
		{main, 0, "antiad_so_models", "up/m, up/m", "ok", `["up/m"]`},
		{main, 0, "antiad_so_models", "-", "ok", "[]"},
		{main, 0, "antiad_act_hard", "101", "bad", ""},
		{main, 0, "antiad_act_hard", "80", "ok", "80"}, // 主管理员可设 antiad 组的全局默认
		{main, 0, "no_such_key", "1", "bad", ""},
		{sub, 0, "log_retention_days", "3", "denied", ""},
	} {
		got := opKind(setSetting(sh, c.uid, c.bot, c.key, c.val))
		if got != c.want {
			t.Errorf("%d 改 %s=%q：得到 %s，应为 %s", c.uid, c.key, c.val, got, c.want)
			continue
		}
		if c.want == "ok" && (c.read != "" || c.val == "-") {
			if v := sh.Cache.Snap().Setting(c.key); v != c.read {
				t.Errorf("%s 落库为 %q，应为 %q", c.key, v, c.read)
			}
		}
	}

	// 旧单值键随列表一起清掉：留着的话列表清空后读侧会回退到它。
	if err := sh.PutSetting("antiad_llm_model", "up/m"); err != nil {
		t.Fatal(err)
	}
	if err := setSetting(sh, main, 0, "antiad_llm_models", "up/m"); err != nil {
		t.Fatal(err)
	}
	if v := sh.Cache.Snap().Setting("antiad_llm_model"); v != "" {
		t.Errorf("旧单值键应被清空，得到 %q", v)
	}

	// per-bot：归属人可改自己的 bot；全局项不能按 bot 覆盖；空值/「-」撤销覆盖。
	bot := testutil.TestBotID
	if err := setSetting(sh, sub, bot, "antiad_act_hard", "70"); opKind(err) != "denied" {
		t.Errorf("次管改别人的 bot 应被拒，得到 %v", err)
	}
	if err := setSetting(sh, main, bot, "log_retention_days", "3"); opKind(err) != "bad" {
		t.Errorf("全局项按 bot 覆盖应被拒，得到 %v", err)
	}
	if err := setSetting(sh, main, bot, "antiad_act_hard", "70"); err != nil {
		t.Fatal(err)
	}
	if v := sh.Cache.Snap().BotSetting(bot, "antiad_act_hard"); v != "70" {
		t.Errorf("bot 覆盖应为 70，得到 %q", v)
	}
	if err := setSetting(sh, main, bot, "antiad_act_hard", "-"); err != nil {
		t.Fatal(err)
	}
	if v := sh.Cache.Snap().BotSetting(bot, "antiad_act_hard"); v != "80" {
		t.Errorf("撤销覆盖后应跟随全局 80，得到 %q", v)
	}
}
