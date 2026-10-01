package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// lastNoticeContaining 取最后一条含关键字的群消息。
func lastNoticeContaining(t *testing.T, fake *testutil.FakeTG, sub string) string {
	t.Helper()
	var out string
	for _, p := range fake.Calls("sendMessage") {
		if s := stringOf(p["text"]); strings.Contains(s, sub) {
			out = s
		}
	}
	if out == "" {
		t.Fatalf("群里没有出现含 %q 的消息", sub)
	}
	return out
}

// TestJtimeShowsJoinTime：/jtime 用回复、user_id、@用户名 三种形态查询，
// 显示入群时间与时长，并把命令本身从群里删掉。
func TestJtimeShowsJoinTime(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	joined := time.Now().Add(-72 * time.Hour).Unix()
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,5001,?,?,9,?,0)`, joined, joined, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	// 回复形态：72 小时前入群 → 「3 天前」。
	m := testutil.GroupMsg(-100, 1, 20, "/jtime")
	m.ReplyToMessage = &tg.Message{MessageID: 7,
		From: &tg.TGUser{ID: 5001, FirstName: "张小三", Username: "zhang3"}}
	HandleGroupMessage(b, m)
	notice := lastNoticeContaining(t, fake, "入群时间")
	for _, want := range []string{"zhang3", "5001", "入群：", "3 天前", "发言 9 条"} {
		if !strings.Contains(notice, want) {
			t.Errorf("入群时间卡片应含 %q，得到：%s", want, notice)
		}
	}
	// 命令立即撤回；结果卡片挂进待撤回表，按 ttl 延迟撤回。
	if c := fake.LastCall("deleteMessage"); c == nil || c["message_id"] != float64(20) {
		t.Errorf("命令应立即从群里删掉，得到 %v", c)
	}
	var due int64
	if err := b.Store.Read.QueryRow(`SELECT due_at FROM alert_cleanup
		WHERE bot_id=? AND chat_id=-100 AND message_id=1`,
		b.BotID()).Scan(&due); err != nil {
		t.Fatalf("结果卡片应挂进待撤回表: %v", err)
	}
	if left := due - time.Now().Unix(); left < 60 || left > 3600 {
		t.Errorf("撤回时间应在合理延迟后（默认 5 分钟），得到 %d 秒后", left)
	}
	var n int64
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM alert_cleanup
		WHERE bot_id=? AND chat_id=-100 AND message_id=20`,
		b.BotID()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("命令是立即删除，不该挂进待撤回表")
	}

	// 参数形态（客户端补的 @botname 后缀也要认）：查资料补上昵称。
	fake.Resp["getChat"] = `{"ok":true,"result":{"id":5001,"first_name":"张小三","username":"zhang3"}}`
	m2 := testutil.GroupMsg(-100, 2, 21, "/jtime@some_bot 5001")
	HandleGroupMessage(b, m2)
	if n := lastNoticeContaining(t, fake, "入群时间"); !strings.Contains(n, "张小三") {
		t.Errorf("参数形态应带出查到的昵称，得到：%s", n)
	}

	// @用户名 形态。
	m3 := testutil.GroupMsg(-100, 2, 22, "/jtime @zhang3")
	HandleGroupMessage(b, m3)
	if n := lastNoticeContaining(t, fake, "入群时间"); !strings.Contains(n, "3 天前") {
		t.Errorf("@用户名 形态也应显示入群时间，得到：%s", n)
	}
}

// TestJtimeUnknownSaysWhy：没有入群记录时如实说未知并给出补救入口，
// 不编时间；频道身份则直接说明没有入群时间。
func TestJtimeUnknownSaysWhy(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	// 有画像（发过言）但入群时间未知：给首见时间当「至少待到这时」。
	first := time.Now().Add(-48 * time.Hour).Unix()
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,6006,0,?,4,?,0)`, first, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	HandleGroupMessage(b, testutil.GroupMsg(-100, 1, 30, "/jtime 6006"))
	n := lastNoticeContaining(t, fake, "入群时间")
	for _, want := range []string{"未知", "首见时间", "发言 4 条", "补全历史入群时间"} {
		if !strings.Contains(n, want) {
			t.Errorf("未知卡片应含 %q，得到：%s", want, n)
		}
	}

	// 频道身份不是群成员。
	m := testutil.GroupMsg(-100, 2, 31, "/jtime")
	m.ReplyToMessage = &tg.Message{MessageID: 8,
		From: &tg.TGUser{ID: -1001234567890, FirstName: "某频道"}}
	HandleGroupMessage(b, m)
	if n := lastNoticeContaining(t, fake, "频道"); !strings.Contains(n, "没有入群时间") {
		t.Errorf("频道应说明没有入群时间，得到：%s", n)
	}
}
