package panel

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestHandleWhiteDM：管理员私聊 /white <uid> 把人加进本 bot 的豁免名单。
func TestHandleWhiteDM(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)

	HandleWhiteDM(b, privMsg(777, "/white 555"), "/white 555")
	list := b.Cache.Snap().BotSettingInt64List(b.BotID(), "antiad_exempt_users")
	if !slices.Contains(list, int64(555)) {
		t.Fatalf("豁免名单里应有 555，得到 %v", list)
	}

	// 重复添加只提示，不报错。
	HandleWhiteDM(b, privMsg(777, "/white 555"), "/white 555")
	if got := b.Cache.Snap().BotSettingInt64List(b.BotID(), "antiad_exempt_users"); len(got) != 1 {
		t.Errorf("重复添加不该写两遍，得到 %v", got)
	}

	// 参数缺失给用法。
	HandleWhiteDM(b, privMsg(777, "/white"), "/white")
	fake := b.TG.(*testutil.FakeTG)
	if last := fake.LastCall("sendMessage"); last == nil ||
		!strings.Contains(fmt.Sprint(last["text"]), "用法") {
		t.Errorf("缺参数应回用法，得到 %v", last)
	}
}

// TestHandleWhiteDMRejectsOtherAdminsBot：次管只能改自己名下 bot 的
// 豁免名单，对别人的 bot 一律拒绝（否则等于为其开放漏判途径）。
func TestHandleWhiteDMRejectsOtherAdminsBot(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	if err := b.AddAdmin(888, "测试次管", 777); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)

	HandleWhiteDM(b, privMsg(888, "/white 555"), "/white 555")

	if got := b.Cache.Snap().BotSettingInt64List(b.BotID(), "antiad_exempt_users"); len(got) != 0 {
		t.Fatalf("次管不该能改他人 bot 的豁免名单，得到 %v", got)
	}
	if last := fake.LastCall("sendMessage"); last == nil ||
		!strings.Contains(fmt.Sprint(last["text"]), "只能管理") {
		t.Errorf("应回权限提示，得到 %v", last)
	}
}

// TestShowBotExemptListsWhitelistAndRemoves：豁免页同时列出白名单行，
// 并且能逐条移除。
func TestShowBotExemptListsWhitelistAndRemoves(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_whitelist
		(bot_id,chat_id,user_id,expires_at,source,by_uid,created_at)
		VALUES (?, -100, 555, ?, 'appeal', 1, 0)`,
		b.BotID(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	showBotExempt(b, 777, 0, b.BotID())
	last := fake.LastCall("sendMessage")
	text := fmt.Sprint(last["text"])
	if !strings.Contains(text, "白名单") || !strings.Contains(text, "555") {
		t.Fatalf("豁免页应列出白名单行:\n%s", text)
	}

	HandleAdminCallback(b, &tg.CallbackQuery{
		ID: "cb", Data: fmt.Sprintf("a:mb:%d:wd:-100:555", b.BotID()),
		From: &tg.TGUser{ID: 777},
		Message: &tg.Message{MessageID: 5,
			Chat: &tg.Chat{ID: 777, Type: "private"}},
	})
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_whitelist
		WHERE user_id=555`).Scan(&n)
	if n != 0 {
		t.Errorf("wd 回调应删掉白名单行，还剩 %d", n)
	}
}

// TestManagedBotsClauseFiltersSubAdmin：次级管理员只看得到自己 bot 的记录。
func TestManagedBotsClauseFiltersSubAdmin(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	testutil.RegisterTestBot(t, b.Shared, testToken2, 43, 888)
	if err := b.AddAdmin(888, "测试次管", 777); err != nil {
		t.Fatal(err)
	}

	if where, _ := managedBotsClause(b, 777); where != "" {
		t.Errorf("主管理员不该有过滤，得到 %q", where)
	}
	where, args := managedBotsClause(b, 888)
	if !strings.Contains(where, "bot_id IN") || len(args) != 1 || args[0].(int64) != 43 {
		t.Errorf("次管应只过滤到自己的 bot，得到 %q %v", where, args)
	}
	if where, _ := managedBotsClause(b, 999); where != " AND 0" {
		t.Errorf("名下没有 bot 时应查不到任何记录，得到 %q", where)
	}
}

// TestShowUserLogsRendersAndPages：/user 列表按用户过滤并渲染。
func TestShowUserLogsRendersAndPages(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,prompt_tokens,completion_tokens,quota_cost,created_at,bot_id,user_name)
		VALUES (-100,555,900,'广告','ad',0.9,'llm','scam','deleted_muted','',
		0,0,0,?,?, '昵称')`, time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}

	ShowUserLogs(b, 777, 777, 555)

	last := fake.LastCall("sendMessage")
	text := fmt.Sprint(last["text"])
	if !strings.Contains(text, "555") || !strings.Contains(text, "#1") {
		t.Errorf("列表应包含用户与记录号:\n%s", text)
	}
	if kb := fmt.Sprint(last["reply_markup"]); !strings.Contains(kb, "a:ad:rec:") {
		t.Errorf("列表项应带记录卡片按钮: %s", kb)
	}
}
