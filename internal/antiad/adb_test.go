package antiad

import (
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// adbMsg 造一条回复某人后发 /banad 的命令消息。
func adbMsg(chatID, from int64, target *tg.Message) *tg.Message {
	m := testutil.GroupMsg(chatID, from, 900, "/banad")
	m.ReplyToMessage = target
	return m
}

// lastLog 取最后一条判定流水的几个关键字段。
func lastLog(t *testing.T, b *core.Bot) (verdict, decider, action string, uid int64, n int) {
	t.Helper()
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log`).Scan(&n)
	if n == 0 {
		return "", "", "", 0, 0
	}
	err := b.Store.Read.QueryRow(`SELECT verdict,decider,action,user_id
		FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&verdict, &decider, &action, &uid)
	if err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	return verdict, decider, action, uid, n
}

// TestAdbRequiresAdmin 是 /banad 的权限门。
//
// 它绕过 AI 直接删消息 + 禁言，对普通成员开放等于把删消息的权力交给全群：
// 任何人回复一句就能让别人闭嘴。testutil.FakeTG 的 getChatMember 默认不返回
// administrator，所以这里的 42 就是个普通成员。
func TestAdbRequiresAdmin(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1) // 主管 1，bot 也归 1
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "广告内容")
	HandleBanAdCommand(b, conf, adbMsg(-100, 42, target), "")

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("普通成员用 /ban 禁言了别人（%d 次）", n)
	}
	if n := fake.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("普通成员用 /ban 删了消息（%d 次）", n)
	}
	if n := fake.CountCalls("sendMessage"); n != 0 {
		t.Error("非授权者应当被静默忽略 —— 回一句「你没权限」" +
			"等于告诉刷屏的人这条命令存在、值得去试")
	}
	if _, _, _, _, n := lastLog(t, b); n != 0 {
		t.Errorf("未授权的标记不该落流水，当前 %d 条", n)
	}
}

// TestAdbByOwnerActs 确认授权者的标记会真正处置，且落的是人工流水。
func TestAdbByOwnerActs(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "看煮页 有码就来捡签")
	HandleBanAdCommand(b, conf, adbMsg(-100, 1, target), "") // 1 是主管兼归属人

	if fake.CountCalls("restrictChatMember") != 1 {
		t.Errorf("应当禁言一次，实际 %d 次", fake.CountCalls("restrictChatMember"))
	}
	// 命令本身与被标记的消息都要删：命令留在群里等于把用法教给所有人
	if n := fake.CountCalls("deleteMessage"); n != 2 {
		t.Errorf("应当删两条（命令 + 被标记的消息），实际 %d 条", n)
	}

	verdict, decider, action, uid, n := lastLog(t, b)
	if n != 1 {
		t.Fatalf("应当落 1 条流水，实际 %d 条", n)
	}
	if verdict != "ad" || decider != "manual" {
		t.Errorf("流水 = verdict %q decider %q, 期望 ad/manual", verdict, decider)
	}
	if uid != 777 {
		t.Errorf("流水记到了 %d 头上，期望 777（被回复的那个人）", uid)
	}
	if action == "none" {
		t.Errorf("处置动作 = %q, 不该是未处置", action)
	}
}

// TestAdbRespectsDryrun 确认演练群只报告不处置。
func TestAdbRespectsDryrun(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "广告内容")
	HandleBanAdCommand(b, conf, adbMsg(-100, 1, target), "")

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("演练群里不该禁言，实际 %d 次", n)
	}
	if _, _, _, _, n := lastLog(t, b); n != 1 {
		t.Errorf("演练仍应落流水，实际 %d 条", n)
	}
}

// TestAdbFeedsGban 确认人工标记会进联合封禁名单。
// 人工标记是置信度最高的一档判定，它不该比 AI 的结论更弱。
func TestAdbFeedsGban(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatalf("putSetting: %v", err)
	}
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 888, 10, "广告内容")
	HandleBanAdCommand(b, conf, adbMsg(-100, 777, target), "") // 777 是 newTestRegistry 的主管

	if _, ok := b.Cache.Snap().Gban[888]; !ok {
		t.Error("人工标记的广告号应当进联合封禁名单")
	}
}

// TestAdbNeedsReply 确认没回复消息时给的是用法提示而不是默默失败。
func TestAdbNeedsReply(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	HandleBanAdCommand(b, conf, testutil.GroupMsg(-100, 1, 900, "/banad"), "")

	if fake.CountCalls("sendMessage") != 1 {
		t.Errorf("应当回一条用法提示，实际发了 %d 条", fake.CountCalls("sendMessage"))
	}
	if _, _, _, _, n := lastLog(t, b); n != 0 {
		t.Errorf("没有目标时不该落流水，实际 %d 条", n)
	}
}

// TestAdbIgnoresSelf 确认不会把 bot 自己的告警标成广告，并回一条用法提示。
// 告警里带着原文，回复它发 /banad 是常见误操作；静默失败会被当成
// 命令未生效，因此必须回一条正确用法提示。
func TestAdbIgnoresSelf(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, testutil.TestBotID, 10, "🛡 反广告告警")
	HandleBanAdCommand(b, conf, adbMsg(-100, 1, target), "")

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("不该对 bot 自己动手，实际 %d 次", n)
	}
	if _, _, _, _, n := lastLog(t, b); n != 0 {
		t.Errorf("不该落流水，实际 %d 条", n)
	}
	c := fake.LastCall("sendMessage")
	if c == nil || !strings.Contains(c["text"].(string), "/banad") {
		t.Errorf("应回一条带正确用法的提示，实际 %v", c)
	}
}

// TestBanByUserID：/ban <user_id> 按该用户最新一条留底处置（删除 + 按本群
// 处罚方式禁言/封禁），落一条人工标记流水；没有留底时只处罚、不删消息，
// 仍落流水（留底不存在时 deleteMessage 必然失败）。
func TestBanByUserID(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	// 两条留底：最新的那条是媒体消息（没有文字），删除也该指向它。
	recordMessage(b, -100, 10, 555, "旧的一条", 1700000000, "")
	recordMessage(b, -100, 11, 555, "", 1700000001, "")

	HandleBanAdCommand(b, conf, testutil.GroupMsg(-100, 1, 900, "/ban 555"), "555")
	waitIdle(t, b)

	// 删的应是最新那条（id=11）。
	if last := fake.LastCall("deleteMessage"); last == nil ||
		last["message_id"] != float64(11) {
		t.Errorf("应删除最新一条留底 #11，得到 %v", last)
	}
	var kind, action, reason string
	if err := b.Store.Read.QueryRow(`SELECT ad_kind,action,reason FROM antiad_log
		WHERE user_id=555 ORDER BY id DESC LIMIT 1`).Scan(&kind, &action, &reason); err != nil {
		t.Fatal(err)
	}
	if kind != "manual" || !strings.Contains(reason, "人工标记") {
		t.Errorf("应落人工标记流水：kind=%q reason=%q", kind, reason)
	}
	if action != "deleted_muted" && action != "deleted_banned" {
		t.Errorf("应按最高档处置，得到 %q", action)
	}
	// 群内应有回执。
	found := false
	for _, p := range fake.Calls("sendMessage") {
		if s, _ := p["text"].(string); strings.Contains(s, "人工标记") {
			found = true
		}
	}
	if !found {
		t.Error("群里应发出人工标记回执")
	}

	// 没有留底的人：只罚人、不删消息。
	b2, fake2 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	conf2 := testutil.ChatConfOf(t, b2, -100)
	HandleBanAdCommand(b2, conf2, testutil.GroupMsg(-100, 1, 901, "/ban 666"), "666")
	waitIdle(t, b2)
	if n := fake2.CountCalls("deleteMessage"); n != 1 { // 只删命令本身那一条
		t.Errorf("没有留底时不该删消息（命令自身除外），得到 %d 次", n)
	}
	var action2 string
	if err := b2.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE user_id=666 ORDER BY id DESC LIMIT 1`).Scan(&action2); err != nil {
		t.Fatal(err)
	}
	if action2 != "muted" && action2 != "banned" {
		t.Errorf("没有留底时应只禁言/封禁，得到 %q", action2)
	}
}

// TestAdbDropsProfileOK：人工标记广告后，该用户已有的资料放行要一并作废，
// 否则这份放行会继续挡后续的资料判定，使人工结论被过期的自动放行架空；
// 演练群不处置，也不该撤销真实放行。
func TestAdbDropsProfileOK(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	p := senderProfile{UserID: 555, FirstName: "广告位"}
	if GrantProfileOK(b, p, 48, "先前的放行") == 0 {
		t.Fatal("预置放行失败")
	}
	HandleBanAdCommand(b, conf, adbMsg(-100, 1, testutil.GroupMsg(-100, 555, 10, "广告")), "")
	if n := countRows(t, b, `SELECT COUNT(*) FROM profile_ok`); n != 0 {
		t.Errorf("人工标记广告后应撤销资料放行，剩 %d 行", n)
	}

	// 演练群不处置：真实的放行也不该被撤。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b2, -100, true)
	conf2 := testutil.ChatConfOf(t, b2, -100)
	p2 := senderProfile{UserID: 555, FirstName: "广告位"}
	if GrantProfileOK(b2, p2, 48, "先前的放行") == 0 {
		t.Fatal("预置放行失败")
	}
	HandleBanAdCommand(b2, conf2, adbMsg(-100, 1, testutil.GroupMsg(-100, 555, 10, "广告")), "")
	if n := countRows(t, b2, `SELECT COUNT(*) FROM profile_ok`); n != 1 {
		t.Errorf("演练群不该撤真实放行，剩 %d 行", n)
	}
}

// TestBanCommandPlainBan：/ban 是纯封禁 —— 封人、留一条可撤销的流水，
// 但不删发言、不进样本池、不进联合封禁（那些是 /banad 的语义）。
func TestBanCommandPlainBan(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "捣乱")
	HandleBanCommand(b, conf, adbMsg(-100, 1, target), "")

	if n := fake.CountCalls("banChatMember"); n == 0 {
		t.Error("/ban 应封禁出群")
	}
	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Error("/ban 是封禁，不该退化成禁言")
	}
	// 只删命令消息，不删被回复的那条。
	for _, c := range fake.Calls("deleteMessage") {
		if c["message_id"] == float64(10) {
			t.Error("/ban 不该删被回复的发言")
		}
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM gban`); n != 0 {
		t.Errorf("/ban 不该进全局联合封禁，当前 %d 条", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM gban_own_bans`); n != 0 {
		t.Errorf("/ban 不该进专属联合封禁，当前 %d 条", n)
	}
	_, decider, action, uid, n := lastLog(t, b)
	if n == 0 || action != "banned" || decider != "manual-ban" || uid != 777 {
		t.Errorf("流水应记纯封禁：decider=%s action=%s uid=%d", decider, action, uid)
	}
}

// TestUngbanScopeByRole：/ungban 的范围按身份分 —— 群管理员只解本群所属的
// 专属联合封禁；主/次管理员连全局组一起解。
func TestUngbanScopeByRole(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	sh := b.Shared

	// 全局组与归属人（1）的专属组各一条；专属组圈定本群。
	if err := GbanAdd(sh, 555, "跨群刷广告", 0, b.BotID()); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnSetChat(sh, 1, -100, true); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnAddBan(sh, 1, 555, "简介推广", 0); err != nil {
		t.Fatal(err)
	}

	ungbanMsg := func(from int64) *tg.Message {
		m := testutil.GroupMsg(-100, from, 901, "/ungban")
		m.ReplyToMessage = testutil.GroupMsg(-100, 555, 10, "hi")
		return m
	}

	// 群管理员 200（FakeTG 认成 administrator）：只解专属组。
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`
	HandleUngbanCommand(b, conf, ungbanMsg(200), "")
	if _, ok := sh.Cache.Snap().Gban[555]; !ok {
		t.Error("群管理员不该动全局联合封禁")
	}
	if _, ok := sh.Cache.Snap().GbanOwnBans[1][555]; ok {
		t.Error("群管理员应解除本群所属的专属联合封禁")
	}

	// 主管理员 1：连全局组一起解。
	if err := GbanOwnAddBan(sh, 1, 555, "简介推广", 0); err != nil {
		t.Fatal(err)
	}
	HandleUngbanCommand(b, conf, ungbanMsg(1), "")
	if _, ok := sh.Cache.Snap().Gban[555]; ok {
		t.Error("主管理员应解除全局联合封禁")
	}
	if _, ok := sh.Cache.Snap().GbanOwnBans[1][555]; ok {
		t.Error("主管理员也应解除专属联合封禁")
	}
}
