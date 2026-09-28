package antiad

import (
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
// 抖动时会重试；余量不足的话定案前自动解禁，等于白罚一截。
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

// TestReviewCleanKeepsDeletionOnRecord：复判判为正常时不追加处置、不主动解禁，
// 流水照实记成删过（删掉的回不来）。
func TestReviewCleanKeepsDeletionOnRecord(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(false, 0.8, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 7, "日入过万"))
	waitIdle(t, b)

	if n := fake.CountCalls("restrictChatMember"); n != 1 {
		t.Errorf("只应有那次临时禁言，实际 %d 次（主动解禁会踩掉别的禁言）", n)
	}
	if verdict, action, _ := logRow(t, b); verdict != "clean" || action != "deleted" {
		t.Errorf("verdict=%q action=%q，期望 clean/deleted", verdict, action)
	}
	if gm, _ := loadMember(b.Store, -100, 42); gm.AdHits != 0 {
		t.Error("复判为正常的不该计命中")
	}
}

// TestConfidentOldMemberSkipsReview：初判有把握又不用禁言的（老成员只删）不复判。
func TestConfidentOldMemberSkipsReview(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	_, llmN := fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.9, "scam", "message"))
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
	// 连带删除只删 47 小时内的（TG 的删除期限），测试消息得是「刚发的」。
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

// ---- 禁言档改封禁 ----

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

	HandleAdbCommand(b, conf, adbMsg(-100, 1, testutil.GroupMsg(-100, 777, 10, "广告")))
	if fake.CountCalls("banChatMember") != 1 || fake.CountCalls("restrictChatMember") != 0 {
		t.Error("/ban 在封禁群里应封禁而不是禁言")
	}
}
