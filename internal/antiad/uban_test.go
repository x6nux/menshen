package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
)

const (
	memberMuted  = `{"ok":true,"result":{"status":"restricted","is_member":true,"can_send_messages":false}}`
	memberKicked = `{"ok":true,"result":{"status":"kicked"}}`
	memberFree   = `{"ok":true,"result":{"status":"member"}}`
)

// TestUbanEverywhere：私聊 /uban 是「全解」—— 撤名单、逐群按真实状态解封 /
// 解禁言、清处罚记录与进群限制，但**不加白名单**（之后的发言照常判定）。
// 没被限制的群不碰（「权限全开」对没被禁言的人等于提权到群默认之上）。
func TestUbanEverywhere(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, dispatch)
	sh := a.Shared
	b, fb := testutil.AddRegistryBot(t, reg, sh, 4343, a.Owner())
	c, fc := testutil.AddRegistryBot(t, reg, sh, 4444, a.Owner())
	d, fd := testutil.AddRegistryBot(t, reg, sh, 4545, a.Owner())
	fa := a.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, a, -100)
	testutil.EnableAntiad(t, b, -200)
	testutil.EnableAntiad(t, c, -300)
	testutil.EnableAntiad(t, d, -400) // 干净的群：没被限制、也没有任何记录
	fa.Resp["getChatMember"] = memberMuted
	fb.Resp["getChatMember"] = memberKicked
	fc.Resp["getChatMember"] = memberFree
	fd.Resp["getChatMember"] = memberFree

	const uid = 555
	if err := GbanAdd(sh, uid, "测试", -100, a.BotID()); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,
		text,verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,?,7,'广告','ad',0.95,'systemone','scam','deleted_muted','',?,?)`,
		uid, time.Now().Unix(), a.BotID()); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO join_mutes
		(chat_id,user_id,bot_id,reason,notice_msg,attempts,created_at)
		VALUES (-200,?,?,'简介',0,0,?)`, uid, b.BotID(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	// -300 里 TG 侧已经能发言（被验证机器人放开了），库里却还挂着未解除的
	// 处罚流水 —— 这正是「二次封禁」的来源：复查任务会按它把人再禁回去。
	if _, err := sh.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,
		text,verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-300,?,8,'广告','ad',0.95,'systemone','scam','deleted_muted','',?,?)`,
		uid, time.Now().Unix(), c.BotID()); err != nil {
		t.Fatal(err)
	}

	fa.Reset()
	fb.Reset()
	fc.Reset()
	fd.Reset()

	r := UbanEverywhere(sh, 777, uid)

	if _, in := sh.Cache.Snap().Gban[uid]; in {
		t.Error("应撤掉全局联合封禁")
	}
	if len(r.Unmuted) != 1 || len(r.Unbanned) != 1 || r.Checked != 4 {
		t.Errorf("应解禁 1 群、解封 1 群、核对 4 群，得到 %+v", r)
	}
	unmuted := false
	for _, p := range fa.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok && perms["can_send_messages"] == true {
			unmuted = true
		}
	}
	if !unmuted {
		t.Error("被禁言的群应发「权限全开」")
	}
	if p := fb.LastCall("unbanChatMember"); p == nil || p["only_if_banned"] != true {
		t.Errorf("被封的群应解封且带 only_if_banned：%v", p)
	}
	if fc.CountCalls("restrictChatMember")+fd.CountCalls("restrictChatMember") != 0 {
		t.Error("没被限制、也没有我们记录的群不该发 restrictChatMember（会把人提权到群默认之上）")
	}

	var n int
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log WHERE user_id=? AND lifted_at=0
		AND action IN ('muted','deleted_muted','banned','deleted_banned','gban_muted','gban_banned')`,
		uid).Scan(&n)
	if n != 0 {
		t.Errorf("处罚流水应全部标记解除，还剩 %d 条", n)
	}
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM join_mutes WHERE user_id=?`, uid).Scan(&n)
	if n != 0 {
		t.Errorf("进群限制应清掉，还剩 %d 条", n)
	}
	if len(sh.Cache.Snap().Whitelist) != 0 {
		t.Error("全解不加白名单：之后的发言要照常判定")
	}
}

// TestUbanSubAdminScope：次级管理员只解自己名下 bot 的群，别人的群不碰。
func TestUbanSubAdminScope(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, dispatch)
	sh := a.Shared
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	mine, fm := testutil.AddRegistryBot(t, reg, sh, 4343, 888)
	fa := a.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, a, -100) // 777 的群
	testutil.EnableAntiad(t, mine, -200)
	fa.Resp["getChatMember"] = memberMuted
	fm.Resp["getChatMember"] = memberMuted
	fa.Reset()
	fm.Reset()

	r := UbanEverywhere(sh, 888, 555)

	if fa.CountCalls("restrictChatMember")+fa.CountCalls("getChatMember") != 0 {
		t.Error("次级管理员不该碰别人名下的群")
	}
	if r.Checked != 1 || len(r.Unmuted) != 1 || unmuteCount(fm) != 1 {
		t.Errorf("应只核对并解开自己名下的那 1 个群，得到 %+v", r)
	}
}

func unmuteCount(f *testutil.FakeTG) int {
	n := 0
	for _, p := range f.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok && perms["can_send_messages"] == true {
			n++
		}
	}
	return n
}
