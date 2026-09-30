package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// memberInfo 造一个成员状态；canSend 为 nil 表示不带该字段。
func memberInfo(uid int64, status string, canSend *bool) *tg.ChatMemberInfo {
	return &tg.ChatMemberInfo{User: &tg.TGUser{ID: uid}, Status: status,
		CanSendMessages: canSend, IsMember: true}
}

func boolPtr(v bool) *bool { return &v }

// TestReassertMuteAfterExternalUnmute：入群验证机器人（nmbot 那类）验证通过
// 后会把权限全量开回来，那一下会盖掉我们给的进群限制 —— 库里记录还在，
// 人却已经能发言。这里盯 chat_member 更新，按剩余时长重新施加。
func TestReassertMuteAfterExternalUnmute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	u := &tg.TGUser{ID: 5937663431, FirstName: "高价回收网赌账号"}

	// 先按冷判定的路径限制他（无限期禁言 + join_mutes 记录）。
	applyJoinMute(b, conf, u, adVerdict{IsAd: true, Confidence: 0.98,
		Decider: "systemone", Reason: "进群资料写着收博彩账号"}, "4收博彩平台")
	if n := fake.CountCalls("restrictChatMember"); n != 1 {
		t.Fatalf("应有 1 次限制，得到 %d", n)
	}

	// 外部把权限开回来（验证机器人干的）：受限 → 能发言。
	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat:          &tg.Chat{ID: -100, Type: "supergroup"},
		OldChatMember: memberInfo(u.ID, "restricted", boolPtr(false)),
		NewChatMember: memberInfo(u.ID, "restricted", boolPtr(true)),
	})

	mutes := fake.Calls("restrictChatMember")
	if len(mutes) != 2 {
		t.Fatalf("被外部解除后应重新施加禁言，得到 %d 次", len(mutes))
	}
	perms, _ := mutes[1]["permissions"].(map[string]any)
	if perms == nil || perms["can_send_messages"] != false {
		t.Errorf("重新施加的应是禁言权限集，得到 %v", mutes[1])
	}
	if _, ok := mutes[1]["until_date"]; ok {
		t.Error("进群限制是无限期的，不该带 until_date")
	}

	// 我们自己主动解除的：不反弹。
	if ok, desc := LiftMute(b, -100, u.ID); !ok {
		t.Fatalf("主动解除失败：%s", desc)
	}
	before := fake.CountCalls("restrictChatMember")
	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat:          &tg.Chat{ID: -100, Type: "supergroup"},
		OldChatMember: memberInfo(u.ID, "restricted", boolPtr(false)),
		NewChatMember: memberInfo(u.ID, "restricted", boolPtr(true)),
	})
	if n := fake.CountCalls("restrictChatMember"); n != before {
		t.Errorf("自己主动解除后不该重新施加，多发了 %d 次", n-before)
	}
}

// TestReassertMuteSkipsOtherCases：进群跃迁、限时禁言到期、白名单、
// 以及根本没有我们记录的人，都不该被这条规则碰。
func TestReassertMuteSkipsOtherCases(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)

	unrestrict := func(uid int64) {
		HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
			Chat:          &tg.Chat{ID: -100, Type: "supergroup"},
			OldChatMember: memberInfo(uid, "restricted", boolPtr(false)),
			NewChatMember: memberInfo(uid, "member", nil),
		})
	}
	// 1) 真进群（old 不在群里）：交给冷判定，不在这里动手。
	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat:          &tg.Chat{ID: -100, Type: "supergroup"},
		OldChatMember: &tg.ChatMemberInfo{Status: "left", User: &tg.TGUser{ID: 7001}},
		NewChatMember: &tg.ChatMemberInfo{Status: "member", IsMember: true,
			User: &tg.TGUser{ID: 7001}},
	})
	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("真进群不该在复查里禁言，得到 %d 次", n)
	}

	// 2) 限时禁言已过期：不再施加。
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,7002,1,'加微信','ad',0.9,'llm','scam','deleted_muted','过期禁言',?,?)`,
		time.Now().Unix()-25*3600, b.BotID()); err != nil {
		t.Fatal(err)
	}
	unrestrict(7002)
	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("限时禁言已过期不该再施加，得到 %d 次", n)
	}

	// 3) 白名单放行的人：不施加。
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_whitelist
		(bot_id,chat_id,user_id,expires_at,source,by_uid,created_at)
		VALUES (?,?,?,0,'adw',1,0)`, b.BotID(), -100, 7003); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	applyJoinMute(b, conf, &tg.TGUser{ID: 7003, FirstName: "白名单"}, adVerdict{IsAd: true,
		Confidence: 0.9, Decider: "systemone", Reason: "资料"}, "资料")
	before := fake.CountCalls("restrictChatMember")
	unrestrict(7003)
	if n := fake.CountCalls("restrictChatMember"); n != before {
		t.Errorf("白名单放行的人不该重新施加，多发了 %d 次", n-before)
	}
}

// TestReassertMuteThrottled：与别的 bot 打起来时（它解除、我们施加、它再
// 解除）不能无限循环：同一个人每小时最多重新施加几次。
func TestReassertMuteThrottled(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	u := &tg.TGUser{ID: 7004, FirstName: "循环测试"}
	applyJoinMute(b, conf, u, adVerdict{IsAd: true, Confidence: 0.9,
		Decider: "systemone", Reason: "资料"}, "资料")

	unrestrict := func() {
		HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
			Chat:          &tg.Chat{ID: -100, Type: "supergroup"},
			OldChatMember: memberInfo(u.ID, "restricted", boolPtr(false)),
			NewChatMember: memberInfo(u.ID, "restricted", boolPtr(true)),
		})
	}
	base := fake.CountCalls("restrictChatMember")
	for i := 0; i < reassertLimit+2; i++ {
		justLifted.Delete(reassertKey(-100, u.ID))
		unrestrict()
	}
	got := fake.CountCalls("restrictChatMember") - base
	if got != reassertLimit {
		t.Errorf("每小时最多重新施加 %d 次，实际 %d 次", reassertLimit, got)
	}
}
