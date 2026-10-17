package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestClearCommandWholeChat：/clear 把本群全部成员的判定史截到此刻，
// 非授权者静默忽略 —— 它改的是全群成员的判定输入。
func TestClearCommandWholeChat(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	now := time.Now().Unix()
	for _, uid := range []int64{8001, 8002} {
		touchMember(b, -100, uid, now-3600)
		BumpAdHits(b, -100, uid, 1)
		if _, err := b.Store.Write.Exec(`INSERT INTO group_messages
			(chat_id,message_id,user_id,text,at,media_group,punished)
			VALUES (-100,?,?,'日入5000 私聊我',?,'',1)`, uid, uid, now-300); err != nil {
			t.Fatal(err)
		}
	}

	// 普通成员：命令不执行，也不该留下一句回执暴露命令存在。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 800009, 31, "/clear"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE chat_id=-100 AND hist_cleared_at>0`); n != 0 {
		t.Fatal("非授权者不该能清理判定史")
	}
	for _, p := range fake.Calls("sendMessage") {
		if strings.Contains(stringOf(p["text"]), "已清理") {
			t.Error("非授权者触发时不该有任何回执")
		}
	}

	// 群管理员（本测试里 uid 1 是 bot 归属人兼主管理员）。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 1, 32, "/clear"))
	waitIdle(t, b)

	if n := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE chat_id=-100 AND user_id IN (8001,8002)
		AND hist_cleared_at>0 AND ad_hits=0`); n != 2 {
		t.Errorf("两名成员的判定史都应截断且命中数归零，实际 %d 人", n)
	}
	for _, uid := range []int64{8001, 8002} {
		if got := len(loadUserMessages(b.Store, -100, uid, 10, false)); got != 0 {
			t.Errorf("uid %d 的留底不该再进判定，实际还剩 %d 条", uid, got)
		}
	}
	// 留底不删：它是 /check 复查与连带删除的依据。
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages WHERE chat_id=-100`); n != 2 {
		t.Errorf("留底应原样保留，实际 %d 条", n)
	}
	found := false
	for _, p := range fake.Calls("sendMessage") {
		if strings.Contains(stringOf(p["text"]), "已清理") {
			found = true
		}
	}
	if !found {
		t.Error("执行后该回一条结果说明")
	}
}
