package antiad

import (
	"slices"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
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

// cbDataOfKB 取出 inline_keyboard 里全部 callback_data。FakeTG 的载荷经过
// 一次 JSON 往返，嵌套值都是 any。
func cbDataOfKB(t *testing.T, kb any) []string {
	t.Helper()
	m, ok := kb.(map[string]any)
	if !ok {
		t.Fatalf("载荷里没有 reply_markup: %#v", kb)
	}
	rows, _ := m["inline_keyboard"].([]any)
	var out []string
	for _, r := range rows {
		btns, _ := r.([]any)
		for _, b := range btns {
			btn, _ := b.(map[string]any)
			if s, ok := btn["callback_data"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// TestClearCommandDMPicksTarget：私聊 /clear 先给一份目标清单（每群一个按钮
// + 全部群），带 chat_id 只清那一个群，点「全部群」清掉范围内所有群。
// 范围与 /ban 对称：主管理员=全部 bot 的群，次管=自己名下 bot 的群；
// 非管理员静默忽略。
func TestClearCommandDMPicksTarget(t *testing.T) {
	reg, a := testutil.NewTestRegistry(t, dispatch)
	sh := a.Shared
	b, _ := testutil.AddRegistryBot(t, reg, sh, 4343, a.Owner())
	c, _ := testutil.AddRegistryBot(t, reg, sh, 4444, 999)
	testutil.EnableAntiad(t, a, -100)
	testutil.EnableAntiad(t, b, -200)
	testutil.EnableAntiad(t, c, -300)
	if err := a.PutSetting("antiad_cmd_rpm", "20"); err != nil {
		t.Fatalf("放宽命令限频失败: %v", err)
	}

	chats := []int64{-100, -200, -300}
	now := time.Now().Unix()
	for i, chat := range chats {
		uid := int64(9001 + i)
		touchMember(a, chat, uid, now-3600)
		BumpAdHits(a, chat, uid, 1)
		if _, err := a.Store.Write.Exec(`INSERT INTO group_messages
			(chat_id,message_id,user_id,text,at,media_group,punished)
			VALUES (?,?,?,'日入5000 私聊我',?,'',1)`, chat, uid, uid, now-300); err != nil {
			t.Fatal(err)
		}
	}

	// 范围：主管理员看得到全部三个群，次管只看自己名下 bot 的群。
	main := clearTargets(sh, 777)
	if len(main) != 3 {
		t.Fatalf("主管理员应看到 3 个群，实际 %d 个", len(main))
	}
	if err := sh.AddAdmin(999, "次管", 777); err != nil {
		t.Fatalf("加次管失败: %v", err)
	}
	sub := clearTargets(sh, 999)
	if len(sub) != 1 || sub[0].ChatID != -300 {
		t.Fatalf("次管应只看到自己名下 bot 的群，实际 %+v", sub)
	}

	fake := a.TG.(*testutil.FakeTG)
	dm := func(uid int64, text string) *tg.Message {
		return &tg.Message{MessageID: 9, Date: 1700000000, Text: text,
			From: &tg.TGUser{ID: uid}, Chat: &tg.Chat{ID: uid, Type: "private"}}
	}
	recv := func(t *testing.T, kb bool) []map[string]any {
		t.Helper()
		calls := fake.Calls("sendMessage")
		fake.Reset()
		if kb && len(calls) != 1 {
			t.Fatalf("该回一条目标清单，实际 %d 条", len(calls))
		}
		return calls
	}

	// 非管理员：静默忽略。
	HandleClearCommandDM(a, dm(555, "/clear"), "")
	if len(recv(t, false)) != 0 {
		t.Fatal("非管理员触发时不该有任何回应")
	}

	// 清单阶段只列目标，不动数据；按钮顺序按群名（同名按 chat_id）再追加全部群。
	HandleClearCommandDM(a, dm(777, "/clear"), "")
	calls := recv(t, true)
	want := []string{clearCallbackData(-300), clearCallbackData(-200),
		clearCallbackData(-100), clearCallbackData(0)}
	if got := cbDataOfKB(t, calls[0]["reply_markup"]); !slices.Equal(got, want) {
		t.Errorf("目标清单按钮 = %q，期望 %q", got, want)
	}
	for i, chat := range chats {
		if n := len(loadUserMessages(a.Store, chat, int64(9001+i), 10, false)); n != 1 {
			t.Fatalf("清单阶段不该动数据：群 %d 还剩 %d 条", chat, n)
		}
	}

	// 带 chat_id：只清那一个群。
	HandleClearCommandDM(a, dm(777, "/clear"), "-200")
	recv(t, false)
	if n := len(loadUserMessages(a.Store, -200, 9002, 10, false)); n != 0 {
		t.Errorf("目标群应被清理，实际还剩 %d 条", n)
	}
	for i, chat := range []int64{-100, -300} {
		if n := len(loadUserMessages(a.Store, chat, int64(9001+i*2), 10, false)); n != 1 {
			t.Errorf("群 %d 不该被牵连，实际还剩 %d 条", chat, n)
		}
	}
	// 不在范围内的群：如实拒绝，不动数据。
	HandleClearCommandDM(a, dm(777, "/clear"), "-999")
	recv(t, false)
	if n := countRows(t, a, `SELECT COUNT(*) FROM group_members
		WHERE chat_id=-300 AND hist_cleared_at>0`); n != 0 {
		t.Error("范围外的 chat_id 不该被接受")
	}

	// 点「全部群」：范围内三个群一起清。
	HandleClearCallback(a, &tg.CallbackQuery{ID: "cb1",
		From:    &tg.TGUser{ID: 777},
		Data:    clearCallbackData(0),
		Message: &tg.Message{MessageID: 9, Chat: &tg.Chat{ID: 777, Type: "private"}}})
	if fake.CountCalls("editMessageText") == 0 && fake.CountCalls("sendMessage") == 0 {
		t.Error("点按钮后该把结果回报给发起人")
	}
	for i, chat := range chats {
		if n := len(loadUserMessages(a.Store, chat, int64(9001+i), 10, false)); n != 0 {
			t.Errorf("全部群清理后群 %d 不该再留底进判定，实际 %d 条", chat, n)
		}
	}
	if n := countRows(t, a, `SELECT COUNT(*) FROM group_members
		WHERE chat_id IN (-100,-200,-300) AND hist_cleared_at>0 AND ad_hits=0`); n != 3 {
		t.Errorf("三个群都该落截断时刻且命中数归零，实际 %d 个", n)
	}
}
