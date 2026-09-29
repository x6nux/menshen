package antiad

import (
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// sameOwnerPair 建同一 owner 名下、同在 chatID 的两个 bot，各自一个假传输层。
func sameOwnerPair(t *testing.T, chatID int64) (a, b *core.Bot, fa, fb *testutil.FakeTG) {
	t.Helper()
	reg, a := testutil.NewTestRegistry(t, dispatch)
	b, fb = testutil.AddRegistryBot(t, reg, a.Shared, 4343, a.Owner())
	testutil.EnableAntiad(t, a, chatID)
	testutil.EnableAntiad(t, b, chatID)
	return a, b, a.TG.(*testutil.FakeTG), fb
}

// TestSameChatBotsHandleSenderOnce：同群两个 bot 都收到同一个人的每条消息，
// 但只有一个 bot 处理他——不会两边同时删、同时禁言，画像也只计一次。
// 两条消息故意以相反的顺序投递：谁先收到都不该改变归属。
func TestSameChatBotsHandleSenderOnce(t *testing.T) {
	a, b, fa, fb := sameOwnerPair(t, -100)
	fakeAI(t, a, nil)

	const uid = 5001
	HandleGroupMessage(a, testutil.GroupMsg(-100, uid, 1, "加微信领福利，日赚千元"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, uid, 1, "加微信领福利，日赚千元"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, uid, 2, "私聊我拉你进内部群"))
	HandleGroupMessage(a, testutil.GroupMsg(-100, uid, 2, "私聊我拉你进内部群"))
	waitIdle(t, a)
	waitIdle(t, b)

	na, nb := fa.CountCalls("deleteMessage"), fb.CountCalls("deleteMessage")
	if na+nb == 0 {
		t.Fatal("广告一条都没删")
	}
	if na > 0 && nb > 0 {
		t.Errorf("同一个人被两个 bot 同时处置（A 删 %d 次，B 删 %d 次）", na, nb)
	}
	if n := countRows(t, a, `SELECT msg_count FROM group_members
		WHERE chat_id=-100 AND user_id=?`, uid); n != 2 {
		t.Errorf("msg_count = %d，两条消息应当只计 2 次", n)
	}
}

// TestGbanOncePerChat：同群挂着几个 bot 时联合封禁只封一次；
// 头一个 bot 封不动（没有封禁权限）时换下一个，而不是放掉这个群。
func TestGbanOncePerChat(t *testing.T) {
	a, b, fa, fb := sameOwnerPair(t, -100)
	// 两个 bot 在这个群都选「封禁出群」：本测试覆盖的是封禁路径与「换下一个 bot」。
	testutil.SetChatPunish(t, a, -100, 1)
	testutil.SetChatPunish(t, b, -100, 1)
	fa.Resp["banChatMember"] = `{"ok":false,"description":"not enough rights"}`

	EnforceGban(a.Shared, 888, "测试")

	if n := fb.CountCalls("banChatMember"); n != 1 {
		t.Errorf("能封的 bot 发了 %d 次 banChatMember，期望 1 次", n)
	}
	if n := fa.CountCalls("banChatMember"); n > 1 {
		t.Errorf("封不动的 bot 重复发了 %d 次", n)
	}

	fa.Resp["banChatMember"] = `{"ok":true,"result":true}`
	before := fa.CountCalls("banChatMember") + fb.CountCalls("banChatMember")
	EnforceGban(a.Shared, 889, "测试")
	if n := fa.CountCalls("banChatMember") + fb.CountCalls("banChatMember") - before; n != 1 {
		t.Errorf("同一个群被封了 %d 次，期望 1 次", n)
	}
}
