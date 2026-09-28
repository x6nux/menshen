package antiad

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestColdSuspicious 守进群预筛的两个方向。
//
// 漏报（该送检的没送）直接等于功能不存在；误报只是多花一次判定的钱，
// 所以这里刻意不对称：可疑侧要全中，正常侧只挡住最常见的几类普通人。
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

// TestColdSuspiciousNoUsernameIsNotASignal 单独拎出来：
// 「没有用户名」看着像特征，其实大量正常人就是这样。拿它当判据会让预筛
// 退化成「几乎人人都送检」，省钱这个唯一目的直接落空。
func TestColdSuspiciousNoUsernameIsNotASignal(t *testing.T) {
	if ok, _ := coldSuspicious(&tg.TGUser{FirstName: "小明"}, ""); ok {
		t.Error("没有用户名不该构成怀疑理由")
	}
}

// TestUnbanPayloadRoundTrip 确认群号能原样走一圈回来。
func TestUnbanPayloadRoundTrip(t *testing.T) {
	for _, chatID := range []int64{-100, -1001976894016} {
		got, ok := parseUnbanPayload(unbanPayload(chatID))
		if !ok || got != chatID {
			t.Errorf("往返 %d 得到 (%d, %v)", chatID, got, ok)
		}
	}

	// 不是解除入口的 payload 不能被认走：/start 还有别的用法。
	for _, p := range []string{"", "ub", "start", "ub0", "ub-5", "ubabc"} {
		if _, ok := parseUnbanPayload(p); ok {
			t.Errorf("parseUnbanPayload(%q) 不该被认作解除入口", p)
		}
	}
}

// TestNextUnbanDelay 确认退避是指数的且有封顶。
//
// 不限次数是刻意的（改简介改不到位的普通人不该被一次失败挡死），
// 那么拦住「磨开销」就只能靠间隔递增：每次重试都要跑一轮 AI。
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

// TestNextUnbanDelayNoOverflow 守住大 n 下的移位溢出。
// d = base << (n-1) 在 n 大到一定程度会翻成负数，负的等待时间意味着
// 闸门形同虚设——恰好是攻击者最想要的那一端。
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
	if !HandleNonStaffPrivate(b, m, "/start "+unbanPayload(-100)) {
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
	saveJoinMute(b, -100, 555, "简介里写着引流链接", 88)

	m := &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 555},
		Chat: &tg.Chat{ID: 555, Type: "private"}}
	if !HandleNonStaffPrivate(b, m, "/start "+unbanPayload(-100)) {
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
	if kb := fmt.Sprint(last["reply_markup"]); !strings.Contains(kb, "a:ap:st") ||
		!strings.Contains(kb, "a:ap:go") {
		t.Errorf("应给出「写理由 / 直接申诉」两个按钮，得到 %s", kb)
	}
}

// TestAppealDropsBioCache 锁住申诉复核最容易出的那个问题。
//
// 对方刚按提示改完简介就来申诉，这时缓存里还躺着一小时前的旧值。
// 不清缓存的话，他无论怎么改都过不了，而日志里一切正常。
func TestAppealDropsBioCache(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	const stale = "加我微信 abc"
	b.BioCache.Store(int64(555), bioEntry{
		bio: stale, expire: time.Now().Add(time.Hour)})
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
	if v, ok := b.BioCache.Load(int64(555)); ok && v.(bioEntry).bio == stale {
		t.Error("缓存里仍是旧简介 —— 对方改了也读不到，怎么改都通不过")
	}
}

// TestJoinHandledOnce 锁住入群去重。
//
// 同一次入群 TG 会推两份：chat_member 事件与「XXX 加入群组」服务消息，
// 两条路都汇进 onJoin。不去重的话联合封禁拦截、冷判定都各跑两遍——
// 两次 AI 开销、群里两条通知、管理员两条私聊。这里借联合封禁观察。
func TestJoinHandledOnce(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := GbanAdd(b.Shared, 555, "测试", -100, testutil.TestBotID); err != nil {
		t.Fatal(err)
	}
	if err := b.PutSetting("gban_enabled", "1"); err != nil {
		t.Fatal(err)
	}

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

// TestJoinMuteNoticeHidesNameAndReason：群里那条限制通知不写昵称也不写理由。
// 两者常常就是广告本身（昵称里的引流话术、理由里引述的简介链接），bot 把
// 它们发进群等于替广告号再发一遍。理由在私聊的自助解除流程里给本人看。
func TestJoinMuteNoticeHidesNameAndReason(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	conf := testutil.ChatConfOf(t, b, -100)
	v := adVerdict{IsAd: true, Confidence: 0.95, Decider: "llm",
		Reason: "简介写着 t.me/+abc 引流"}

	applyJoinMute(b, conf, &tg.TGUser{ID: 555, FirstName: "日入5000 看主页",
		Username: "riru_ad"}, v, "简介写着 t.me/+abc 引流")
	text := fake.LastCall("sendMessage")["text"].(string)
	for _, bad := range []string{"日入5000", "riru_ad", "t.me/+abc"} {
		if strings.Contains(text, bad) {
			t.Errorf("群内通知带出了 %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "tg://user?id=555") || !strings.Contains(text, "私聊") {
		t.Errorf("通知应给出用户链接并指向私聊里的解除流程:\n%s", text)
	}

	// 拿不到自己的用户名就没有 deep link 按钮，不能叫人去点一个不存在的按钮。
	b.Username = ""
	applyJoinMute(b, conf, &tg.TGUser{ID: 556}, v, "")
	if text := fake.LastCall("sendMessage")["text"].(string); !strings.Contains(text, "联系群管理员") {
		t.Errorf("没有解除按钮时应指向群管理员:\n%s", text)
	}
}
