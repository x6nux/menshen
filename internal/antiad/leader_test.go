package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// TestLeaderHit：领导人姓名的几种写法都要命中，同时不误伤普通人名。
func TestLeaderHit(t *testing.T) {
	hits := []string{
		"习近平", "Xi Jinping", "xi-jinping", "xijinping3616", "习 近 平",
		"習近平", "习大大", "习主席", "我们的习总书记",
		"李强总理", "李克强", "毛澤東", "鄧小平", "彭丽媛",
	}
	for _, s := range hits {
		if leaderHit(s) == "" {
			t.Errorf("%q 应命中领导人姓名", s)
		}
	}
	miss := []string{
		"",
		"今天天气不错",
		"李强",      // 普通人名：裸名不命中，只有带职务才算
		"李希",      // 同上
		"jinping", // 只有名：可能是普通人
		"我喜欢吃包子",
		"这广告高明啊",
	}
	for _, s := range miss {
		if h := leaderHit(s); h != "" {
			t.Errorf("%q 不该命中，得到 %q", s, h)
		}
	}
}

// TestLeaderGateBansImpersonator：昵称/用户名里出现领导人姓名时直接封禁
// 出群（删消息 + banChatMember + 落流水），不送检、不花 AI 的钱；
// 正文里只是提到不算（管理员口径「提到不管，只查资料」）。
func TestLeaderGateBansImpersonator(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	_, llmN := fakeAIWith(t, b, `{}`, `{}`)

	// 昵称就是领导人姓名。
	m := testutil.GroupMsg(-100, 8879208542, 7, "我的子民们")
	m.From.FirstName = "习近平"
	m.From.Username = "xijinping3616"
	HandleGroupMessage(b, m)
	waitIdle(t, b)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Fatalf("应封禁出群一次，得到 %d 次", n)
	}
	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("应删掉这条消息，得到 %d 次", n)
	}
	if llmN.Load() != 0 {
		t.Errorf("硬规则不该送检，复判却跑了 %d 次", llmN.Load())
	}
	var action, reason, kind string
	if err := b.Store.Read.QueryRow(`SELECT action,reason,ad_kind FROM antiad_log
		WHERE user_id=8879208542`).Scan(&action, &reason, &kind); err != nil {
		t.Fatal(err)
	}
	if action != "deleted_banned" || kind != "impersonate" ||
		!strings.Contains(reason, "冒用国家领导人") {
		t.Errorf("action=%q kind=%q reason=%q", action, kind, reason)
	}

	// 用户名里带着拼写（昵称正常）：照样封。
	b2, fake2 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	fakeAIWith(t, b2, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	m2 := testutil.GroupMsg(-100, 7002, 8, "朕刚和美国总统谈完回来")
	m2.From.Username = "xijinping3616"
	HandleGroupMessage(b2, m2)
	waitIdle(t, b2)
	if n := fake2.CountCalls("banChatMember"); n != 1 {
		t.Errorf("用户名冒用也该封禁，得到 %d 次", n)
	}

	// 正文里只是「提到」姓名：不封禁（管理员口径「提到不管，只查资料」），
	// 照常走判定链路。
	b3, fake3 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b3, -100)
	fakeAIWith(t, b3, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	HandleGroupMessage(b3, testutil.GroupMsg(-100, 7003, 9, "给你们看看习近平的讲话"))
	waitIdle(t, b3)
	if n := fake3.CountCalls("banChatMember"); n != 0 {
		t.Errorf("正文提到姓名不该封禁，得到 %d 次", n)
	}
	if n := fake3.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("正文提到姓名不该删消息，得到 %d 次", n)
	}

	// 普通人不受影响：照常走判定。
	b4, fake4 := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b4, -100)
	fakeAIWith(t, b4, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message")) // 默认两级都判广告
	HandleGroupMessage(b4, testutil.GroupMsg(-100, 7004, 10, "这广告高明啊"))
	waitIdle(t, b4)
	if n := fake4.CountCalls("banChatMember"); n != 0 {
		t.Errorf("普通人名不该触发封禁，得到 %d 次", n)
	}
}

// TestLeaderGateSkipsExempt：管理员与白名单不受这条硬规则影响 —— 豁免在
// 规则之前，机器人不能因为管理员聊到政治就把自己的管理员踢出去。
func TestLeaderGateSkipsExempt(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	// 1 是这个测试实例的管理员/归属人（见 testutil.NewTestBot(t, 1)）。
	m := testutil.GroupMsg(-100, 1, 11, "习近平")
	HandleGroupMessage(b, m)
	waitIdle(t, b)
	if n := fake.CountCalls("banChatMember"); n != 0 {
		t.Errorf("管理员不该被这条规则踢出群，得到 %d 次", n)
	}
	if n := fake.CountCalls("deleteMessage"); n != 0 {
		t.Errorf("豁免者的消息不该被删，得到 %d 次", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log WHERE user_id=1`); n != 0 {
		t.Errorf("豁免者连流水都不该落，得到 %d 条", n)
	}
}

// TestLeaderBanRespectsDryrun：演练群只落流水（dryrun:deleted_banned），
// 不动手也不发群内告警。
func TestLeaderBanRespectsDryrun(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"), llmReply(false, 0.9, "none", "message"))
	m := testutil.GroupMsg(-100, 7005, 12, "我的子民们")
	m.From.Username = "xijinping"
	HandleGroupMessage(b, m)
	waitIdle(t, b)

	if n := fake.CountCalls("banChatMember"); n != 0 {
		t.Errorf("演练群不该封禁，得到 %d 次", n)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE user_id=7005`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "dryrun:deleted_banned" {
		t.Errorf("演练应记 dryrun:deleted_banned，得到 %q", action)
	}
}

// TestLeaderProfileHit：资料检查分字段给出位置说明。
func TestLeaderProfileHit(t *testing.T) {
	if _, where := leaderProfileHit(&tg.TGUser{FirstName: "习近平"}, ""); where != "昵称" {
		t.Errorf("昵称命中应报「昵称」，得到 %q", where)
	}
	if _, where := leaderProfileHit(&tg.TGUser{Username: "xjp_xijinping"}, ""); where != "用户名" {
		t.Errorf("用户名命中应报「用户名」，得到 %q", where)
	}
	if _, where := leaderProfileHit(&tg.TGUser{FirstName: "张三"}, "我的偶像邓小平"); where != "简介" {
		t.Errorf("简介命中应报「简介」，得到 %q", where)
	}
	if h, _ := leaderProfileHit(&tg.TGUser{FirstName: "张三", Username: "zhangsan"}, "喜欢摄影"); h != "" {
		t.Error("普通人资料不该命中")
	}
}

// TestColdJudgeBansLeaderProfile：进群这一步就拦下冒用者 —— 资料里带着
// 领导人姓名时直接封禁出群，不跑判定模型（零开销）。
func TestColdJudgeBansLeaderProfile(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("clean", 0.9, "none", "account"),
		llmReply(false, 0.9, "none", "account"))
	fake.Resp["getChat"] = `{"ok":true,"result":{"first_name":"习近平","username":"xijinping3616"}}`

	u := &tg.TGUser{ID: 8879208542, FirstName: "习近平", Username: "xijinping3616"}
	coldJudge(b, testutil.ChatConfOf(t, b, -100), u)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Fatalf("进群就该封禁出群，得到 %d 次", n)
	}
	if soN.Load()+llmN.Load() != 0 {
		t.Errorf("硬规则不该送检，跑了 %d+%d 次", soN.Load(), llmN.Load())
	}
	var action, kind string
	if err := b.Store.Read.QueryRow(`SELECT action,ad_kind FROM antiad_log
		WHERE user_id=8879208542`).Scan(&action, &kind); err != nil {
		t.Fatal(err)
	}
	if action != "banned" || kind != "impersonate" {
		t.Errorf("action=%q kind=%q，期望 banned/impersonate", action, kind)
	}
}

// TestCheckPathBansLeaderProfile：管理员复查一个冒用者时不该被模型带偏 ——
// /check <uid> 没有消息可依托，资料从 getChat 补（昵称/用户名/简介）。
func TestCheckPathBansLeaderProfile(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	fake.Resp["getChat"] = `{"ok":true,"result":{"first_name":"习近平","username":"xijinping3616"}}`
	// 复查要求本人有留底。
	recordMessage(b, -100, 231717, 8879208542, "我的子民们", 1700000000, "")

	HandleGroupMessage(b, testutil.GroupMsg(-100, 1, 9, "/check 8879208542"))
	waitIdle(t, b)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Errorf("复查也该封禁冒用者，得到 %d 次", n)
	}
	var action, kind string
	if err := b.Store.Read.QueryRow(`SELECT action,ad_kind FROM antiad_log
		WHERE user_id=8879208542 ORDER BY id DESC LIMIT 1`).Scan(&action, &kind); err != nil {
		t.Fatal(err)
	}
	if action != "banned" || kind != "impersonate" {
		t.Errorf("action=%q kind=%q，期望 banned/impersonate", action, kind)
	}
}
