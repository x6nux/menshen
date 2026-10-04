package panel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"menshen/internal/antiad"
	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// cb 造一条回调。
func cb(from int64, data string) *tg.CallbackQuery {
	return &tg.CallbackQuery{ID: "q", From: &tg.TGUser{ID: from}, Data: data,
		Message: &tg.Message{MessageID: 9, Chat: &tg.Chat{ID: from, Type: "private"}}}
}

// seedLog 直接落一条流水。
func seedLog(t *testing.T, b *core.Bot, uid int64, text, action string) int64 {
	t.Helper()
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,text,
		verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,?,7,?,'ad',0.95,'systemone','scam',?,'',0,?)`, uid, text, action, b.BotID())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestRecordCardRespectsTenancy：汇总里的「📋 #id」进记录卡片，带原文，只发到私聊；
// callback_data 是客户端发上来的，别人 bot 的记录不能看。
func TestRecordCardRespectsTenancy(t *testing.T) {
	b, fake, sh := testutil.NewTestBotOwned(t, 1, 100)
	if err := sh.AddAdmin(200, "另一个次管", 1); err != nil {
		t.Fatal(err)
	}
	id := seedLog(t, b, 42, "广告原文", "deleted")

	HandleAdminCallback(b, cb(200, "a:ad:rec:"+itoa(id)))
	if fake.CountCalls("sendMessage") != 0 {
		t.Error("别人名下的次管不该看到这条记录")
	}
	HandleAdminCallback(b, cb(100, "a:ad:rec:"+itoa(id)))
	p := fake.LastCall("sendMessage")
	if p == nil || !strings.Contains(p["text"].(string), "广告原文") {
		t.Fatalf("归属人应看到记录卡片: %v", p)
	}
}

// TestFalsePositiveForgetsHashAndUnbans：误判要撤掉内容哈希（否则同样的内容会
// 一直被直接删下去），封禁过的要解封且带 only_if_banned。
func TestFalsePositiveForgetsHashAndUnbans(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, 42, "日入过万", "deleted_banned")
	b.Store.Write.Exec(`INSERT INTO ad_hashes (bot_id,hash,log_id,created_at,last_hit_at)
		VALUES (?,?,?,0,0)`, b.BotID(), hashOf("日入过万"), id)

	HandleAdminCallback(b, cb(1, "a:ad:fp:"+itoa(id)))

	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_hashes`).Scan(&n)
	if n != 0 {
		t.Error("误判后内容哈希应撤掉")
	}
	p := fake.LastCall("unbanChatMember")
	if p == nil || p["only_if_banned"] != true {
		t.Errorf("封禁过的误判应解封且带 only_if_banned: %v", p)
	}
}

// TestManualMuteChannel：频道身份没有成员权限可改，人工禁言只能 banChatSenderChat。
func TestManualMuteChannel(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, -1009, "x", "deleted")
	HandleAdminCallback(b, cb(1, "a:ad:mute:"+itoa(id)))
	if fake.CountCalls("restrictChatMember") != 0 || fake.CountCalls("banChatSenderChat") != 1 {
		t.Error("频道身份的人工禁言应走 banChatSenderChat")
	}
}

// TestChatPunishCycles：每群处罚方式 跟随 → 禁言 → 封禁 → 跟随。
func TestChatPunishCycles(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	for _, want := range []int64{0, 1, -1} {
		HandleAdminCallback(b, cb(1, "a:mb:"+itoa(b.BotID())+":c:-100:pn"))
		if got := testutil.ChatConfOf(t, b, -100).Punish; got != want {
			t.Fatalf("punish = %d，期望 %d", got, want)
		}
	}
}

// TestAdChatHealthCached：群权限自检要发 getChatMember，而它跑在 bot 的
// 串行更新路径上（TG 慢时最坏 40 秒不收新消息）。同一群的重复渲染必须
// 命中缓存。
func TestAdChatHealthCached(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	for i := 0; i < 3; i++ {
		if got := adChatHealth(b, -100); !strings.Contains(got, "权限正常") {
			t.Fatalf("应识别为管理员，得到 %q", got)
		}
	}
	if n := fake.CountCalls("getChatMember"); n != 1 {
		t.Errorf("三次渲染只该查一次，实际 %d 次", n)
	}
}

// TestChatDetailShowsPunishDuration：群详情要写明禁言的时长与实际处罚，
// 并在未开启封禁时给出改法。只写「禁言」会让人以为永久封禁已生效。
func TestChatDetailShowsPunishDuration(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)

	showChatDetail(b, 777, 0, b.BotID(), -100)
	text := fmt.Sprint(fake.LastCall("sendMessage")["text"])
	// 默认 1440 分钟 → 渲染成「禁言 1 天」。
	if !strings.Contains(text, "禁言 1 天") || !strings.Contains(text, "要改成永久禁言") {
		t.Fatalf("应写明禁言时长与改法:\n%s", text)
	}

	// 禁言时长 0 = 永久禁言：人留在群里、发不了言。
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	showChatDetail(b, 777, 0, b.BotID(), -100)
	text = fmt.Sprint(fake.LastCall("sendMessage")["text"])
	if !strings.Contains(text, "永久禁言") || strings.Contains(text, "要改成永久禁言") {
		t.Fatalf("配成 0 后应显示永久禁言且不再提示改法:\n%s", text)
	}

	// 开启封禁后显示永久封禁出群（优先级高于禁言时长）。
	if err := b.PutBotSetting(b.BotID(), "antiad_ban", "1"); err != nil {
		t.Fatal(err)
	}
	showChatDetail(b, 777, 0, b.BotID(), -100)
	text = fmt.Sprint(fake.LastCall("sendMessage")["text"])
	if !strings.Contains(text, "封禁出群（永久）") {
		t.Fatalf("开启封禁后应显示永久封禁:\n%s", text)
	}
	if strings.Contains(text, "要改成永久") {
		t.Error("已开启封禁时不该再提示改法")
	}
}

// TestFalsePositiveLiftsGban：误判时由这条判定派生的联合封禁也要撤——
// 名单跨所有接入群执行，留着等于让一条被判错的记录继续全平台封人
// （线上真实事件：ping0.cc 被误判后，人还留在名单里）。
func TestFalsePositiveLiftsGban(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	if err := antiad.GbanAdd(sh, 555, "自动判定：测试", -100, b.BotID()); err != nil {
		t.Fatal(err)
	}
	if err := antiad.GbanOwnAddBan(sh, b.Owner(), 555, "自动判定：测试", -100); err != nil {
		t.Fatal(err)
	}
	id := seedLog(t, b, 555, "ping0.cc", "deleted_muted")

	HandleAdminCallback(b, cb(777, "a:ad:fp:"+itoa(id)))

	if _, in := sh.Cache.Snap().Gban[555]; in {
		t.Error("误判应撤掉全局联合封禁")
	}
	if _, in := sh.Cache.Snap().GbanOwnBans[777][555]; in {
		t.Error("误判应撤掉操作者专属组里的条目")
	}
	var action, reason string
	b.Store.Read.QueryRow(`SELECT action,reason FROM antiad_log WHERE id=?`, id).
		Scan(&action, &reason)
	if action != "undone" {
		t.Errorf("流水应标记 undone，得到 %q", action)
	}
	if !strings.Contains(reason, "联合封禁") {
		t.Errorf("理由里应注明撤了名单，得到 %q", reason)
	}
}

// TestFalsePositiveLiftsRecordOwnerGban：主管理员在别人的 bot 记录上点误判，
// 既要撤操作者自己的账本，也要撤**记录所属 bot 归属人**的专属组——否则
// 误判撤了，人在那个归属人的群里还封着。
func TestFalsePositiveLiftsRecordOwnerGban(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	sh := b.Shared
	testutil.EnableAntiad(t, b, -100)
	if err := sh.AddAdmin(888, "次管", 777); err != nil {
		t.Fatal(err)
	}
	// 另一个归属人（888）的 bot，与它名下的群。
	const token2 = "123456789:AAAnotherBot_for_gban_test_00000001"
	testutil.RegisterTestBot(t, sh, token2, 43, 888)
	if err := antiad.GbanOwnAddBan(sh, 888, 555, "自动判定：测试", -100); err != nil {
		t.Fatal(err)
	}

	// 记录挂在 888 的 bot 名下。
	res, err := b.Store.Write.Exec(`INSERT INTO antiad_log (chat_id,user_id,message_id,
		text,verdict,confidence,decider,ad_kind,action,reason,created_at,bot_id)
		VALUES (-100,555,7,'ping0.cc','ad',0.62,'systemone+llm','promo','gban_muted','',0,43)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	HandleAdminCallback(b, cb(777, "a:ad:fp:"+itoa(id)))

	if _, in := sh.Cache.Snap().GbanOwnBans[888][555]; in {
		t.Error("主管理员点误判应撤掉记录所属 bot 归属人专属组里的条目")
	}
	var reason string
	b.Store.Read.QueryRow(`SELECT reason FROM antiad_log WHERE id=?`, id).Scan(&reason)
	if !strings.Contains(reason, "联合封禁") {
		t.Errorf("理由里应注明撤了名单，得到 %q", reason)
	}
}

// TestUserCardShowsProfileAndProcessedOnly：/user 要给出资料卡（昵称/用户名/
// ID/简介/发言数/首见/最近/判定统计），并且**默认只列被处置过的**记录；
// 点切换才连未处置的一起列出来。
func TestUserCardShowsProfileAndProcessedOnly(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	// TG 侧的资料（getChat）：昵称、用户名、简介。
	fake.Resp["getChat"] = `{"ok":true,"result":{"first_name":"白展堂",` +
		`"username":"tgdsBZT","bio":"我的TG频道 @tgds100 👉私信找我"}}`
	// 两条：一条被处置、一条只是被判过（正常）。
	paid := seedLog(t, b, 555, "广告原文", "deleted_muted")
	clean := seedLog(t, b, 555, "日常闲聊", "none")
	if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET verdict='clean' WHERE id=?`, clean); err != nil {
		t.Fatal(err)
	}

	ShowUserLogs(b, 1, 1, 555)
	p := fake.LastCall("sendMessage")
	if p == nil {
		t.Fatal("应发出资料卡")
	}
	text, _ := p["text"].(string)
	for _, want := range []string{"用户资料", "555", "白展堂", "tgdsBZT",
		"我的TG频道", "留底", "历史命中", "被处置过"} {
		if !strings.Contains(text, want) {
			t.Errorf("资料卡应包含 %q：\n%s", want, text)
		}
	}
	// 默认只看处置过的：被罚过的那条在、只是判过正常的那条不在。
	if !strings.Contains(text, fmt.Sprintf("#%d", paid)) {
		t.Errorf("默认应列出被处置过的记录 #%d：\n%s", paid, text)
	}
	if strings.Contains(text, fmt.Sprintf("#%d", clean)) {
		t.Errorf("默认不该列未处置的记录 #%d", clean)
	}
	kb := fmt.Sprint(p["reply_markup"])
	if !strings.Contains(kb, "a:ad:ul:555:1:all") {
		t.Errorf("应给出「显示全部判定记录」的切换，得到 %s", kb)
	}

	// 切换后：全部列出。
	HandleAdminCallback(b, cb(1, "a:ad:ul:555:1:all"))
	var all string
	for _, m := range []string{"editMessageText", "sendMessage"} {
		for _, c := range fake.Calls(m) {
			if s, ok := c["text"].(string); ok && strings.Contains(s, "判定记录") {
				all = s
			}
		}
	}
	if all == "" {
		t.Fatal("切换后应编辑出全部列表")
	}
	if !strings.Contains(all, fmt.Sprintf("#%d", clean)) ||
		!strings.Contains(all, fmt.Sprintf("#%d", paid)) {
		t.Errorf("切换后应同时列出两条记录（#%d、#%d）：\n%s", paid, clean, all)
	}
}

// TestConfirmBypassesVeteranExemption：管理员点「✅ 判定正确」等于给判定背书，
// 该按本群处罚方式补一次正式处置（绕过老成员免禁言），动作标签同步更新 ——
// 之后点「误判」仍能按标签解禁。
func TestConfirmBypassesVeteranExemption(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	// 老成员档的落库：只删不禁（deleted）。
	id := seedLog(t, b, 555, "转发色情相册截图", "deleted")
	if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET verdict='ad',ad_kind='porn_bait' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	HandleAdminCallback(b, cb(1, "a:ad:ok:"+itoa(id)))

	// 补了一次正式禁言。
	if fake.CountCalls("restrictChatMember") == 0 {
		t.Fatal("确认后应按本群处罚方式禁言")
	}
	// 动作标签与理由更新。
	var action, reason string
	if err := b.Store.Read.QueryRow(`SELECT action,reason FROM antiad_log WHERE id=?`, id).
		Scan(&action, &reason); err != nil {
		t.Fatal(err)
	}
	if action != "deleted_muted" {
		t.Errorf("动作应更新成 deleted_muted，得到 %q", action)
	}
	if !strings.Contains(reason, "管理员确认判定正确") ||
		!strings.Contains(reason, "不看资历") {
		t.Errorf("理由里应写明绕过资历，得到 %q", reason)
	}

	// 反悔：点误判应把这条禁言解掉（按动作标签走既有解禁路径）。
	before := fake.CountCalls("restrictChatMember")
	HandleAdminCallback(b, cb(1, "a:ad:fp:"+itoa(id)))
	if fake.CountCalls("restrictChatMember") <= before {
		t.Error("确认后再点误判应解禁（沿用原有解禁路径）")
	}
}

// TestUserCardShowsAndLiftsPenalties：/user 卡片要列出「生效中的限制」与
// 「联合封禁」，并给出解除按钮；点解除后限制记录与名单条目都要清掉。
func TestUserCardShowsAndLiftsPenalties(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, dispatch)
	fake := b.TG.(*testutil.FakeTG)
	sh := b.Shared
	if err := antiad.GbanOwnSetChat(sh, b.Owner(), -100, true); err != nil {
		t.Fatal(err)
	}
	// 555 在自己（归属人）的专属组名单里；556 有一条进群限制（个人简介）。
	if err := antiad.GbanOwnAddBan(sh, b.Owner(), 555, "简介推广接码服务", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO join_mutes
		(chat_id,user_id,bot_id,reason,notice_msg,attempts,created_at)
		VALUES (-100,556,?, '简介写着加微信',0,0,?)`, b.BotID(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	ShowUserLogs(b, 777, 777, 555)
	last := fake.LastCall("sendMessage")
	text := fmt.Sprint(last["text"])
	kb := fmt.Sprint(last["reply_markup"])
	for _, want := range []string{"生效中的限制", "联合封禁"} {
		if !strings.Contains(text, want) {
			t.Errorf("资料卡应含 %q：\n%s", want, text)
		}
	}
	if !strings.Contains(kb, "a:ad:gbl:555:1") {
		t.Fatalf("应给出「解除联合封禁」按钮：%s", kb)
	}
	// 点「解除联合封禁」：专属组条目清掉。
	HandleAdminCallback(b, cb(777, "a:ad:gbl:555:1"))
	if _, ok := sh.Cache.Snap().GbanOwnBans[b.Owner()][555]; ok {
		t.Error("点解除后专属组条目该没了")
	}

	// 556 的资料卡要有逐群解除按钮；点了进群限制记录要清掉。
	ShowUserLogs(b, 777, 777, 556)
	kb = fmt.Sprint(fake.LastCall("sendMessage")["reply_markup"])
	if !strings.Contains(kb, "a:ad:lift:-100:556:1") {
		t.Fatalf("应给出逐群解除按钮：%s", kb)
	}
	HandleAdminCallback(b, cb(777, "a:ad:lift:-100:556:1"))
	var n int64
	sh.Store.Read.QueryRow(`SELECT COUNT(*) FROM join_mutes
		WHERE chat_id=-100 AND user_id=556`).Scan(&n)
	if n != 0 {
		t.Error("点解除后进群限制记录该被清掉")
	}
}

// TestManualDeleteAlreadyGone：管理员点「删除」时消息已被别人先删掉，
// deleteMessage 报 not found —— 目标已达成，回执应是「已删除」而不是
// 「删除失败」，记录也要落成 deleted。
func TestManualDeleteAlreadyGone(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, 42, "广告原文", "alerted")
	fake.Resp["deleteMessage"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: message to delete not found"}`

	HandleAdminCallback(b, cb(1, "a:ad:del:"+itoa(id)))

	p := fake.LastCall("answerCallbackQuery")
	if p == nil || !strings.Contains(fmt.Sprint(p["text"]), "已删除") {
		t.Fatalf("回执应为已删除: %v", p)
	}
	var action string
	b.Store.Read.QueryRow(`SELECT action FROM antiad_log WHERE id=?`, id).Scan(&action)
	if action != "deleted" {
		t.Errorf("记录动作应变更为 deleted，得到 %q", action)
	}
}

// TestManualMuteGoneUser：人已被封禁出群时人工禁言必然失败，
// 二次判断后回执「无需禁言」，且不把记录改成已禁言。
func TestManualMuteGoneUser(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	id := seedLog(t, b, 42, "广告原文", "deleted")
	fake.Resp["restrictChatMember"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: PARTICIPANT_ID_INVALID"}`

	HandleAdminCallback(b, cb(1, "a:ad:mute:"+itoa(id)))

	p := fake.LastCall("answerCallbackQuery")
	if p == nil || !strings.Contains(fmt.Sprint(p["text"]), "无需禁言") {
		t.Fatalf("回执应说明无需禁言: %v", p)
	}
	var action string
	b.Store.Read.QueryRow(`SELECT action FROM antiad_log WHERE id=?`, id).Scan(&action)
	if action != "deleted" {
		t.Errorf("没禁上就不该把记录改成 deleted_muted，得到 %q", action)
	}
}

// TestManualConfirmGoneUserSkipsMute：确认判定正确时若人已出群，
// 不能把记录标成 deleted_muted（根本没禁上），理由里留痕供回溯。
func TestManualConfirmGoneUserSkipsMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	id := seedLog(t, b, 42, "广告原文", "deleted")
	fake.Resp["restrictChatMember"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: PARTICIPANT_ID_INVALID"}`

	HandleAdminCallback(b, cb(1, "a:ad:ok:"+itoa(id)))

	p := fake.LastCall("answerCallbackQuery")
	if p == nil || !strings.Contains(fmt.Sprint(p["text"]), "无需禁言") {
		t.Fatalf("回执应说明无需禁言: %v", p)
	}
	var action, reason string
	b.Store.Read.QueryRow(`SELECT action,reason FROM antiad_log WHERE id=?`, id).Scan(&action, &reason)
	if action != "deleted" {
		t.Errorf("禁言没落地，动作不该变成 %q", action)
	}
	if !strings.Contains(reason, "未追加禁言") {
		t.Errorf("理由应写明未追加禁言：%q", reason)
	}
}
