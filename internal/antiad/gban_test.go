package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestGbanHitScopes：联合封禁的适用性判定。全局组要求总开关 + bot 加入
// （gban_global）；专属组要求总开关 + 组开着 + 群在圈定清单里。
func TestGbanHitScopes(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)

	if err := GbanAdd(sh, 999, "全局命中", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnAddBan(sh, 777, 998, "专属命中", -100); err != nil {
		t.Fatal(err)
	}

	// 总开关没开：什么都不命中。
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 999); hit {
		t.Error("总开关关闭时全局组不应命中")
	}
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}

	// 全局组：默认 bot 已加入。
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 999); !hit {
		t.Error("全局组应命中")
	}
	// bot 退出全局组后不再命中全局条目。
	if err := b.PutBotSetting(testutil.TestBotID, "gban_global", "0"); err != nil {
		t.Fatal(err)
	}
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 999); hit {
		t.Error("退出全局组后不应命中全局条目")
	}

	// 专属组：群没圈定时不命中；圈定后命中；组关掉后不命中。
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 998); hit {
		t.Error("专属组没圈定该群时不应命中")
	}
	if err := GbanOwnSetChat(sh, 777, -100, true); err != nil {
		t.Fatal(err)
	}
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 998); !hit {
		t.Error("圈定后专属组应命中")
	}
	if err := GbanOwnSetEnabled(sh, 777, false); err != nil {
		t.Fatal(err)
	}
	if _, hit := gbanHit(sh, testutil.TestBotID, -100, 998); hit {
		t.Error("专属组关闭后不应命中")
	}
}

// TestGbanMessageGuard：联合封禁在发言路径生效——命中即禁言（全 false
// 权限集）、删除本条、落一条流水；/white 的本群白名单优先于名单。
func TestGbanMessageGuard(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	fake := b.TG.(*testutil.FakeTG)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := GbanAdd(sh, 999, "惯犯", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}

	m := testutil.GroupMsg(-100, 999, 5, "hello")
	if !GbanMessageGuard(b, testutil.ChatConfOf(t, b, -100), m.From, m.MessageID, false) {
		t.Fatal("名单内的人发言应被拦下")
	}
	p := fake.LastCall("restrictChatMember")
	if p == nil || int64(p["user_id"].(float64)) != 999 {
		t.Fatal("应发出 restrictChatMember")
	}
	perms := p["permissions"].(map[string]any)
	if perms["can_send_messages"] != false {
		t.Error("禁言权限集应为全 false")
	}
	if fake.CountCalls("deleteMessage") != 1 {
		t.Error("应删除本条消息")
	}
	var action string
	if err := sh.Store.Read.QueryRow(
		`SELECT action FROM antiad_log WHERE user_id=999`).Scan(&action); err != nil ||
		action != "gban_muted" {
		t.Errorf("应落 gban_muted 流水（err=%v action=%s）", err, action)
	}

	// /white 本群解封（白名单）优先于名单：命中放行。标记由调用方从
	// 画像行带进来（见 touchMember），这里直接传 true。
	if err := setGroupWhitelist(b, -100, 999, true); err != nil {
		t.Fatal(err)
	}
	if GbanMessageGuard(b, testutil.ChatConfOf(t, b, -100), m.From, 6, true) {
		t.Error("白名单用户不应被联合封禁拦下")
	}
}

// TestMaybeGbanScopes：判定命中写入哪个组由 bot 是否加入全局组决定；
// 专属组账本总是记录（归属人的圈）。
func TestMaybeGbanScopes(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	// bot 退出全局组：命中只进归属人的专属组。
	if err := b.PutBotSetting(testutil.TestBotID, "gban_global", "0"); err != nil {
		t.Fatal(err)
	}
	maybeGban(b, -100, 888, "命中")
	if _, ok := sh.Cache.Snap().Gban[888]; ok {
		t.Error("未加入全局组的 bot 不应把命中写进全局组")
	}
	if _, ok := sh.Cache.Snap().GbanOwnBans[777][888]; !ok {
		t.Error("命中应写进归属人的专属组账本")
	}

	// 加入全局组后两边都记。
	if err := b.PutBotSetting(testutil.TestBotID, "gban_global", "1"); err != nil {
		t.Fatal(err)
	}
	maybeGban(b, -100, 889, "命中")
	if _, ok := sh.Cache.Snap().Gban[889]; !ok {
		t.Error("加入全局组的 bot 应把命中写进全局组")
	}
	if _, ok := sh.Cache.Snap().GbanOwnBans[777][889]; !ok {
		t.Error("加入全局组也应写专属组账本")
	}
}

// TestAdminLiftGban 解除范围按身份分账本：全局组人人可解除，专属组只动
// 自己的；没在名单里时返回空串。
func TestAdminLiftGban(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	if err := GbanAdd(sh, 900, "全局", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnAddBan(sh, 777, 900, "专属", -100); err != nil {
		t.Fatal(err)
	}
	if got := AdminLiftGban(sh, 888, 900); !strings.Contains(got, "全局") ||
		strings.Contains(got, "专属") {
		t.Errorf("他人专属组不应被解除，得到 %q", got)
	}
	if _, ok := sh.Cache.Snap().GbanOwnBans[777][900]; !ok {
		t.Error("他人的专属组条目不应被动")
	}
	if got := AdminLiftGban(sh, 777, 900); !strings.Contains(got, "专属") {
		t.Errorf("本人应能解除自己的专属组条目，得到 %q", got)
	}
	if got := AdminLiftGban(sh, 777, 901); got != "" {
		t.Errorf("不在名单里应返回空串，得到 %q", got)
	}
}

// TestGbanNeedsHardEvidence：联合封禁会把人从所有接入群请出去，必须有一条
// 独立于档位的证据线。按模型结论定档时置信度不参与档位，62% 的误报曾经
// 因此被写进名单（线上真实事件，管理员随即标了误判）。
func TestGbanNeedsHardEvidence(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	fake := b.TG.(*testutil.FakeTG)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()

	low := adVerdict{IsAd: true, Confidence: 0.62, Decider: "systemone+llm"}
	if gbanWorthy(b, snap, low) {
		t.Error("62% 的判定不够格进联合封禁")
	}
	high := adVerdict{IsAd: true, Confidence: 0.95, Decider: "systemone+llm"}
	if !gbanWorthy(b, snap, high) {
		t.Error("95% 的判定应够格")
	}
	// 危害度高的（systemone 判色情/诈骗）即使置信度没到线也算。
	severe := adVerdict{IsAd: true, Confidence: 0.7, Severity: 2.5, Decider: "systemone"}
	if !gbanWorthy(b, snap, severe) {
		t.Error("危害度达阈值时应够格")
	}

	// 端到端：bool 模式下 62% 的新人广告只禁言、不进名单。
	fakeAIWith(t, b, soReply("ad", 0.62, "promo", "message"),
		llmReply(true, 0.62, "promo", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "ping0.cc"))
	waitIdle(t, b)
	if _, in := b.Cache.Snap().Gban[42]; in {
		t.Error("低置信误报不该进联合封禁名单")
	}
	if n := fake.CountCalls("restrictChatMember"); n == 0 {
		t.Error("单群禁言照常执行")
	}
}

// TestGbanFollowsGroupConfig：联合封禁执行服从各群自己的配置——演练群不动手、
// 禁言档只禁言、封禁档才请出群（用户要求「不要直接就封禁」）。
func TestGbanFollowsGroupConfig(t *testing.T) {
	// 用 registry 版环境：扇出执行（EnforceGban）要走「所有 bot 的所有群」。
	a, b, fa, fb := sameOwnerPair(t, -100)
	if err := a.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := GbanAdd(a.Shared, 888, "测试", -100, a.BotID()); err != nil {
		t.Fatal(err)
	}
	user := &tg.TGUser{ID: 888}
	bans := func() int { return fa.CountCalls("banChatMember") + fb.CountCalls("banChatMember") }
	mutes := func() int {
		return fa.CountCalls("restrictChatMember") + fb.CountCalls("restrictChatMember")
	}

	// 1) 禁言档（默认跟随 bot，antiad_ban=0）：只禁言，不踢人。
	if !gbanGuard(a, testutil.ChatConfOf(t, a, -100), user) {
		t.Fatal("名单内的人进群应被拦下")
	}
	if bans() != 0 {
		t.Errorf("禁言档不该请出群，发了 %d 次 banChatMember", bans())
	}
	if mutes() == 0 {
		t.Error("禁言档应发 restrictChatMember")
	}

	// 2) 扇出执行（EnforceGban）同样按配置。
	ban0, mute0 := bans(), mutes()
	EnforceGban(a.Shared, 889, "测试")
	if bans() != ban0 {
		t.Error("扇出在禁言档不该踢人")
	}
	if mutes() == mute0 {
		t.Error("扇出在禁言档应禁言")
	}

	// 3) 演练群：判定照跑，但不处置。同群两个 bot 都切到演练。
	testutil.SetChatDryrun(t, a, -100, true)
	testutil.SetChatDryrun(t, b, -100, true)
	ban1, mute1 := bans(), mutes()
	if gbanGuard(a, testutil.ChatConfOf(t, a, -100), user) {
		t.Error("演练群不该拦人（不处置）")
	}
	EnforceGban(a.Shared, 890, "测试")
	if bans() != ban1 || mutes() != mute1 {
		t.Error("演练群不该动手")
	}

	// 4) 封禁档：请出群。
	testutil.SetChatDryrun(t, a, -100, false)
	testutil.SetChatDryrun(t, b, -100, false)
	testutil.SetChatPunish(t, a, -100, 1)
	testutil.SetChatPunish(t, b, -100, 1)
	if !gbanGuard(a, testutil.ChatConfOf(t, a, -100), user) {
		t.Fatal("封禁档应拦下")
	}
	if bans() == ban1 {
		t.Error("封禁档应发 banChatMember")
	}
}

// TestGbanFanoutLogsMuteAndLiftUnmutes：名单扇出在禁言档的群要留一条
// gban_muted 流水，撤名单时才知道该去哪解禁言 —— unbanChatMember 只解
// 封禁，禁言档当初走的是 restrictChatMember。少了这一步就是「名单撤了，
// 人在群里还是发不了言」。
func TestGbanFanoutLogsMuteAndLiftUnmutes(t *testing.T) {
	a, _, fa, fb := sameOwnerPair(t, -100)
	if err := a.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := GbanAdd(a.Shared, 888, "测试用广告号", -100, a.BotID()); err != nil {
		t.Fatal(err)
	}
	mutes := func() int {
		return fa.CountCalls("restrictChatMember") + fb.CountCalls("restrictChatMember")
	}

	// 扇出：默认禁言档（antiad_ban=0）→ restrictChatMember 且落流水。
	EnforceGban(a.Shared, 888, "测试用广告号")
	if mutes() == 0 {
		t.Fatal("禁言档的扇出应发 restrictChatMember")
	}
	var n int
	if err := a.Shared.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=888 AND action='gban_muted' AND lifted_at=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("扇出应落 gban_muted 流水，否则解除时找不到该解的群")
	}

	// 撤名单：除了 unban，还要补一次「权限全开」把禁言解掉。
	before := mutes()
	LiftGban(a.Shared, 888)
	if mutes() == before {
		t.Error("撤联合封禁应补发一次解除禁言")
	}
	sawUnmute := false
	for _, p := range append(fa.Calls("restrictChatMember"), fb.Calls("restrictChatMember")...) {
		if perms, ok := p["permissions"].(map[string]any); ok && perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("解除禁言要发全开权限（再发全 false 等于又禁言一次）")
	}
	if err := a.Shared.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=888 AND action='gban_muted' AND lifted_at=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("解除后应标记 lifted_at，还剩 %d 条", n)
	}
}

// TestGbanLiftKeepsOtherPenalty：群里还有别的生效处罚（比如这个人自己也
// 发过广告被禁言）时，撤联合封禁不该顺手把那条禁言也解掉。
func TestGbanLiftKeepsOtherPenalty(t *testing.T) {
	a, _, fa, fb := sameOwnerPair(t, -100)
	if err := a.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	if err := GbanAdd(a.Shared, 889, "测试用广告号", -100, a.BotID()); err != nil {
		t.Fatal(err)
	}
	EnforceGban(a.Shared, 889, "测试用广告号")

	// 此人另有一条生效中的禁言（他自己的广告判定）。
	if _, err := a.Shared.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id)
		VALUES (-100,889,7,'加微信','ad',0.99,'llm','scam','deleted_muted','自己的广告',
		 0,0,0,1700000000,?)`, a.BotID()); err != nil {
		t.Fatal(err)
	}
	mutes := func() int {
		return fa.CountCalls("restrictChatMember") + fb.CountCalls("restrictChatMember")
	}
	before := mutes()
	gbanLiftRecorded(a.Shared, 889)
	if mutes() != before {
		t.Error("还有别的生效处罚时不该解禁言")
	}
	var n int
	a.Shared.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log
		WHERE user_id=889 AND action='gban_muted' AND lifted_at=0`).Scan(&n)
	if n == 0 {
		t.Error("没解成的禁言不该标记 lifted_at，否则以后不会再补解")
	}

	// 那条处罚解除后再撤一次，就该解掉了。
	if _, err := a.Shared.Store.Write.Exec(`UPDATE antiad_log SET lifted_at=1
		WHERE user_id=889 AND action='deleted_muted'`); err != nil {
		t.Fatal(err)
	}
	gbanLiftRecorded(a.Shared, 889)
	if mutes() == before {
		t.Error("别的处罚解除后，撤联合封禁应解掉当初的禁言")
	}
}
