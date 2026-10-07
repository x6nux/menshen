package antiad

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// logRow 取最后一条流水的 verdict/action/reason。
func logRow(t *testing.T, b *core.Bot) (verdict, action, reason string) {
	t.Helper()
	b.Store.Read.QueryRow(`SELECT verdict,action,reason FROM antiad_log
		ORDER BY id DESC LIMIT 1`).Scan(&verdict, &action, &reason)
	return
}

// untilOf 取 restrictChatMember 的 until_date 距现在的秒数。
func untilOf(p map[string]any) int64 {
	return int64(p["until_date"].(float64)) - time.Now().Unix()
}

// TestTempMuteOutlastsAIBudget：临时禁言必须长过复判最坏耗时，否则正式禁言
// 落地前它就到期了；TG 把不足 30 秒的限时当成永久。
func TestTempMuteOutlastsAIBudget(t *testing.T) {
	if tempMute <= aiTotalBudget || tempMute < 30*time.Second {
		t.Errorf("tempMute = %v，必须长过 aiTotalBudget %v 且不短于 30 秒", tempMute, aiTotalBudget)
	}
}

// TestTempMuteIsFiveMinutes：临时禁言给到 5 分钟 —— 复判最坏 45 秒，但上游
// 抖动时会重试；余量不足的话定案前自动解禁，等于未起到限制作用。
func TestTempMuteIsFiveMinutes(t *testing.T) {
	if tempMute != 5*time.Minute {
		t.Errorf("tempMute = %v，应为 5 分钟", tempMute)
	}
}

// TestDeleteFirstThenReview：初判一出结论就删、要禁言的先临时禁言，
// 复判确认后转正式禁言；整条只落一条流水。
func TestDeleteFirstThenReview(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	_, llmN := fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"),
		llmReply(true, 0.95, "scam", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万 私聊"))
	waitIdle(t, b)

	if llmN.Load() != 1 {
		t.Fatalf("要禁言的应交给大模型复判，复判 %d 次", llmN.Load())
	}
	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("消息应只删一次（初判先删），实际 %d 次", n)
	}
	mutes := fake.Calls("restrictChatMember")
	if len(mutes) != 2 {
		t.Fatalf("应先临时禁言、再正式禁言，实际 %d 次", len(mutes))
	}
	if d := untilOf(mutes[0]); d > int64(tempMute/time.Second)+5 {
		t.Errorf("第一次应是临时禁言，时长 %d 秒", d)
	}
	if d := untilOf(mutes[1]); d < 23*3600 {
		t.Errorf("复判确认后应是正式禁言，时长 %d 秒", d)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log`); n != 1 {
		t.Errorf("一条消息只该落一条流水，实际 %d", n)
	}
	if _, action, reason := logRow(t, b); action != "deleted_muted" || !strings.Contains(reason, "初判先行") {
		t.Errorf("action=%q reason=%q", action, reason)
	}
}

// TestReviewCleanLiftsTempMute：复判判为正常时要主动解掉临时禁言 ——
// 它只是复判期间的占位；让一个已被判为正常的人等待 5 分钟无法发言，
// 比漏判一条同类消息更糟。流水照实记为已删除（删除不可撤销）。
func TestReviewCleanLiftsTempMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(false, 0.8, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万"))
	waitIdle(t, b)

	mutes := fake.Calls("restrictChatMember")
	if len(mutes) != 2 {
		t.Fatalf("应先临时禁言、复判正常后再解除，实际 %d 次", len(mutes))
	}
	if d := untilOf(mutes[0]); d <= 0 {
		t.Errorf("第一次应是限时禁言，时长 %d 秒", d)
	}
	// 第二次必须是权限全开：再发一次全 false 等于又禁言一次。
	perms, _ := mutes[1]["permissions"].(map[string]any)
	if perms == nil || perms["can_send_messages"] != true {
		t.Errorf("复判正常后应解除临时禁言，得到 %v", mutes[1])
	}
	if verdict, action, reason := logRow(t, b); verdict != "clean" || action != "deleted" ||
		!strings.Contains(reason, "已解除临时禁言") {
		t.Errorf("verdict=%q action=%q reason=%q", verdict, action, reason)
	}
	if gm, _ := loadMember(b.Store, -100, 42); gm.AdHits != 0 {
		t.Error("复判为正常的不该计命中")
	}
}

// TestReviewCleanKeepsOtherMute：同一人在本群另有未解除的正式处罚时，
// 复判正常也不主动解禁 —— 那种禁言是他另一条消息挣来的，解掉会把那条
// 判定一起踩掉。
func TestReviewCleanKeepsOtherMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(false, 0.8, "none", "message"))
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,42,1,'加微信','ad',0.99,'llm','scam','deleted_muted','自己的广告',?,?)`,
		time.Now().Unix(), b.BotID()); err != nil {
		t.Fatal(err)
	}

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万"))
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 1 {
		t.Errorf("另有生效处罚时不该主动解禁，实际 %d 次", n)
	}
}

// TestConfidentOldMemberSkipsReview：初判有把握又不用禁言的（老成员只删）不复判。
func TestConfidentOldMemberSkipsReview(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	_, llmN := fakeAIWith(t, b, soReplySev("ad", 0.95, "promo", "message", 1),
		llmReply(true, 0.9, "promo", "message"))
	b.Store.Write.Exec(`INSERT INTO group_members (chat_id,user_id,joined_at,first_seen,msg_count)
		VALUES (-100,42,1,1,500)`)

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "广告"))
	waitIdle(t, b)
	if llmN.Load() != 0 {
		t.Errorf("老成员高置信只删不禁，不该复判（复判 %d 次）", llmN.Load())
	}
	if fake.CountCalls("deleteMessage") != 1 || fake.CountCalls("restrictChatMember") != 0 {
		t.Error("老成员应只删不禁")
	}
}

// TestAccountScopePurges：账号本身是广告号、新人、过删除+禁言线 → 连带删除近期全部消息。
func TestAccountScopePurges(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	// 连带删除只删 47 小时内的（TG 的删除期限），测试消息得是刚发的。
	now := func(m *tg.Message) *tg.Message { m.Date = time.Now().Unix(); return m }
	HandleGroupMessage(b, now(testutil.GroupMsg(-100, 42, 1, "你好")))
	waitIdle(t, b)
	pic := now(testutil.GroupMsg(-100, 42, 2, ""))
	pic.Photo = []tg.PhotoSize{{FileID: "f", FileUniqueID: "u", Width: 10, Height: 10}}
	HandleGroupMessage(b, pic) // 纯图没配识图，不判，但要留底供连带删除
	// 两天前的那条不在删除期限内，不该带上
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 9, "很久以前"))

	fakeAIWith(t, b, soReply("ad", 0.97, "promo", "account"), llmReply(true, 0.97, "promo", "account"))
	HandleGroupMessage(b, now(testutil.GroupMsg(-100, 42, 3, "看我主页")))
	waitIdle(t, b)

	calls := fake.Calls("deleteMessages")
	if len(calls) == 0 {
		t.Fatal("账号级广告应连带删除（deleteMessages）")
	}
	ids := calls[len(calls)-1]["message_ids"].([]any)
	if len(ids) != 3 {
		t.Errorf("应删掉此人近期全部 3 条（含纯图），实际 %v", ids)
	}
	if _, _, reason := logRow(t, b); !strings.Contains(reason, purgeNote) {
		t.Errorf("流水理由应注明连带删除: %q", reason)
	}
}

// TestAlbumDeletedAsWhole：相册配文只挂在一张上，判成广告整组删，之后才到的也删。
func TestAlbumDeletedAsWhole(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.97, "promo", "message"), llmReply(true, 0.97, "promo", "message"))
	b.Store.Write.Exec(`INSERT INTO group_members (chat_id,user_id,joined_at,first_seen,msg_count)
		VALUES (-100,42,1,1,500)`) // 老成员：只删不禁，不牵扯复判

	first := testutil.GroupMsg(-100, 42, 10, "")
	first.MediaGroupID = "alb"
	HandleGroupMessage(b, first)
	cap := testutil.GroupMsg(-100, 42, 11, "")
	cap.Caption, cap.MediaGroupID = "加我微信领福利", "alb"
	HandleGroupMessage(b, cap)
	waitIdle(t, b)

	calls := fake.Calls("deleteMessages")
	if len(calls) == 0 || len(calls[0]["message_ids"].([]any)) != 2 {
		t.Fatalf("相册应整组删除: %v", calls)
	}
	late := testutil.GroupMsg(-100, 42, 12, "")
	late.MediaGroupID = "alb"
	before := fake.CountCalls("deleteMessage")
	HandleGroupMessage(b, late)
	if fake.CountCalls("deleteMessage") != before+1 {
		t.Error("判定之后才到的相册图片也应删除")
	}
}

// TestAdAllowDedupByMessage：护栏按消息 ID 去重（带编辑时间），不按文字——
// 多号轮番刷同一段模板时按文字去重会只判第一个号。
func TestAdAllowDedupByMessage(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := b.PutBotSetting(b.BotID(), "antiad_rpm_chat", "2"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	if ok, _ := adAllow(b, snap, -100, 1, 0, false); !ok {
		t.Fatal("首条应放行")
	}
	if ok, why := adAllow(b, snap, -100, 1, 0, false); ok || why != adDupMessage {
		t.Errorf("同一条重复投递应被拦下，why=%q", why)
	}
	if ok, _ := adAllow(b, snap, -100, 1, 99, false); !ok {
		t.Error("同一条消息的编辑版要放行")
	}
	if ok, why := adAllow(b, snap, -100, 2, 0, false); ok || !strings.Contains(why, "频率") {
		t.Errorf("老成员超出上限应拦下，why=%q", why)
	}
	if ok, _ := adAllow(b, snap, -100, 3, 0, true); !ok {
		t.Error("新人不受每群上限约束")
	}
}

// TestSkippedIsLogged：超出频率上限而没送检的要记成 skipped，面板上看得见。
func TestSkippedIsLogged(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	if err := b.PutBotSetting(b.BotID(), "antiad_rpm_chat", "1"); err != nil {
		t.Fatal(err)
	}
	b.Store.Write.Exec(`INSERT INTO group_members (chat_id,user_id,joined_at,first_seen,msg_count)
		VALUES (-100,42,1,1,500)`)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "一"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 2, "二"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log WHERE verdict='skipped'`); n != 1 {
		t.Errorf("未送检的应记 1 条 skipped，实际 %d", n)
	}
}

// ---- 封禁模式 ----

// TestBanModeBansInsteadOfMute：bot 级设置为封禁时，正式处置用 banChatMember；
// 复判前的临时禁言仍是禁言（封禁踢出群不可逆，不适合当先行动作）。
func TestBanModeBansInsteadOfMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))
	if err := b.PutBotSetting(b.BotID(), "antiad_ban", "1"); err != nil {
		t.Fatal(err)
	}

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万"))
	waitIdle(t, b)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Errorf("封禁模式应 banChatMember 一次，实际 %d", n)
	}
	if n := fake.CountCalls("restrictChatMember"); n != 1 {
		t.Errorf("只应有复判前那次临时禁言，实际 %d", n)
	}
	if _, action, _ := logRow(t, b); action != "deleted_banned" {
		t.Errorf("action = %q，期望 deleted_banned", action)
	}
}

// TestChatPunishOverridesBot：每群设置压过 bot 设置。
func TestChatPunishOverridesBot(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_ban", "1"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	conf := testutil.ChatConfOf(t, b, -100)
	if !snap.BanMode(conf) {
		t.Error("跟随时应读 bot 设置（封禁）")
	}
	conf.Punish = 0
	if snap.BanMode(conf) {
		t.Error("群设为禁言时应压过 bot 的封禁")
	}
	if err := b.PutBotSetting(b.BotID(), "antiad_ban", "0"); err != nil {
		t.Fatal(err)
	}
	conf.Punish = 1
	if !b.Cache.Snap().BanMode(conf) {
		t.Error("群设为封禁时应压过 bot 的禁言")
	}
}

// TestAdbHonorsBanMode：人工标记走最高档，同样服从封禁设置。
func TestAdbHonorsBanMode(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(`UPDATE bot_chats SET punish=1 WHERE chat_id=-100`); err != nil {
		t.Fatal(err)
	}
	b.Cache.Reload()
	conf := testutil.ChatConfOf(t, b, -100)

	HandleBanAdCommand(b, conf, adbMsg(-100, 1, testutil.GroupMsg(-100, 777, 10, "广告")), "")
	if fake.CountCalls("banChatMember") != 1 || fake.CountCalls("restrictChatMember") != 0 {
		t.Error("/ban 在封禁群里应封禁而不是禁言")
	}
}

// TestMuteLabel：参数是分钟——0 = 永久禁言，能整除的按天/小时渲染。
func TestMuteLabel(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{{0, "永久禁言"}, {5, "禁言 5 分钟"}, {90, "禁言 90 分钟"},
		{360, "禁言 6 小时"}, {1440, "禁言 1 天"}, {2880, "禁言 2 天"}}
	for _, c := range cases {
		if got := MuteLabel(c.in); got != c.want {
			t.Errorf("MuteLabel(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestPermanentMuteOmitsUntilDate：禁言时长配成 0 = 永久禁言。不带 until_date
// 的 restrictChatMember 就是无限期：人留在群里，但发不了言。
func TestPermanentMuteOmitsUntilDate(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}

	act := adAction{Delete: true, Mute: true, Name: "deleted_muted"}
	if note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false); note != "" {
		t.Fatalf("处置应成功，得到 %q", note)
	}
	p := fake.LastCall("restrictChatMember")
	if p == nil {
		t.Fatal("应发出 restrictChatMember")
	}
	if _, ok := p["until_date"]; ok {
		t.Errorf("永久禁言不该带 until_date，得到 %v", p["until_date"])
	}
	if perms := p["permissions"].(map[string]any); perms["can_send_messages"] != false {
		t.Error("禁言权限集应为全 false")
	}

	// 限时档仍然带 until_date：360 分钟 = 6 小时。
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "360"); err != nil {
		t.Fatal(err)
	}
	ApplyAction(b, testutil.GroupMsg(-100, 43, 8, "广告"), act, false)
	p = fake.LastCall("restrictChatMember")
	if d := untilOf(p); d < 5*3600 || d > 7*3600 {
		t.Errorf("360 分钟档的 until_date 应在 6 小时附近，得到 %d 秒", d)
	}
}

// TestEffectivePenaltiesPermanentMute：永久禁言不会自己到期，不能再靠时间窗
// 判断是否仍在限制中——历史禁言要列出来，人工解除后必须消失。
func TestEffectivePenaltiesPermanentMute(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}

	// 一条 30 天前的禁言记录：限时档会被时间窗过滤掉，永久档要列出来。
	id := logAd(b, &tg.Message{Chat: &tg.Chat{ID: -100},
		From: &tg.TGUser{ID: 555}, MessageID: 1, Text: "广告"},
		adVerdict{IsAd: true, Confidence: 0.9, Decider: "so"}, "deleted_muted", "")
	old := time.Now().Unix() - 30*86400
	if _, err := b.Store.Write.Exec(`UPDATE antiad_log SET created_at=? WHERE id=?`, old, id); err != nil {
		t.Fatal(err)
	}

	got := effectivePenalties(b.Shared, b.BotID(), 555)
	if len(got) != 1 || got[0].Action != "deleted_muted" {
		t.Fatalf("永久禁言应列出历史禁言，得到 %+v", got)
	}
	// 人工解除（LiftMute / 兑换 / 申诉撤销都会走 MarkPenaltiesLifted）。
	MarkPenaltiesLifted(b, -100, 555)
	if got := effectivePenalties(b.Shared, b.BotID(), 555); len(got) != 0 {
		t.Fatalf("已解除的处罚不该再列出，得到 %+v", got)
	}
}

// TestShortMuteOnDeletedTier：开启 antiad_short_mute 后，仅删除档要附加
// 一次 5 分钟短禁言——只删不罚的话，发广告的人删除后就能接着发。它刻意不触发
// 大模型复判（Short 不并入 Mute），否则这一档每条都要多花一次复判。
func TestShortMuteOnDeletedTier(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_short_mute", "1"); err != nil {
		t.Fatal(err)
	}
	_, llmN := fakeAIWith(t, b, soReplySev("ad", 0.95, "promo", "message", 1),
		llmReply(true, 0.95, "promo", "message"))

	// 老人（发言 50 条、100 小时前进群）高置信：矩阵给删除，不触发复判。
	// 时间基准用消息自带的 Date（1700000000）：用 time.Now() 会算出进群
	// 时间在未来，AgeHours 归零反而变回新人。
	const msgDate = int64(1700000000)
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,42,?,?,50,0,0)`,
		msgDate-100*3600, msgDate-100*3600); err != nil {
		t.Fatal(err)
	}
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万 私聊"))
	waitIdle(t, b)

	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Fatalf("应删除消息，实际 %d 次", n)
	}
	mutes := fake.Calls("restrictChatMember")
	if len(mutes) != 1 {
		for i, p := range mutes {
			t.Logf("mute[%d]: until=%v perms=%v", i, p["until_date"], p["permissions"])
		}
		_, action, reason := logRow(t, b)
		t.Logf("log: action=%q reason=%q", action, reason)
		t.Fatalf("仅删除档应附一次短禁言，实际 %d 次", len(mutes))
	}
	if d := untilOf(mutes[0]); d < 4*60 || d > 6*60 {
		t.Errorf("短禁言应约 5 分钟，得到 %d 秒", d)
	}
	if llmN.Load() != 0 {
		t.Errorf("仅删除档不该触发复判，实际 %d 次", llmN.Load())
	}
	_, action, reason := logRow(t, b)
	if action != "deleted" {
		t.Errorf("动作名应保持 deleted（短禁言不并进处罚档），得到 %q", action)
	}
	if !strings.Contains(reason, "短时禁言") {
		t.Errorf("理由里应注明短禁言:\n%s", reason)
	}

	// 关掉开关：只删不禁。
	if err := b.PutBotSetting(b.BotID(), "antiad_short_mute", "0"); err != nil {
		t.Fatal(err)
	}
	before := fake.CountCalls("restrictChatMember")
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 8, "日入过万 私聊"))
	waitIdle(t, b)
	if after := fake.CountCalls("restrictChatMember"); after != before {
		t.Errorf("开关关闭时不该禁言，多出 %d 次", after-before)
	}
}

// TestBoolVerdictMode：打开按模型结论定档后，只看模型的是/否结论，
// 不看置信度——同一条广告的置信度会波动，卡硬阈值会让它时而只删、时而
// 禁言。老人仍只删不禁。
func TestBoolVerdictMode(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_bool_verdict", "1"); err != nil {
		t.Fatal(err)
	}
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	_, llmN := fakeAIWith(t, b, soReplySev("ad", 0.88, "promo", "message", 1),
		llmReply(true, 0.82, "promo", "message"))

	// 新人：置信度只有 82%（低于置信度阈值）。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "探花 9000 一单"))
	waitIdle(t, b)

	mutes := fake.Calls("restrictChatMember")
	if len(mutes) == 0 {
		t.Fatal("bool 模式下模型判为广告就该禁言，实际一次都没禁")
	}
	last := mutes[len(mutes)-1]
	if _, has := last["until_date"]; has {
		t.Errorf("mute_hours=0 时该是永久禁言，不该带 until_date: %v", last["until_date"])
	}
	if _, action, _ := logRow(t, b); action != "deleted_muted" {
		t.Errorf("动作应为 deleted_muted，得到 %q", action)
	}
	if llmN.Load() == 0 {
		t.Error("要禁言时应过复判")
	}

	// 老人：普通广告（非高危害）仍只删不禁；高危害那条
	// 由 TestSevereAdBeatsVeteranExemption 覆盖。
	const msgDate = int64(1700000000)
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,43,?,?,50,0,0)`, msgDate-100*3600, msgDate-100*3600); err != nil {
		t.Fatal(err)
	}
	before := fake.CountCalls("restrictChatMember")
	HandleGroupMessage(b, testutil.GroupMsg(-100, 43, 8, "探花 9000 一单"))
	waitIdle(t, b)
	if after := fake.CountCalls("restrictChatMember"); after != before {
		t.Errorf("老人不该被自动禁言，多出 %d 次", after-before)
	}
	if _, action, _ := logRow(t, b); action != "deleted" {
		t.Errorf("老人动作应为 deleted，得到 %q", action)
	}
}

// TestLowConfidenceAdNotMuted：复判模型返回 is_ad=true、confidence=0、
// reason 却写着正常时，bool 模式按结论定档会删消息并永久禁言。kind=none
// 说不出广告类别，按正常走：不删也不禁。
func TestLowConfidenceAdNotMuted(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	// 最坏情况：万一禁言落地就是永久。
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	fakeAIWith(t, b, soReply("clean", 0.5, "none", "message"),
		llmReply(true, 0, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7,
		"我从几千的IP池里挑出来十个极度纯净又快的IP"))
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 0 {
		t.Errorf("0%% 置信的判定不该禁言，实际禁言 %d 次", n)
	}
	if n := fake.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("kind=none 的结论不该删消息，实际删除 %d 次", n)
	}
	if _, action, _ := logRow(t, b); action != "none" {
		t.Errorf("动作应为 none，得到 %q", action)
	}
}

// TestHashHitPunishesInBoolMode：按模型结论定档时，哈希命中不再只删不罚。
// 若保留该豁免，复判上游超时（复判失败回退到 hash 结论）时，同一条广告
// 换个号再发只会被删除而不禁言。
func TestHashHitPunishesInBoolMode(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutBotSetting(b.BotID(), "antiad_mute_minutes", "0"); err != nil {
		t.Fatal(err)
	}
	// 复判上游全部失败：判定链路要回退到别的路径，而不是整条瘫掉。
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	// 这条内容此前已判为消息级广告。
	rememberAdHash(b, "招募探花 9000一单", adVerdict{Kind: "porn_bait", Confidence: 0.88}, 1)

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "招募探花 9000一单"))
	waitIdle(t, b)

	mutes := fake.Calls("restrictChatMember")
	if len(mutes) == 0 {
		t.Fatal("bool 模式下哈希命中应禁言，实际一次都没禁")
	}
	if _, action, _ := logRow(t, b); action == "deleted" {
		t.Errorf("复判失败不该让哈希命中退回 deleted，得到 %q", action)
	}
}

// TestSevereAdBeatsVeteranExemption：高危害（色情/诈骗/赌博，或危害度达标）
// 不受老成员免禁言豁免 —— 老人免禁言是为了避免误伤普通聊天，不是给惯犯
// 留豁免。
func TestSevereAdBeatsVeteranExemption(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := b.PutBotSetting(b.BotID(), "antiad_bool_verdict", "1"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	old := func(v adVerdict) adAction { return decideAction(b, snap, false, v) }

	// 普通推广广告：老成员仍只删（免禁言这条底线不变）。
	if act := old(adVerdict{IsAd: true, Confidence: 0.9, Kind: "promo"}); act.Mute {
		t.Error("老成员的普通广告不该禁言")
	}
	// 高危害（色情，危害度 2.9）：老成员也禁言。
	if act := old(adVerdict{IsAd: true, Confidence: 0.96, Kind: "porn_bait", Severity: 2.9}); !act.Mute {
		t.Error("高危害广告老成员也该禁言")
	}
	// 复判结论没有危害度，按分类兜底（置信度够才算）。
	if act := old(adVerdict{IsAd: true, Confidence: 0.9, Kind: "scam"}); !act.Mute {
		t.Error("诈骗分类且置信度够时老成员也该禁言")
	}
	if act := old(adVerdict{IsAd: true, Confidence: 0.6, Kind: "scam"}); act.Mute {
		t.Error("置信度不够的诈骗分类不该套用高危害")
	}
	// 阈值设 0：关闭这条，回到老人只删。
	if err := b.PutBotSetting(b.BotID(), "antiad_severe_mute", "0"); err != nil {
		t.Fatal(err)
	}
	snap = b.Cache.Snap()
	if act := decideAction(b, snap, false, adVerdict{IsAd: true, Confidence: 0.96,
		Kind: "porn_bait", Severity: 2.9}); act.Mute {
		t.Error("阈值设 0 后应回到老人只删")
	}
}

// TestDeleteAlreadyGoneIsNotFailure：消息被管理员或别的 bot 先删掉时，
// deleteMessage 返回 “message to delete not found”。目标已经达成，不应
// 上报删除失败——汇总里错误的告警与补删按钮都来自它。
func TestDeleteAlreadyGoneIsNotFailure(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fake.Resp["deleteMessage"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: message to delete not found"}`

	act := adAction{Delete: true, Name: "deleted"}
	if note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false); note != "" {
		t.Fatalf("消息本就不在，不该报失败，得到 %q", note)
	}
	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("应尝试过一次删除，实际 %d 次", n)
	}
}

// TestDeleteRealFailureStillReported：消息还在但删不掉（超期、没权限）
// 是真失败，照常上报。
func TestDeleteRealFailureStillReported(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fake.Resp["deleteMessage"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: message can't be deleted"}`

	act := adAction{Delete: true, Name: "deleted"}
	note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false)
	if !strings.Contains(note, noteDeleteFailed) {
		t.Fatalf("真失败要上报，得到 %q", note)
	}
}

// TestMuteGoneByErrorText：PARTICIPANT_ID_INVALID 直接说明对方不在群里，
// 不必再查 getChatMember，也不再报 `禁言失败`。
func TestMuteGoneByErrorText(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fake.Resp["restrictChatMember"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: PARTICIPANT_ID_INVALID"}`

	act := adAction{Delete: true, Mute: true, Name: "deleted_muted"}
	note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false)
	if strings.Contains(note, noteMuteFailed) {
		t.Fatalf("人已出群，禁言失败不该上报，得到 %q", note)
	}
	if n := fake.CountCalls("getChatMember"); n != 0 {
		t.Errorf("错误文本已经说明对方不在群，不该再查成员，实际 %d 次", n)
	}
}

// TestMuteGoneByMemberLookup：错误文本没说清时再查成员状态；已退群、
// 已被封禁出群、已在禁言状态都视为无需禁言。
func TestMuteGoneByMemberLookup(t *testing.T) {
	cases := []struct {
		name, resp string
	}{
		{"已退群", `{"ok":true,"result":{"status":"left"}}`},
		{"已被封禁出群", `{"ok":true,"result":{"status":"kicked"}}`},
		{"受限记录但已不在群", `{"ok":true,"result":{"status":"restricted","is_member":false}}`},
		{"已处于禁言状态", `{"ok":true,"result":{"status":"restricted","is_member":true,"can_send_messages":false}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, fake := testutil.NewTestBot(t, 1)
			testutil.EnableAntiad(t, b, -100)
			fake.Resp["restrictChatMember"] = `{"ok":false,"error_code":400,` +
				`"description":"Bad Request: CHAT_ADMIN_REQUIRED"}`
			fake.Resp["getChatMember"] = c.resp

			act := adAction{Delete: true, Mute: true, Name: "deleted_muted"}
			note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false)
			if strings.Contains(note, noteMuteFailed) {
				t.Fatalf("无需禁言却报失败，得到 %q", note)
			}
		})
	}
}

// TestMuteRealFailureStillReported：查不到不用禁的理由时照常上报；
// TG 查询失败也按真失败处理，不能因为一次抖动把没禁上显示成已处理。
func TestMuteRealFailureStillReported(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fake.Resp["restrictChatMember"] = `{"ok":false,"error_code":400,` +
		`"description":"Bad Request: CHAT_ADMIN_REQUIRED"}`
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"member"}}`

	act := adAction{Delete: true, Mute: true, Name: "deleted_muted"}
	note := ApplyAction(b, testutil.GroupMsg(-100, 42, 7, "广告"), act, false)
	if !strings.Contains(note, noteMuteFailed) {
		t.Fatalf("真失败要上报，得到 %q", note)
	}

	// getChatMember 本身失败：没有证据说明不用禁，按失败上报。
	fake.Resp["getChatMember"] = `{"ok":false,"description":"Internal Server Error"}`
	note = ApplyAction(b, testutil.GroupMsg(-100, 43, 8, "广告"), act, false)
	if !strings.Contains(note, noteMuteFailed) {
		t.Fatalf("成员查询失败时要保守上报，得到 %q", note)
	}
}
