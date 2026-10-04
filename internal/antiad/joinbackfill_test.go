package antiad

import (
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

// TestJoinBackfillRefusesWhenBusy：同一时间只允许一个补全任务（同一个 bot 的
// MTProto 会话文件不能并发使用）。忙的时候要如实回「有任务在跑」，不能先答应
// 再悄悄丢掉——接口已经回过「已开始」，协程里抢不到锁直接 return 就是撒谎。
func TestJoinBackfillRefusesWhenBusy(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 2040, "hash"
	testutil.EnableAntiad(t, b, -100)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getChat"] = `{"ok":true,"result":{"id":-100,"type":"supergroup","username":"testgroup"}}`

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	done := make(chan struct{}, 4)
	oldRunner := backfillRunner
	backfillRunner = func(_ *core.Bot, chatID int64, _ string) ([]backfillRow, error) {
		if chatID == -100 {
			started <- struct{}{}
			<-release // 卡住第一个任务，模拟大群还在翻成员
		}
		done <- struct{}{} // runner 结束（之后协程还差一步才解锁）
		return nil, nil
	}
	defer func() { backfillRunner = oldRunner }()

	if ok, why := StartJoinBackfill(b, -100, "testgroup", true); !ok {
		t.Fatalf("第一个任务该起得来：%s", why)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个任务没跑起来")
	}
	// 忙时：明确拒绝并给出原因。
	if ok, why := StartJoinBackfill(b, -200, "", true); ok || why == "" {
		t.Fatalf("忙时应拒绝并说明原因，得到 ok=%v why=%q", ok, why)
	}
	close(release)
	// 等第一个任务收尾（runner 返回后协程还要写库、记日志，再解锁）。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个任务没收尾")
	}
	time.Sleep(50 * time.Millisecond)
	// 锁释放后要能再起；这一轮也等它跑完，别把锁带进下一个测试。
	if ok, why := StartJoinBackfill(b, -200, "", true); !ok {
		t.Fatalf("第一个任务结束后应能再起：%s", why)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("第二个任务没跑完")
	}
	time.Sleep(50 * time.Millisecond)
}

// TestResolveJoinTime：按需实时查询 —— 查到写库、之后不再起脚本；查不到
// 进负缓存，短时间内不重试。
func TestResolveJoinTime(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 2040, "hash"
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,5001,0,0,1,0,0)`); err != nil {
		t.Fatal(err)
	}

	ts := time.Now().Add(-40 * 24 * time.Hour).Unix()
	calls := 0
	old := joinLookupRunner
	joinLookupRunner = func(_ *core.Bot, _, _ int64) (int64, error) {
		calls++
		return ts, nil
	}
	defer func() { joinLookupRunner = old }()

	got, ok := ResolveJoinTime(b, -100, 5001)
	if !ok || got != ts {
		t.Fatalf("第一次应查到 %d，得到 %d/%v", ts, got, ok)
	}
	// 已入库：第二次直接秒回，不再起脚本。
	if got2, ok2 := ResolveJoinTime(b, -100, 5001); !ok2 || got2 != ts || calls != 1 {
		t.Fatalf("第二次该走库缓存：got=%d ok=%v calls=%d", got2, ok2, calls)
	}
	var inDB int64
	if err := b.Store.Read.QueryRow(`SELECT joined_at FROM group_members
		WHERE chat_id=-100 AND user_id=5001`).Scan(&inDB); err != nil {
		t.Fatal(err)
	}
	if inDB != ts {
		t.Errorf("查到的时间应写回库里，得到 %d", inDB)
	}
	// ensureJoinAge 把查到的时间补进画像（判定用的就是画像）。
	p := senderProfile{UserID: 5001}
	ensureJoinAge(b, -100, &p)
	if !p.AgeKnown || p.AgeHours <= 0 {
		t.Errorf("画像应补上年龄：known=%v hours=%d", p.AgeKnown, p.AgeHours)
	}

	// 查不到（已退群/被踢）：负缓存，第二次不再起脚本。
	joinLookupRunner = func(_ *core.Bot, _, _ int64) (int64, error) {
		calls++
		return 0, nil
	}
	if _, ok := ResolveJoinTime(b, -100, 5002); ok {
		t.Fatal("查不到时不该报 ok")
	}
	if _, ok := ResolveJoinTime(b, -100, 5002); ok || calls != 2 {
		t.Fatalf("负缓存应拦住第二次查询：calls=%d", calls)
	}
}

// TestJoinBackfillNotTriggeredOnAdminGrant：拿到管理员权限不再自动全量
// 补全 —— 改成按需实时查询（ResolveJoinTime），要预热整群走 Mini App 的
// 「补全历史入群时间」按钮。守门测试：别让全量扫描悄悄回来。
func TestJoinBackfillNotTriggeredOnAdminGrant(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	b.Cfg.TGAPIID, b.Cfg.TGAPIHash = 2040, "hash"
	calls := make(chan int64, 8)
	oldRunner := backfillRunner
	backfillRunner = func(_ *core.Bot, chatID int64, _ string) ([]backfillRow, error) {
		calls <- chatID
		return nil, nil
	}
	defer func() { backfillRunner = oldRunner }()
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
	HandleMyChatMemberUpdate(b, mk("member", "administrator"))
	select {
	case c := <-calls:
		t.Fatalf("拿到管理员权限不该再自动全量补全（收到 %d）", c)
	case <-time.After(300 * time.Millisecond):
	}
}
