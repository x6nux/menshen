package antiad

import (
	"os"
	"path/filepath"
	"strings"
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

// TestJoinBackfillUnknownsFile：补全前把 joined_at=0 的 uid 写成名单，交给
// 脚本逐个补查 —— 批量列表在一万条左右截断，大群里漏掉的人就靠这条路径。
func TestJoinBackfillUnknownsFile(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	now := time.Now().Unix()
	seed := func(uid, joined int64) {
		if _, err := b.Store.Write.Exec(`INSERT INTO group_members
			(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
			VALUES (-100,?,?,?,1,?,0)`, uid, joined, now-100, now); err != nil {
			t.Fatal(err)
		}
	}
	seed(10, 0)
	seed(11, now-3600) // 已知：不该进名单
	seed(12, 0)
	seed(13, 0)
	// uid 10 有一条留底：名单要带上它，供最后一条兜底路径抠 access_hash。
	if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at) VALUES (-100,77,10,'广告',?)`,
		now-50); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(t.TempDir(), "unknowns.txt")
	n, err := writeJoinBackfillUnknowns(b, -100, p, 2) // 上限 2：只带升序的前两个
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("名单该受上限约束为 2 人，得到 %d", n)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "10\t77" || lines[1] != "12\t0" {
		t.Errorf("名单应为 `uid\\t最新留底消息id`（升序、受上限约束），得到 %v", lines)
	}
	// 没有未知成员时不建文件、也不算错。
	empty := filepath.Join(t.TempDir(), "none.txt")
	if n, err := writeJoinBackfillUnknowns(b, -999, empty, 100); err != nil || n != 0 {
		t.Fatalf("没有未知成员时应返回 0：n=%d err=%v", n, err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("空名单不该建文件")
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
