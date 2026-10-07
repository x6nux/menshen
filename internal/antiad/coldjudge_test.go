package antiad

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestColdSuspicious 覆盖进群预筛的两个方向。
//
// 漏报（该送检的没送）会使功能失效；误报只多花一次判定开销。因此两侧刻意
// 不对称：可疑侧要求全部命中，正常侧只过滤最常见的几类正常账号。
func TestColdSuspicious(t *testing.T) {
	cases := []struct {
		name string
		u    *tg.TGUser
		bio  string
		want bool
	}{
		{"昵称写着引流话术", &tg.TGUser{FirstName: "看喔主页 赚米"}, "", true},
		{"简介带私密群链接", &tg.TGUser{FirstName: "张三"},
			"做单进入公群有担保：https://t.me/+AOgLtfgl6sg2MDE5", true},
		{"简介留联系方式", &tg.TGUser{FirstName: "李四"}, "薇信 abc123", true},
		{"用户名带招揽用语", &tg.TGUser{Username: "riru5000_daili"}, "日入5000", true},
		// 代收代付账号：没有外链也没有词表中的联系方式词，
		// 仅靠原有判据会静默放过。
		{"代收代付账号", &tg.TGUser{FirstName: "大成代收|小小"},
			"代收代付，5个点，公示100万 USDT", true},
		{"资料里挂 @联系方式", &tg.TGUser{FirstName: "小王"}, "业务联系 @lilai", true},

		{"普通人", &tg.TGUser{FirstName: "张三", Username: "zhangsan"},
			"喜欢摄影，住在杭州", false},
		{"什么都没填", &tg.TGUser{ID: 1}, "", false},
		{"随机字符的名字", &tg.TGUser{FirstName: "x7k2m"}, "", false},
	}
	for _, c := range cases {
		got, what := coldSuspicious(c.u, c.bio)
		if got != c.want {
			t.Errorf("%s: coldSuspicious = %v, 期望 %v", c.name, got, c.want)
		}
		if got && what == "" {
			t.Errorf("%s: 命中了却说不出命中什么 —— 这句话要原样给用户看", c.name)
		}
	}
}

// TestColdSuspiciousNoUsernameIsNotASignal 单独覆盖一种情况：
// 没有用户名看似是特征，但大量正常账号也是如此。以它作判据会让预筛退化成
// 几乎人人送检，失去节省开销的意义。
func TestColdSuspiciousNoUsernameIsNotASignal(t *testing.T) {
	if ok, _ := coldSuspicious(&tg.TGUser{FirstName: "小明"}, ""); ok {
		t.Error("没有用户名不该构成怀疑理由")
	}
}

// TestColdPrefilterOffByDefault：默认不预筛，所有新人都过一次冷判定。
// 判定模型开销低，进群这道门不漏判比节省一次调用更重要。需要降低开销的
// 部署可以打开 antiad_cold_prefilter。
func TestColdPrefilterOffByDefault(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_cold", "1"); err != nil {
		t.Fatal(err)
	}
	soN, _ := fakeAIWith(t, b,
		soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))

	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: -100, Type: "supergroup"}, Date: 5000,
		OldChatMember: &tg.ChatMemberInfo{Status: "left"},
		NewChatMember: &tg.ChatMemberInfo{Status: "member",
			User: &tg.TGUser{ID: 555, FirstName: "张三", Username: "zhangsan"}},
	})
	waitIdle(t, b)
	if soN.Load() == 0 {
		t.Error("预筛默认关闭，资料平平的新人也应送检")
	}
}

// TestUnbanPayloadRoundTrip 确认冷判定记录号能原样走一圈回来。
func TestUnbanPayloadRoundTrip(t *testing.T) {
	for _, logID := range []int64{1, 42, 1001976894016} {
		got, ok := ParseUnbanPayload(unbanPayload(logID))
		if !ok || got != logID {
			t.Errorf("往返 %d 得到 (%d, %v)", logID, got, ok)
		}
	}

	// 不是这个入口的 payload 不能被认走：/start 还有别的用法。
	for _, p := range []string{"", "ub", "start", "ub0", "ub-5", "ubabc"} {
		if _, ok := ParseUnbanPayload(p); ok {
			t.Errorf("ParseUnbanPayload(%q) 不该被认作解除入口", p)
		}
	}
}

// TestNextUnbanDelay 确认退避是指数的且有封顶。
//
// 重试次数不限（未改到位的用户不应被一次失败永久挡住），因此限制开销只能
// 依靠间隔递增：每次重试都要跑一轮 AI。
func TestNextUnbanDelay(t *testing.T) {
	base := time.Minute
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, 0},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{6, 32 * time.Minute},
		{7, time.Hour}, // 封顶
		{40, time.Hour},
	}
	for _, c := range cases {
		if got := nextUnbanDelay(base, c.n); got != c.want {
			t.Errorf("nextUnbanDelay(1m, %d) = %v, 期望 %v", c.n, got, c.want)
		}
	}
}

// TestNextUnbanDelayNoOverflow 覆盖大 n 下的移位溢出。
// d = base << (n-1) 在 n 足够大时会溢出成负数，负的等待时间会使闸门失效。
func TestNextUnbanDelayNoOverflow(t *testing.T) {
	for _, n := range []int{62, 63, 64, 100, 1000} {
		if got := nextUnbanDelay(time.Minute, n); got != time.Hour {
			t.Errorf("nextUnbanDelay(1m, %d) = %v, 期望封顶 1h", n, got)
		}
	}
}

// TestUnbanGateBackoff 确认闸门按次数往后推。
func TestUnbanGateBackoff(t *testing.T) {
	_, _, sh := testutil.NewTestBotOwned(t, 1, 1)

	if ok, _ := unbanGateCheck(sh, 5); !ok {
		t.Fatal("第一次尝试不该被挡")
	}
	unbanGateBump(sh, 5)
	ok, wait := unbanGateCheck(sh, 5)
	if ok {
		t.Fatal("刚尝试过就该被挡住")
	}
	if wait <= 0 || wait > 61*time.Second {
		t.Errorf("等待时长 = %v, 期望在默认 60 秒上下", wait)
	}

	// 解除成功后清零，下次被限制时从头开始
	unbanGateClear(sh, 5)
	if ok, _ := unbanGateCheck(sh, 5); !ok {
		t.Error("清零后不该还被挡")
	}
}

// TestAppealEntryNoMute 确认没被限制的人点进来会被明确告知，
// 而不是被拉进一条走不通的申诉流程。
func TestAppealEntryNoMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)

	m := &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 555},
		Chat: &tg.Chat{ID: 555, Type: "private"}}
	if !HandleNonStaffPrivate(b, m, "/start "+unbanPayload(1)) {
		t.Fatal("进群限制的 deep link 应当被接管")
	}
	if fake.CountCalls("sendMessage") != 1 {
		t.Errorf("应当回一条说明，实际发了 %d 条", fake.CountCalls("sendMessage"))
	}

	// 不是我们发出的 payload 要原样放回去给别的处理器
	if HandleNonStaffPrivate(b, m, "/start somethingelse") {
		t.Error("无关的 payload 不该被接管")
	}
}

// TestAppealEntryListsPenalties 确认被限制的人拿到的是限制清单与申诉按钮。
func TestAppealEntryListsPenalties(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, kindProfile, "简介里写着引流链接", 88)

	m := &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 555},
		Chat: &tg.Chat{ID: 555, Type: "private"}}
	if !HandleNonStaffPrivate(b, m, "/start "+unbanPayload(1)) {
		t.Fatal("应当被接管")
	}
	last := fake.LastCall("sendMessage")
	if last == nil {
		t.Fatal("应当发一条申诉入口")
	}
	text, _ := last["text"].(string)
	if !strings.Contains(text, "申诉") || !strings.Contains(text, "进群资料审核") {
		t.Errorf("应列出有效限制:\n%s", text)
	}
	if kb := fmt.Sprint(last["reply_markup"]); !strings.Contains(kb, "a:ap:st") {
		t.Errorf("应给出「写申诉理由」按钮，得到 %s", kb)
	} else if strings.Contains(kb, "a:ap:go") {
		t.Errorf("申诉理由必填，不该再有「直接申诉」按钮，得到 %s", kb)
	}
}

// TestAppealDropsBioCache 覆盖申诉复核时的资料缓存失效。
//
// 用户刚按提示改完简介就来申诉，此时缓存中仍是改前的旧值。若不清缓存，
// 无论怎么改都会复核失败，而日志中没有任何异常。
func TestAppealDropsBioCache(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	const stale = "加我微信 abc"
	cachesOf(b.Shared).bio.Set(bioCacheKey(b, 555), bioEntry{bio: stale}, time.Hour)
	setGlobal(t, b, "antiad_llm_model", "llm-model")

	penalties := []appealPenalty{
		{Type: "join_profile", ChatID: -100, Reason: "简介里有联系方式"}}
	// 没配上游，复核必然失败 —— 正好让我们在不打网络的情况下走完资料重拉。
	if _, err := judgeAppeal(b, b.Cache.Snap(), 555, penalties, "我改了"); err == nil {
		t.Fatal("没配上游时申诉复核应当失败")
	}

	if fake.CountCalls("getChat") == 0 {
		t.Error("申诉复核时必须重新拉一次资料，不能用缓存里的旧值")
	}
	if e, ok := cachesOf(b.Shared).bio.Get(bioCacheKey(b, 555)); ok && e.bio == stale {
		t.Error("缓存里仍是旧简介 —— 对方改了也读不到，怎么改都通不过")
	}
}

// TestJoinHandledOnce 覆盖入群去重。
//
// 同一次入群 TG 会推送两条：chat_member 事件与加入群组的 service 消息，
// 两条路都汇进 onJoin。不去重会使联合封禁拦截与冷判定各跑两遍，产生两次
// AI 开销、两条群内通知与两条管理员私聊。这里借联合封禁观察。
func TestJoinHandledOnce(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := GbanAdd(b.Shared, 555, "测试", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}
	// 该群选封禁出群：联合封禁按本群配置执行，这里测的是封禁路径。
	testutil.SetChatPunish(t, b, -100, 1)

	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: -100, Type: "supergroup"}, Date: 5000,
		OldChatMember: &tg.ChatMemberInfo{Status: "left"},
		NewChatMember: &tg.ChatMemberInfo{Status: "member", User: &tg.TGUser{ID: 555}},
	})
	svc := testutil.GroupMsg(-100, 555, 41, "")
	svc.NewChatMembers = []*tg.TGUser{{ID: 555}}
	HandleGroupMessage(b, svc)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Errorf("同一次入群封了 %d 次，期望 1 次", n)
	}
}

// TestColdJudgeLogsJoinChecked：入群检查判正常也要落一条流水，否则用户页
// 对大多数新成员为空。action 用 join_checked：它不是处置，用户页的处置计数
// 与私聊汇总都要排除。
func TestColdJudgeLogsJoinChecked(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fake := b.TG.(*testutil.FakeTG)
	fake.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":9001,"type":"private",` +
				`"first_name":"普通人","username":"normalguy","bio":"喜欢摄影，住在杭州"}}`, true
		}
		return "", false
	}
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	conf := testutil.ChatConfOf(t, b, -100)
	coldJudge(b, conf, &tg.TGUser{ID: 9001, FirstName: "普通人", Username: "normalguy"})

	var verdict, action, text string
	if err := b.Store.Read.QueryRow(`SELECT verdict,action,text FROM antiad_log
		WHERE user_id=9001 ORDER BY id DESC LIMIT 1`).Scan(&verdict, &action, &text); err != nil {
		t.Fatalf("判正常也要落入群检查流水: %v", err)
	}
	if verdict != "clean" || action != "join_checked" {
		t.Errorf("verdict=%q action=%q，期望 clean/join_checked", verdict, action)
	}
	if !strings.Contains(text, "［入群资料检查］") {
		t.Errorf("正文要能看出是入群资料检查：%q", text)
	}
	// 不是处置：用户页的计数与私聊汇总都不该把它算进去。
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log WHERE user_id=9001 AND `+ProcessedCond); n != 0 {
		t.Errorf("入群检查不是处置，不该计入被处置过，得到 %d", n)
	}
	if _, _, ok := renderAdSummary(b, 0, 1<<62); ok {
		t.Error("入群检查不该触发管理员私聊汇总")
	}
}

// TestApplyJoinMuteNotifyIdempotent：已在进群类限制里的人再施加一次
// 是无操作——不再 restrict、不再发通知、不再写流水；kind 也不被改写。
func TestApplyJoinMuteNotifyIdempotent(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	saveJoinMute(b, -100, 555, kindPrewarm, "已有前置号限制", 0)
	const logs = `SELECT COUNT(*) FROM antiad_log
		WHERE chat_id=-100 AND user_id=555`
	before := countRows(t, b, logs)

	applyJoinMuteNotify(b, conf, &tg.TGUser{ID: 555, FirstName: "广告号"},
		adVerdict{IsAd: true, Confidence: 0.99, Reason: "资料广告"},
		joinMuteSpec{Kind: kindProfile, Action: actionJoinMuted,
			Note: "延迟复查发现资料广告", Body: "x", Reason: "资料广告",
			Announce: true})

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("已有进群限制不该重复禁言，得到 %d 次", got)
	}
	if got := fake.CountCalls("sendMessage"); got != 0 {
		t.Fatalf("无操作不该发群内通知，得到 %d 次", got)
	}
	if got := countRows(t, b, logs); got != before {
		t.Fatalf("无操作不该写新流水：%d -> %d", before, got)
	}
	rec, ok := loadJoinMute(b.Store, -100, 555)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("原有记录不该被改写：ok=%v kind=%q", ok, rec.Kind)
	}
}

// TestJoinMuteNoticeOneLine：群内限制通知保持一行——uid + 短结论，不贴昵称与
// 用户名；它受群内展示开关控制，发出后安排到点自动撤回。
func TestJoinMuteNoticeOneLine(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET group_alert=1 WHERE chat_id=-100`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	conf := testutil.ChatConfOf(t, b, -100)
	v := adVerdict{IsAd: true, Confidence: 0.95, Decider: "llm",
		Reason: "简介写着推广内容引流"}

	applyJoinMute(b, conf, &tg.TGUser{ID: 555, FirstName: "日入5000 看主页",
		Username: "riru_ad"}, v, "简介写着推广内容引流")
	p := fake.LastCall("sendMessage")
	if p == nil || int64(p["chat_id"].(float64)) != -100 {
		t.Fatal("开了群内展示应发一条群内通知")
	}
	text := p["text"].(string)
	for _, bad := range []string{"日入5000", "riru_ad"} {
		if strings.Contains(text, bad) {
			t.Errorf("群内通知带出了昵称/用户名:\n%s", text)
		}
	}
	if !strings.Contains(text, "tg://user?id=555") ||
		!strings.Contains(text, "广告 置信度:95%") {
		t.Errorf("通知应是一行 uid + 短结论:\n%s", text)
	}
	if strings.Contains(text, "简介写着推广内容引流") {
		t.Errorf("群内通知不该带具体理由（详情在申诉入口与记录里）:\n%s", text)
	}
	// 完整理由要留在记录里：申诉入口与管理员详情都靠它。
	var stored string
	if err := b.Store.Read.QueryRow(
		`SELECT reason FROM join_mutes WHERE chat_id=-100 AND user_id=555`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "简介写着推广内容引流") {
		t.Errorf("完整理由应留在冷判定记录里，得到 %q", stored)
	}
	// 文本链接带冷判定记录号：管理员点进来落到记录卡片，被限制的人进申诉入口。
	// 群内提示用文本链接而非内联按钮（按钮在部分客户端容易被忽略）。
	var logID int64
	if err := b.Store.Read.QueryRow(
		`SELECT id FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, fmt.Sprintf(`① <a href="https://t.me/testbot?start=log%d">点我申诉</a>`, logID)) {
		t.Errorf("通知应把申诉链接嵌在文字上（含记录号 %d）:\n%s", logID, text)
	}
	if p["reply_markup"] != nil {
		t.Errorf("群内通知不该再挂内联按钮，得到 %v", p["reply_markup"])
	}
	var cleanup int
	if err := b.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM alert_cleanup`).Scan(&cleanup); err != nil || cleanup != 1 {
		t.Errorf("通知应安排自动撤回（err=%v, n=%d）", err, cleanup)
	}

	// 关掉群内展示：禁言照常执行，群里一个字都不发。
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET group_alert=0 WHERE chat_id=-100`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	conf = testutil.ChatConfOf(t, b, -100)
	n := fake.CountCalls("sendMessage")
	applyJoinMute(b, conf, &tg.TGUser{ID: 557}, v, "x")
	if fake.CountCalls("sendMessage") != n {
		t.Error("群内展示关闭时不应发群内通知")
	}
}

// 进群冷判定同样先跑资料规则门：enforce 命中零 AI 直接禁言，流水带
// rule:<id>，kind 仍是 profile。
func TestColdJudgeProfileEnforceRuleZeroAI(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	ruleID := insertRule(t, b, "资料广告_冷判定", `免押小额洗资`, "promo", true, true)
	soN, llmN := fakeAIWith(t, b,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":681,"first_name":"张三",` +
				`"bio":"免押小额洗资 私聊"}}`, true
		}
		return "", false
	}
	conf := testutil.ChatConfOf(t, b, -100)

	coldJudge(b, conf, &tg.TGUser{ID: 681, FirstName: "张三"})

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("enforce 命中不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("enforce 命中应禁言 1 次，得到 %d", got)
	}
	if _, ok := loadJoinMute(b.Store, -100, 681); !ok {
		t.Fatal("应写 join_mutes")
	}
	var decider, action string
	if err := b.Store.Read.QueryRow(`SELECT decider,action FROM antiad_log
		WHERE chat_id=-100 AND user_id=681 ORDER BY id DESC LIMIT 1`).
		Scan(&decider, &action); err != nil {
		t.Fatal(err)
	}
	if decider != fmt.Sprintf("rule:%d", ruleID) || action != actionJoinMuted {
		t.Fatalf("decider/action = %q/%q，期望 rule:%d/join_muted",
			decider, action, ruleID)
	}
}

// v.Kind 为空时形状表退回限制类型：绝不能落一个空类别，phash 流水
// 至少显示 profile/prewarm 而不是空白。
func TestProfileShapeLearnKindFallback(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	shape := profileShape(senderProfile{Bio: "免押小额洗资 https://t.me/+ccc"})
	applyJoinMuteNotify(b, conf, &tg.TGUser{ID: 558},
		adVerdict{IsAd: true, Confidence: 0.9, Reason: "x"}, // Kind 为空
		joinMuteSpec{Kind: kindProfile, Action: actionJoinMuted, Note: "n",
			Body: "b", Reason: "x", Shape: shape})

	rec, ok := lookupProfileShape(b.Store, shape)
	if !ok || rec.Kind != kindProfile {
		t.Fatalf("v.Kind 为空应退回限制类型 %q：ok=%v rec=%+v",
			kindProfile, ok, rec)
	}
}

// 批量（探测）禁言 Quiet：不发群内通知，但流水与 join_mutes 照常，
// 私聊汇总照常包含；非 Quiet 通知受按群 5 秒限速。
func TestApplyJoinMuteQuietAndNoticePacing(t *testing.T) {
	b, fake, _ := testutil.NewTestBotOwned(t, 1, 100)
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(
		`UPDATE bot_chats SET group_alert=1 WHERE chat_id=-100`); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	conf := testutil.ChatConfOf(t, b, -100)
	v := adVerdict{IsAd: true, Confidence: 0.99, Kind: "promo", Reason: "批量模板"}
	FlushAdSummary(b, time.Now()) // 初始化汇总游标

	// Quiet：只落流水/限制，不发群通知。
	applyJoinMuteNotify(b, conf, &tg.TGUser{ID: 555}, v, joinMuteSpec{
		Kind: kindProfile, Action: actionJoinMuted, Note: "延迟复查发现资料广告",
		Body: "x", Reason: "账号资料中含有推广或引流内容",
		Announce: true, Quiet: true,
	})
	if got := fake.CountCalls("sendMessage"); got != 0 {
		t.Fatalf("Quiet 批量禁言不该发群内通知，得到 %d 条", got)
	}
	if _, ok := loadJoinMute(b.Store, -100, 555); !ok {
		t.Fatal("Quiet 禁言也要写 join_mutes")
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log
		WHERE chat_id=-100 AND user_id=555 AND action='join_muted'`); n != 1 {
		t.Fatalf("Quiet 禁言应落一条 join_muted 流水，得到 %d", n)
	}
	FlushAdSummary(b, time.Now())
	if got := dmCount(fake, 100); got != 1 {
		t.Fatalf("批量禁言应进私聊汇总，得到 %d 条私信", got)
	}

	// 非 Quiet 通知按群 5 秒限速：紧接着两条只发一条群通知。
	fake.Reset()
	for _, uid := range []int64{556, 557} {
		applyJoinMuteNotify(b, conf, &tg.TGUser{ID: uid}, v, joinMuteSpec{
			Kind: kindProfile, Action: actionJoinMuted, Note: "进群冷判定",
			Body: "y", Reason: "账号资料中含有推广或引流内容", Announce: true})
	}
	if got := fake.CountCalls("sendMessage"); got != 1 {
		t.Fatalf("同群 5 秒内只应发一条通知，得到 %d", got)
	}
}
