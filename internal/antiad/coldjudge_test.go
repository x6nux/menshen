package antiad

import (
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

// TestHandleStartPayloadNoMute 确认没被限制的人点进来会被明确告知，
// 而不是拿到一道莫名其妙的算术题。
func TestHandleStartPayloadNoMute(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)

	m := &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 555},
		Chat: &tg.Chat{ID: 555, Type: "private"}}
	if !HandleStartPayload(b, m, unbanPayload(-100)) {
		t.Fatal("解除入口的 payload 应当被接管")
	}
	if b.Captcha.Pending(555) {
		t.Error("没有待解除的限制时不该出题")
	}
	if fake.CountCalls("sendMessage") != 1 {
		t.Errorf("应当回一条说明，实际发了 %d 条", fake.CountCalls("sendMessage"))
	}

	// 不是解除入口的 payload 要原样放回去给别的处理器
	if HandleStartPayload(b, m, "somethingelse") {
		t.Error("无关的 payload 不该被接管")
	}
}

// TestHandleStartPayloadIssuesCaptcha 确认被限制的人拿到的是验证码。
func TestHandleStartPayloadIssuesCaptcha(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, "简介里写着引流链接", 88)

	m := &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 555},
		Chat: &tg.Chat{ID: 555, Type: "private"}}
	if !HandleStartPayload(b, m, unbanPayload(-100)) {
		t.Fatal("应当被接管")
	}
	if !b.Captcha.Pending(555) {
		t.Error("被限制的人应当拿到一道验证题")
	}
}

// TestRecheckDropsBioCache 锁住自助解除最容易出的那个问题。
//
// 对方刚按提示改完简介就来复核，这时缓存里还躺着一小时前的旧值。
// 不清缓存的话，他无论怎么改都过不了，而日志里一切正常。
func TestRecheckDropsBioCache(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	const stale = "加我微信 abc"
	b.BioCache.Store(int64(555), bioEntry{
		bio: stale, expire: time.Now().Add(time.Hour)})
	saveJoinMute(b, -100, 555, "简介里有联系方式", 0)

	rec, _ := loadJoinMute(b.Store, -100, 555)
	// 没配上游，judgeJoin 必然失败 —— 这条路径按「放行」处理，
	// 正好让我们在不打网络的情况下走完整段收尾逻辑。
	recheckAndLift(b, 555, -100, &tg.TGUser{ID: 555}, rec)

	if fake.CountCalls("getChat") == 0 {
		t.Error("复核时必须重新拉一次资料，不能用缓存里的旧值")
	}
	if v, ok := b.BioCache.Load(int64(555)); ok && v.(bioEntry).bio == stale {
		t.Error("缓存里仍是旧简介 —— 对方改了也读不到，怎么改都通不过")
	}
	if _, still := loadJoinMute(b.Store, -100, 555); still {
		t.Error("判定失败时应当放行并清掉限制记录")
	}
}
