package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestUadReleasesUser：/uad 一次把人放回来 —— 解封（only_if_banned）、
// 解禁言、清掉进群限制记录、标记处罚已解除，并给 24 小时白名单缓冲。
func TestUadReleasesUser(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	u := &tg.TGUser{ID: 5001, FirstName: "被限制的人"}
	// 先有两条真实处置：进群资料限制 + 一条消息级禁言流水。
	applyJoinMute(b, conf, u, adVerdict{IsAd: true, Confidence: 0.98,
		Decider: "systemone", Reason: "资料写着收博彩账号"}, "4收博彩平台")
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,5001,7,'加微信','ad',0.9,'llm','scam','deleted_muted','自己的广告',?,?)`,
		time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}
	// 本群所属的专属联合封禁也挂上：/uad 要连带解除 ——
	// 只解群里的限制、不动名单的话，人一发言又会被名单自动禁回去。
	if err := GbanOwnSetChat(b.Shared, b.Owner(), -100, true); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnAddBan(b.Shared, b.Owner(), 5001, "简介推广", 0); err != nil {
		t.Fatal(err)
	}
	before := fake.CountCalls("restrictChatMember")

	// 群管理员发 /uad。
	m := testutil.GroupMsg(-100, 1, 20, "/uad 5001")
	HandleGroupMessage(b, m)
	waitIdle(t, b)

	// 解封带 only_if_banned（不带等于把没被封的人踢出去）。
	un := fake.LastCall("unbanChatMember")
	if un == nil || un["only_if_banned"] != true {
		t.Fatalf("应发 only_if_banned 的解封，得到 %v", un)
	}
	// 解禁言是十项权限全开。
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember")[before:] {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("应发一次权限全开的解禁言")
	}
	// 记录清干净：进群限制行没了、处罚流水标成已解除。
	if n := countRows(t, b, `SELECT COUNT(*) FROM join_mutes WHERE user_id=5001`); n != 0 {
		t.Errorf("进群限制记录该被清掉，剩 %d 条", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log
		WHERE user_id=5001 AND action='deleted_muted' AND lifted_at=0`); n != 0 {
		t.Errorf("处罚流水该标成已解除，剩 %d 条", n)
	}
	// 24 小时白名单缓冲：否则他重新进群时冷判定又按同一份资料限制一次。
	var expires int64
	var source string
	if err := b.Store.Read.QueryRow(`SELECT expires_at,source FROM ad_whitelist
		WHERE bot_id=? AND chat_id=? AND user_id=?`, b.BotID(), -100, 5001).
		Scan(&expires, &source); err != nil {
		t.Fatalf("应写入白名单缓冲: %v", err)
	}
	if source != "uad" {
		t.Errorf("白名单来源应记 uad，得到 %q", source)
	}
	// 专属联合封禁条目一并解除。
	if _, ok := b.Cache.Snap().GbanOwnBans[b.Owner()][5001]; ok {
		t.Error("/uad 应连带解除本群所属的专属联合封禁")
	}
	left := expires - time.Now().Unix()
	if left < int64(23*time.Hour/time.Second) || left > int64(25*time.Hour/time.Second) {
		t.Errorf("缓冲应约 24 小时，得到 %d 秒", left)
	}
	// 群里要有一句回执。
	var notice string
	for _, p := range fake.Calls("sendMessage") {
		if s := stringOf(p["text"]); strings.Contains(s, "已解除") {
			notice = s
		}
	}
	if notice == "" {
		t.Error("群里应发出解封回执")
	}
}

// TestUadPermissionsAndUsage：非群管理员静默忽略（不回复、不动作）；
// 用法不对时回一条用法说明。
func TestUadPermissionsAndUsage(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	// 普通成员（uid 999）：静默。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 999, 21, "/uad 5002"))
	waitIdle(t, b)
	if n := fake.CountCalls("unbanChatMember") + fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("非管理员用 /uad 不该有任何动作，得到 %d 次调用", n)
	}
	for _, p := range fake.Calls("sendMessage") {
		if s := stringOf(p["text"]); strings.Contains(s, "用法") {
			t.Error("非管理员不该收到用法提示（等于告诉他这条命令存在）")
		}
	}

	// 管理员但没给参数：回用法说明。
	fake.Reset()
	HandleGroupMessage(b, testutil.GroupMsg(-100, 1, 22, "/uad"))
	waitIdle(t, b)
	found := false
	for _, p := range fake.Calls("sendMessage") {
		if s := stringOf(p["text"]); strings.Contains(s, "用法") {
			found = true
		}
	}
	if !found {
		t.Error("没给参数时该回一条用法说明")
	}
}

// stringOf 取载荷里的文本字段（测试断言用）。
func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
