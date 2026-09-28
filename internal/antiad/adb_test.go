package antiad

import (
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// adbMsg 造一条「回复某人后发 /ban」的命令消息。
func adbMsg(chatID, from int64, target *tg.Message) *tg.Message {
	m := testutil.GroupMsg(chatID, from, 900, "/ban")
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

// TestAdbRequiresAdmin 是 /ban 的权限门。
//
// 它绕过 AI 直接删消息 + 禁言，对普通成员开放等于把删消息的权力交给全群：
// 任何人回复一句就能让别人闭嘴。testutil.FakeTG 的 getChatMember 默认不返回
// administrator，所以这里的 42 就是个普通成员。
func TestAdbRequiresAdmin(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1) // 主管 1，bot 也归 1
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "广告内容")
	HandleAdbCommand(b, conf, adbMsg(-100, 42, target))

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

// TestAdbByOwnerActs 确认授权者的标记真的动手了，且落的是人工流水。
func TestAdbByOwnerActs(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "看煮页 有码就来捡签")
	HandleAdbCommand(b, conf, adbMsg(-100, 1, target)) // 1 是主管兼归属人

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

// TestAdbRespectsDryrun 确认演练群里只报告不动手。
// 演练的全部意义就是「先看看会拦到谁」，动了手就不叫演练了。
func TestAdbRespectsDryrun(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, 777, 10, "广告内容")
	HandleAdbCommand(b, conf, adbMsg(-100, 1, target))

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
	HandleAdbCommand(b, conf, adbMsg(-100, 777, target)) // 777 是 newTestRegistry 的主管

	if _, ok := isGbanned(b.Shared, 888); !ok {
		t.Error("人工标记的广告号应当进联合封禁名单")
	}
}

// TestAdbNeedsReply 确认没回复消息时给的是用法提示而不是默默失败。
func TestAdbNeedsReply(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	HandleAdbCommand(b, conf, testutil.GroupMsg(-100, 1, 900, "/ban"))

	if fake.CountCalls("sendMessage") != 1 {
		t.Errorf("应当回一条用法提示，实际发了 %d 条", fake.CountCalls("sendMessage"))
	}
	if _, _, _, _, n := lastLog(t, b); n != 0 {
		t.Errorf("没有目标时不该落流水，实际 %d 条", n)
	}
}

// TestAdbIgnoresSelf 确认不会把 bot 自己的告警标成广告。
// 告警里带着原文，回复它发 /ban 是很自然的手滑。
func TestAdbIgnoresSelf(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	target := testutil.GroupMsg(-100, testutil.TestBotID, 10, "🛡 反广告告警")
	HandleAdbCommand(b, conf, adbMsg(-100, 1, target))

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("不该对 bot 自己动手，实际 %d 次", n)
	}
	if _, _, _, _, n := lastLog(t, b); n != 0 {
		t.Errorf("不该落流水，实际 %d 条", n)
	}
}
