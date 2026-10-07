package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestUndoVerdictNotReasserted：误判解禁后 TG 会推一条 restricted → 能发言
// 的 chat_member 更新。解禁若不走 LiftMute（落主动解除标记、清进群限制、
// 标记同群其余禁言流水），外部解除复查会把它当成被外部抹掉，当场再禁回去，
// 导致误判已点但用户仍无法发言。
func TestUndoVerdictNotReasserted(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	const uid = 555
	seed := func(action string) AdLogRow {
		res, err := b.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,
			text,verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
			VALUES (-100,?,7,'广告','ad',0.95,'systemone','scam',?,'',?,?)`,
			uid, action, time.Now().Unix(), b.BotID())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		r, _ := LoadAdLog(b.Store, id)
		return r
	}
	r := seed("deleted_muted")
	seed("deleted_muted") // 同一个人在本群的另一条广告
	if _, err := b.Store.Write.Exec(`INSERT INTO join_mutes
		(chat_id,user_id,bot_id,reason,notice_msg,attempts,created_at)
		VALUES (-100,?,?,'简介',0,0,?)`, uid, b.BotID(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	if _, ok, desc := UndoVerdict(b, r, 1); !ok {
		t.Fatalf("误判解禁失败：%s", desc)
	}
	fake.Reset()
	f, tr := false, true
	conf := testutil.ChatConfOf(t, b, -100)
	reassertMute(b, conf, &tg.ChatMemberUpdated{
		Chat:          &tg.Chat{ID: -100},
		OldChatMember: &tg.ChatMemberInfo{Status: "restricted", IsMember: true, CanSendMessages: &f},
		NewChatMember: &tg.ChatMemberInfo{Status: "member", User: &tg.TGUser{ID: uid}, CanSendMessages: &tr},
	})
	if fake.CountCalls("restrictChatMember") != 0 {
		t.Error("误判刚解开的禁言被外部解除复查重新施加了")
	}
	if _, _, active := activeMute(b, -100, uid); active {
		t.Error("误判后本群不该再有我们名下的生效限制")
	}
}
