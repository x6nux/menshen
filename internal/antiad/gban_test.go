package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
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
	if !GbanMessageGuard(b, -100, m.From, m.MessageID) {
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

	// /white 本群解封（白名单）优先于名单：命中放行。
	if err := setGroupWhitelist(b, -100, 999, true); err != nil {
		t.Fatal(err)
	}
	if GbanMessageGuard(b, -100, m.From, 6) {
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
