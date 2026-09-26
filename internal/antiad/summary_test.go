package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// hit 直接落一条命中流水，返回 ID。
func hit(t *testing.T, b *core.Bot, uid int64, text string) int64 {
	t.Helper()
	return logAd(b, testutil.GroupMsg(-100, uid, 1, text),
		adVerdict{IsAd: true, Confidence: 0.95, Kind: "scam", Decider: "systemone"},
		"deleted_muted", "")
}

// dmCount 数发给某人的私聊条数。
func dmCount(fake *testutil.FakeTG, to int64) int {
	n := 0
	for _, p := range fake.Calls("sendMessage") {
		if int64(p["chat_id"].(float64)) == to {
			n++
		}
	}
	return n
}

// TestSummaryFirstRunOnlyMarksCursor：升级后第一次运行只记位置，不翻旧账。
func TestSummaryFirstRunOnlyMarksCursor(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	hit(t, b, 42, "旧广告")
	FlushAdSummary(b, time.Now())
	if dmCount(fake, 100) != 0 {
		t.Error("第一次运行不该把历史命中翻出来发")
	}
}

// TestSummaryBatchesAndThrottles：一段时间内的命中汇成一条私聊给归属人，
// 不带原文；距上次发出不足间隔就先攒着。
func TestSummaryBatchesAndThrottles(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	now := time.Now()
	FlushAdSummary(b, now) // 初始化游标

	id1 := hit(t, b, 42, "加微信日入过万")
	hit(t, b, 43, "另一条广告")
	FlushAdSummary(b, now)
	if dmCount(fake, 100) != 1 {
		t.Fatalf("两条命中应汇成一条私聊，实际 %d 条", dmCount(fake, 100))
	}
	text := fake.LastCall("sendMessage")["text"].(string)
	if !strings.Contains(text, "命中 2") || !strings.Contains(text, "#"+itoa(id1)) {
		t.Errorf("汇总内容不对:\n%s", text)
	}
	if strings.Contains(text, "加微信") {
		t.Errorf("汇总不该带原文:\n%s", text)
	}

	hit(t, b, 44, "第三条")
	FlushAdSummary(b, now.Add(time.Minute))
	if dmCount(fake, 100) != 1 {
		t.Error("间隔未到不该再发")
	}
	FlushAdSummary(b, now.Add(6*time.Minute))
	if dmCount(fake, 100) != 2 {
		t.Error("间隔到了应把攒下的发出去")
	}
}

// TestSummarySkipsQuietAndHonorsSwitch：只有正常判定与未送检的不打扰；
// 私聊关掉时游标照走，再打开不补发。
func TestSummarySkipsQuietAndHonorsSwitch(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	FlushAdSummary(b, time.Now())

	logAd(b, testutil.GroupMsg(-100, 42, 1, "正常"), adVerdict{Decider: "systemone"}, "none", "")
	logAd(b, testutil.GroupMsg(-100, 42, 2, "x"), adVerdict{Decider: adDeciderSkipped}, "none", "")
	FlushAdSummary(b, time.Now())
	if dmCount(fake, 100) != 0 {
		t.Error("只有正常判定与未送检时不该发")
	}

	if err := b.PutBotSetting(b.BotID(), "antiad_dm_admins", "0"); err != nil {
		t.Fatal(err)
	}
	hit(t, b, 42, "广告")
	FlushAdSummary(b, time.Now())
	if err := b.PutBotSetting(b.BotID(), "antiad_dm_admins", "1"); err != nil {
		t.Fatal(err)
	}
	FlushAdSummary(b, time.Now().Add(time.Hour))
	if dmCount(fake, 100) != 0 {
		t.Error("私聊关着时的命中，重新打开后不该补发")
	}
}

// TestSummaryIsPerBot：只汇总本 bot 的流水——次级管理员不该看到别人的命中。
func TestSummaryIsPerBot(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	FlushAdSummary(b, time.Now())
	id := hit(t, b, 4242, "别人 bot 的命中")
	b.Store.Write.Exec(`UPDATE antiad_log SET bot_id=999 WHERE id=?`, id)
	FlushAdSummary(b, time.Now())
	if dmCount(fake, 100) != 0 {
		t.Error("区间里只有别的 bot 的命中时不该发")
	}

	// 本 bot 的命中与别人的交错在同一个 ID 区间里
	id = hit(t, b, 4343, "别人 bot 的又一条")
	b.Store.Write.Exec(`UPDATE antiad_log SET bot_id=999 WHERE id=?`, id)
	hit(t, b, 42, "本 bot 的命中")
	FlushAdSummary(b, time.Now())
	text := fake.LastCall("sendMessage")["text"].(string)
	if !strings.Contains(text, "命中 1 ") || strings.Contains(text, "4343") {
		t.Errorf("汇总混进了别的 bot 的命中:\n%s", text)
	}
}

// TestAlertCleanupSurvivesInDB：待撤回告警记在库里，到点由 sweep 撤；
// 进程重启（内存计时器丢失）也不会让告警永久留在群里。
func TestAlertCleanupSurvivesInDB(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	scheduleAlertCleanup(b, -100, 55, time.Minute)
	SweepAlertCleanup(b, time.Now())
	if fake.CountCalls("deleteMessage") != 0 {
		t.Error("没到点不该撤")
	}
	SweepAlertCleanup(b, time.Now().Add(2*time.Minute))
	p := fake.LastCall("deleteMessage")
	if p == nil || int64(p["message_id"].(float64)) != 55 {
		t.Fatalf("到点应撤回 55: %v", p)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM alert_cleanup`); n != 0 {
		t.Errorf("撤过的应从表里删掉，剩 %d", n)
	}
}

// TestRenderAdRecord：记录卡片给管理员私聊看，处置按钮按实际动作给。
func TestRenderAdRecord(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	id := logAd(b, testutil.GroupMsg(-100, 42, 7, "广告原文"),
		adVerdict{IsAd: true, Confidence: 0.9, Kind: "porn_bait", Decider: "systemone", Reason: "理由"},
		"dryrun:deleted_banned", "")
	row, ok := LoadAdLog(b.Store, id)
	if !ok || row.BotID != b.BotID() {
		t.Fatalf("LoadAdLog: ok=%v bot=%d", ok, row.BotID)
	}
	text, kb := RenderAdRecord(b, row)
	for _, want := range []string{"#" + itoa(id), "色情内容", "演练", "广告原文"} {
		if !strings.Contains(text, want) {
			t.Errorf("卡片缺 %q:\n%s", want, text)
		}
	}
	if kb == nil {
		t.Error("卡片应带处置按钮")
	}
}

// TestActionLabel 覆盖完整词汇表。漏掉任何一个值，面板会直接显示原始字符串。
func TestActionLabel(t *testing.T) {
	cases := map[string]string{
		"none": "未处置", "alerted": "仅告警", "deleted": "已删除",
		"muted": "已禁言", "deleted_muted": "已删除+禁言", "deleted_banned": "已删除+封禁",
		"banned": "已封禁", "join_muted": "进群限制发言", "undone": "已标记误判",
		"dryrun:deleted_muted": "演练（本应已删除+禁言）",
	}
	for in, want := range cases {
		if got := ActionLabel(in); got != want {
			t.Errorf("ActionLabel(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
