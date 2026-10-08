package antiad

import (
	"testing"

	"menshen/internal/testutil"
)

// TestSplitPunishMode：从参数尾部取处罚方式，mute/ban 与中文别名都认；
// 没写方式时原样返回参数，由调用方决定默认值。
func TestSplitPunishMode(t *testing.T) {
	cases := []struct{ in, mode, rest string }{
		{"555", "", "555"},
		{"555 mute", "mute", "555"},
		{"555 ban", "ban", "555"},
		{"@someone 禁言", "mute", "@someone"},
		{"@someone 封禁", "ban", "@someone"},
		{"555 MUTE", "mute", "555"},
		{"mute", "", "mute"}, // 只有方式没有目标：交给调用方回用法
		{"", "", ""},
	}
	for _, c := range cases {
		mode, rest := splitPunishMode(c.in)
		if mode != c.mode || rest != c.rest {
			t.Errorf("splitPunishMode(%q) = (%q,%q)，期望 (%q,%q)",
				c.in, mode, rest, c.mode, c.rest)
		}
	}
}

// TestPunishEverywhereScopesAndModes：私聊 /ban 在能管的所有群里逐群执行，
// 两种方式分别走 restrictChatMember / banChatMember，并各落一条可撤销的
// 处罚流水；演练群跳过。主管理员的范围是全部 bot 的群（与 /uban 对称）。
func TestPunishEverywhereScopesAndModes(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, dispatch)
	sh := a.Shared
	b, fb := testutil.AddRegistryBot(t, reg, sh, 4343, a.Owner())
	c, fc := testutil.AddRegistryBot(t, reg, sh, 4444, 999) // 别人名下，但主管理员仍覆盖
	testutil.EnableAntiad(t, a, -100)
	testutil.EnableAntiad(t, b, -200)
	testutil.EnableAntiad(t, c, -300)
	fa := a.TG.(*testutil.FakeTG)
	fa.Reset()
	fb.Reset()
	fc.Reset()

	// 默认档：封禁。
	r := punishEverywhere(sh, 777, 555, "ban", "测试")
	if len(r.Done) != 3 || r.Dryrun != 0 {
		t.Fatalf("主管理员应在 3 个群里封禁，得到 %+v", r)
	}
	if fa.CountCalls("banChatMember") != 1 || fb.CountCalls("banChatMember") != 1 ||
		fc.CountCalls("banChatMember") != 1 {
		t.Errorf("封禁档应各发一次 banChatMember：a=%d b=%d c=%d",
			fa.CountCalls("banChatMember"), fb.CountCalls("banChatMember"),
			fc.CountCalls("banChatMember"))
	}

	// 禁言档。
	fa.Reset()
	fb.Reset()
	fc.Reset()
	r = punishEverywhere(sh, 777, 555, "mute", "测试")
	if len(r.Done) != 3 {
		t.Fatalf("应在 3 个群里禁言，得到 %+v", r)
	}
	if fa.CountCalls("restrictChatMember") != 1 || fb.CountCalls("restrictChatMember") != 1 ||
		fc.CountCalls("restrictChatMember") != 1 {
		t.Errorf("禁言档应各发一次 restrictChatMember：a=%d b=%d c=%d",
			fa.CountCalls("restrictChatMember"), fb.CountCalls("restrictChatMember"),
			fc.CountCalls("restrictChatMember"))
	}

	// 演练群不执行。
	fa.Reset()
	fb.Reset()
	testutil.SetChatDryrun(t, b, -200, true)
	r = punishEverywhere(sh, 777, 555, "ban", "测试")
	if len(r.Done) != 2 || r.Dryrun != 1 {
		t.Fatalf("演练群应跳过，得到 %+v", r)
	}
	if fb.CountCalls("banChatMember") != 0 {
		t.Error("演练群不该发 banChatMember")
	}

	var n int
	if err := sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=555 AND action IN ('banned','muted')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 8 { // 封禁 3 + 禁言 3 + 演练轮 2（演练那个群不落）
		t.Errorf("应落 8 条治安流水，得到 %d", n)
	}
}

// TestPunishEverywhereSubAdminScope：次级管理员只动自己名下 bot 的群。
func TestPunishEverywhereSubAdminScope(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, dispatch)
	sh := a.Shared
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	mine, fm := testutil.AddRegistryBot(t, reg, sh, 4343, 888)
	fa := a.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, a, -100) // 777 的群
	testutil.EnableAntiad(t, mine, -200)
	fa.Reset()
	fm.Reset()

	r := punishEverywhere(sh, 888, 555, "ban", "测试")

	if fa.CountCalls("banChatMember") != 0 {
		t.Error("次管不该碰别人名下的群")
	}
	if len(r.Done) != 1 || fm.CountCalls("banChatMember") != 1 {
		t.Errorf("次管应只处置自己名下的 1 个群，得到 %+v", r)
	}
}

// TestGbanApplyHonorsStoredMode：名单行手动选定的方式覆盖群配置；不给方式
// 的行仍服从群配置。这是 /gban 带 mute|ban 时“选择禁言还是封禁”的落点。
func TestGbanApplyHonorsStoredMode(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}

	// 群配置为封禁；名单行选 mute → 应禁言。
	testutil.SetChatPunish(t, b, -100, 1)
	if err := GbanAddMode(sh, 555, "mute", "手动", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	if rec, ok := sh.Cache.Snap().Gban[555]; !ok || rec.Mode != "mute" {
		t.Fatalf("方式应写进名单行：%+v ok=%v", rec, ok)
	}
	conf := testutil.ChatConfOf(t, b, -100)
	fake.Reset()
	if act, ok, _ := gbanApply(b, conf, 555); !ok || act != "mute" {
		t.Errorf("名单方式应覆盖群配置为禁言，得到 %s ok=%v", act, ok)
	}
	if fake.CountCalls("restrictChatMember") != 1 || fake.CountCalls("banChatMember") != 0 {
		t.Error("选 mute 时应只发 restrictChatMember")
	}

	// 群配置改为禁言；名单行选 ban → 应封禁（同级覆盖反向也要生效）。
	testutil.SetChatPunish(t, b, -100, 0)
	if err := GbanAddMode(sh, 555, "ban", "手动", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	if rec := sh.Cache.Snap().Gban[555]; rec.Mode != "ban" {
		t.Fatalf("重复写入应覆盖方式列，得到 %q", rec.Mode)
	}
	conf = testutil.ChatConfOf(t, b, -100)
	fake.Reset()
	if act, ok, _ := gbanApply(b, conf, 555); !ok || act != "ban" {
		t.Errorf("名单方式应覆盖群配置为封禁，得到 %s ok=%v", act, ok)
	}
	if fake.CountCalls("banChatMember") != 1 {
		t.Error("选 ban 时应只发 banChatMember")
	}

	// 不带方式的行服从群配置（禁言）。
	if err := GbanAdd(sh, 556, "手动", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	fake.Reset()
	if act, _, _ := gbanApply(b, conf, 556); act != "mute" {
		t.Errorf("不带方式的名单行应服从群配置（禁言），得到 %s", act)
	}
}
