package antiad

import (
	"sync"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestJoinBackfillAppliesOnlyUnknown：补全结果只填 joined_at=0 的行，已有的
// 值不动（实时机制记的比脚本新）。
func TestJoinBackfillAppliesOnlyUnknown(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	now := time.Now().Unix()
	seed := func(uid, joined int64) {
		if _, err := b.Store.Write.Exec(`INSERT INTO group_members
			(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
			VALUES (-100,?,?,?,1,?,0)`, uid, joined, now-100, now); err != nil {
			t.Fatal(err)
		}
	}
	seed(10, 0)        // 未知：应被填上
	seed(11, now-3600) // 已知：不动
	seed(12, 0)        // 未知但这次没拿到：保持 0

	rows := []backfillRow{
		{ChatID: -100, UserID: 10, Joined: now - 86400*30},
		{ChatID: -100, UserID: 11, Joined: now - 86400*99},
		{ChatID: -999, UserID: 12, Joined: now - 86400}, // 别的群，忽略
	}
	if filled := applyJoinBackfill(b, -100, rows); filled != 1 {
		t.Errorf("应只填一行，得到 %d", filled)
	}
	var got int64
	b.Store.Read.QueryRow(`SELECT joined_at FROM group_members WHERE chat_id=-100 AND user_id=10`).Scan(&got)
	if got != now-86400*30 {
		t.Errorf("未知的应被填成 30 天前，得到 %d", got)
	}
	b.Store.Read.QueryRow(`SELECT joined_at FROM group_members WHERE chat_id=-100 AND user_id=11`).Scan(&got)
	if got != now-3600 {
		t.Errorf("已知的不该被覆盖，得到 %d", got)
	}
	b.Store.Read.QueryRow(`SELECT joined_at FROM group_members WHERE chat_id=-100 AND user_id=12`).Scan(&got)
	if got != 0 {
		t.Errorf("没拿到的应保持 0，得到 %d", got)
	}
}

// TestJoinBackfillTriggeredOnAdminGrant：bot 从非管理员变成管理员时触发一次；
// 同在管理员状态的更新（改权限、置顶）不再触发；冷却期内不重复触发。
func TestJoinBackfillTriggeredOnAdminGrant(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 2040, "hash"
	// 替换脚本执行器：只记录被调用，不真跑 python。
	calls := make(chan int64, 8)
	oldRunner := backfillRunner
	backfillRunner = func(_ *core.Bot, chatID int64, _ string) ([]backfillRow, error) {
		calls <- chatID
		return nil, nil
	}
	defer func() { backfillRunner = oldRunner }()
	joinBackfillDone = sync.Map{}
	// 群要处于启用状态，getChat 也要能返回（拿公开用户名）。
	testutil.EnableAntiad(t, b, -100)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getChat"] = `{"ok":true,"result":{"id":-100,"type":"supergroup","username":"testgroup"}}`

	mk := func(old, now string) *tg.ChatMemberUpdated {
		return &tg.ChatMemberUpdated{
			Chat:          &tg.Chat{ID: -100, Type: "supergroup", Title: "测试群", Username: "testgroup"},
			OldChatMember: &tg.ChatMemberInfo{Status: old},
			NewChatMember: &tg.ChatMemberInfo{Status: now},
		}
	}
	// member → administrator：触发。
	HandleMyChatMemberUpdate(b, mk("member", "administrator"))
	select {
	case c := <-calls:
		if c != -100 {
			t.Errorf("触发时应带上群 id，得到 %d", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("拿到管理员权限应触发历史成员补全")
	}
	// administrator → administrator（改权限）：不触发。
	HandleMyChatMemberUpdate(b, mk("administrator", "administrator"))
	select {
	case <-calls:
		t.Error("同在管理员状态不该重复触发")
	case <-time.After(300 * time.Millisecond):
	}
}
