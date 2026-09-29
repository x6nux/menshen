package antiad

import (
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestRoleOf 守三档权限的边界。
func TestRoleOf(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 1)
	if err := sh.AddAdmin(200, "次管", 1); err != nil {
		t.Fatalf("addAdmin: %v", err)
	}

	cases := []struct {
		uid  int64
		want core.Role
	}{
		{1, core.RoleMain},
		{200, core.RoleSub},
		{999, core.RoleNone},
	}
	for _, c := range cases {
		if got := sh.RoleOf(c.uid); got != c.want {
			t.Errorf("roleOf(%d) = %v, 期望 %v", c.uid, got, c.want)
		}
	}
}

// TestAddAdminRejectsMain 确认主管理员进不了 admins 表。
//
// 主管权限扎在配置文件里，表里再有一条只会让面板显示一行「删了也不生效」
// 的记录，删过一次的人下次就不敢信这个面板了。
func TestAddAdminRejectsMain(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 1)
	if err := sh.AddAdmin(1, "", 1); err == nil {
		t.Error("把主管理员加进次管名单应当被拒绝")
	}
	if _, ok := sh.Cache.Snap().Admins[1]; ok {
		t.Error("主管理员不该出现在 admins 表里")
	}
}

// TestCanManageBotIsolation 是多租户隔离的核心断言。
//
// callback_data 是客户端发上来的，任何次管都能把别人 bot 的 id 拼进去
// 再点一下。这道门一旦恒真，整个多租户就只是界面上的错觉。
func TestCanManageBotIsolation(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 100) // 主管 1，bot 归 100
	if err := sh.AddAdmin(200, "另一个次管", 1); err != nil {
		t.Fatalf("addAdmin: %v", err)
	}

	cases := []struct {
		uid  int64
		who  string
		want bool
	}{
		{1, "主管理员", true},
		{100, "bot 归属人", true},
		{200, "别人名下的次管", false},
		{999, "路人", false},
	}
	for _, c := range cases {
		if got := sh.CanManageBot(c.uid, testutil.TestBotID); got != c.want {
			t.Errorf("canManageBot(%s) = %v, 期望 %v", c.who, got, c.want)
		}
	}

	// 不存在的 bot 对谁都不该放行（主管除外——他本来就能管全部）
	if sh.CanManageBot(100, 999999) {
		t.Error("对不存在的 bot 不该放行")
	}
}

// TestCanManageChatFollowsBot 确认群不是独立的权限主体：
// 能不能动这个群，完全由能不能动这个 bot 决定。
func TestCanManageChatFollowsBot(t *testing.T) {
	b, _, sh := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	if err := sh.AddAdmin(200, "", 1); err != nil {
		t.Fatalf("addAdmin: %v", err)
	}

	if !sh.CanManageChat(100, testutil.TestBotID, -100) {
		t.Error("归属人应当能管自己 bot 的群")
	}
	if sh.CanManageChat(200, testutil.TestBotID, -100) {
		t.Error("别人名下的次管不该能管这个群")
	}
	// 群不在这个 bot 名下时，哪怕是归属人也没得管
	if sh.CanManageChat(100, testutil.TestBotID, -999) {
		t.Error("不在 bot 名下的群不该放行")
	}
}

// TestBotSettingIsolated 确认一个 bot 的阈值改动不会漏到别的 bot 上。
//
// 次管能自己调阈值的前提就是这份隔离。三级回退（bot → 全局 → 默认）
// 任何一级串了，一个人的校准就会改掉所有人的判定。
func TestBotSettingIsolated(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 100)
	const other int64 = 4343
	testutil.RegisterTestBot(t, sh, "555555555:CCotherfaketoken_ForUnitTests5555", other, 200)

	const key = "antiad_act_hard"
	def := sh.Cache.Snap().BotSettingInt(testutil.TestBotID, key, 90)

	if err := sh.PutBotSetting(testutil.TestBotID, key, "70"); err != nil {
		t.Fatalf("putBotSetting: %v", err)
	}
	snap := sh.Cache.Snap()
	if got := snap.BotSettingInt(testutil.TestBotID, key, 90); got != 70 {
		t.Errorf("本 bot 的阈值 = %d, 期望 70", got)
	}
	if got := snap.BotSettingInt(other, key, 90); got != def {
		t.Errorf("别的 bot 的阈值被改成了 %d, 期望保持 %d", got, def)
	}

	// 改全局默认时，已有覆盖的那个 bot 不受影响
	if err := sh.PutSetting(key, "50"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	snap = sh.Cache.Snap()
	if got := snap.BotSettingInt(testutil.TestBotID, key, 90); got != 70 {
		t.Errorf("有覆盖的 bot 被全局默认盖掉了，得到 %d", got)
	}
	if got := snap.BotSettingInt(other, key, 90); got != 50 {
		t.Errorf("没覆盖的 bot 应当跟随全局默认，得到 %d", got)
	}
}

// TestChatsAreScopedToBot 确认群列表按 bot 分。
// 次管在面板上看到别人的群，等于多租户只是界面上的错觉。
func TestChatsAreScopedToBot(t *testing.T) {
	b, _, sh := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	const other int64 = 4343
	testutil.RegisterTestBot(t, sh, "555555555:CCotherfaketoken_ForUnitTests5555", other, 200)

	if len(sh.Cache.Snap().ChatsOf(other)) != 0 {
		t.Error("别人的 bot 名下不该有群")
	}
	if _, ok := sh.Cache.Snap().ChatConf(other, -100); ok {
		t.Error("同一个 chat_id 不该在别人的 bot 下也生效")
	}
	if _, ok := chatActive(b, -100); !ok {
		t.Error("自己的群应当生效")
	}
}

// TestBotsOwnedBy 确认面板列表按归属过滤，主管看全部。
func TestBotsOwnedBy(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 100)
	testutil.RegisterTestBot(t, sh, "555555555:CCotherfaketoken_ForUnitTests5555", 4343, 200)
	snap := sh.Cache.Snap()

	if n := len(snap.BotsOwnedBy(100, false)); n != 1 {
		t.Errorf("次管 100 看到 %d 个 bot, 期望 1 个", n)
	}
	if n := len(snap.BotsOwnedBy(200, false)); n != 1 {
		t.Errorf("次管 200 看到 %d 个 bot, 期望 1 个", n)
	}
	if n := len(snap.BotsOwnedBy(1, true)); n != 2 {
		t.Errorf("主管看到 %d 个 bot, 期望全部 2 个", n)
	}
	if n := len(snap.BotsOwnedBy(999, false)); n != 0 {
		t.Errorf("路人看到 %d 个 bot, 期望 0 个", n)
	}
}

// TestGbanGuardBansOnJoin 确认联合封禁拦在门口。
// 这是整个功能最值钱的部分：等他发完广告再删，广告已经被人看过了。
func TestGbanGuardBansOnJoin(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)

	if err := GbanAdd(b.Shared, 777, "测试", -100, testutil.TestBotID); err != nil {
		t.Fatalf("gbanAdd: %v", err)
	}

	// 开关没开时不得动手：全平台封禁是要主管理员明确点头的。
	if gbanGuard(b, testutil.ChatConfOf(t, b, -100), &tg.TGUser{ID: 777}) {
		t.Error("开关关闭时不该拦截")
	}
	if n := fake.CountCalls("banChatMember"); n != 0 {
		t.Errorf("开关关闭时发了 %d 次 banChatMember", n)
	}

	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	// 该群选「封禁出群」：联合封禁按每群自己的配置执行。
	testutil.SetChatPunish(t, b, -100, 1)
	if !gbanGuard(b, testutil.ChatConfOf(t, b, -100), &tg.TGUser{ID: 777}) {
		t.Error("名单内的人进群应当被拦下")
	}
	p := fake.LastCall("banChatMember")
	if p == nil {
		t.Fatal("没有发出 banChatMember")
	}
	if int64(p["user_id"].(float64)) != 777 {
		t.Errorf("封错了人: %v", p["user_id"])
	}

	// 不在名单里的人照常放行
	if gbanGuard(b, testutil.ChatConfOf(t, b, -100), &tg.TGUser{ID: 888}) {
		t.Error("名单外的人不该被拦")
	}
}

// TestLiftGbanPassesOnlyIfBanned 锁住整个联合封禁里最容易写反的一处。
//
// unbanChatMember 不带 only_if_banned 时，Telegram 的语义是「先踢出群
// 再解除封禁」。于是对一个**没被封**的人调用它，这个本意为「解封」的
// 动作反而把无辜的人踢了出去 —— 而全平台解封天然会扫到一大批没被封的群。
func TestLiftGbanPassesOnlyIfBanned(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)

	if err := GbanAdd(b.Shared, 777, "测试", -100, testutil.TestBotID); err != nil {
		t.Fatalf("gbanAdd: %v", err)
	}
	LiftGban(b.Shared, 777)

	if _, still := b.Cache.Snap().Gban[777]; still {
		t.Error("解除后仍在名单里")
	}
	p := fake.LastCall("unbanChatMember")
	if p == nil {
		t.Fatal("没有发出 unbanChatMember")
	}
	if p["only_if_banned"] != true {
		t.Errorf("unbanChatMember 缺 only_if_banned（得到 %v）—— "+
			"不带它等于把没被封的人从群里踢出去", p["only_if_banned"])
	}
}

// TestGbanCrossesOwners 确认联合封禁是**全平台**的。
//
// 这是它和普通封禁的唯一区别，也是唯一值得开这个开关的理由：
// 一个广告号在 A 的群里被逮到，B 的群不该等它再来一遍。
func TestGbanCrossesOwners(t *testing.T) {
	reg, b := testutil.NewTestRegistry(t, dispatch)
	testutil.EnableAntiad(t, b, -100)

	// 第二个 bot 归另一个人，在另一个群
	const otherBot int64 = 4343
	testutil.RegisterTestBot(t, b.Shared, "555555555:CCotherfaketoken_ForUnitTests5555",
		otherBot, 200)
	reg.LoadAll()
	b2, ok := reg.LookupID(otherBot)
	if !ok {
		t.Fatal("第二个 bot 没能拉起来")
	}
	testutil.EnableAntiad(t, b2, -200)
	testutil.SetChatPunish(t, b, -100, 1)
	testutil.SetChatPunish(t, b2, -200, 1)

	var chats []int64
	eachActiveChat(b.Shared, nil, func(_ []*core.Bot, chatID int64) { chats = append(chats, chatID) })
	if len(chats) != 2 {
		t.Fatalf("全平台扫到 %d 个群（%v），期望跨 owner 的 2 个", len(chats), chats)
	}

	EnforceGban(b.Shared, 888, "测试")
	// 两个 bot 共用同一个 testutil.FakeTG，所以两次封禁都记在它上面
	if n := b.TG.(*testutil.FakeTG).CountCalls("banChatMember"); n != 2 {
		t.Errorf("发了 %d 次 banChatMember, 期望两个群各一次", n)
	}
}

// TestEachActiveChatSkipsDisabled 确认联合封禁只扫「已启用」的群。
// 停用的群不该因为一次全平台操作被悄悄动手。
func TestEachActiveChatSkipsDisabled(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	testutil.EnableAntiad(t, b, -100)
	testutil.EnableAntiad(t, b, -200)
	testutil.SetChatEnabled(t, b, -200, false)

	var got []int64
	eachActiveChat(b.Shared, nil, func(_ []*core.Bot, chatID int64) { got = append(got, chatID) })
	if len(got) != 1 || got[0] != -100 {
		t.Errorf("遍历到的群 = %v, 期望只有 [-100]", got)
	}
}
