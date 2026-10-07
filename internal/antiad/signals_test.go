package antiad

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestProfileHandles：只取公开用户名，去重保序、限量；私密邀请查不出内容，不取。
func TestProfileHandles(t *testing.T) {
	p := senderProfile{FirstName: "UP主 @MyChannel",
		Bio: "技术频道 t.me/MyChannel 客服 @help_bot 群 t.me/joinchat/xyz t.me/+abcdef"}
	got := strings.Join(profileHandles(p), ",")
	if got != "MyChannel,help_bot" {
		t.Errorf("profileHandles = %q", got)
	}

	var many []string
	for _, h := range []string{"aaaa1", "aaaa2", "aaaa3", "aaaa4", "aaaa5", "aaaa6"} {
		many = append(many, "@"+h)
	}
	if n := len(profileHandles(senderProfile{Bio: strings.Join(many, " ")})); n != profileLinkMax {
		t.Errorf("取了 %d 个，上限应为 %d", n, profileLinkMax)
	}
}

// TestResolveLinkKindsAndCache：频道、bot 各自识别；同一个用户名只查一次；
// 查不到的记成 unknown 并同样缓存——私有群每条消息都重查只是白撞速率限制。
func TestResolveLinkKindsAndCache(t *testing.T) {
	// bot 的简介要去公开预览页取，测试里指到一个关着的端口：立刻失败，
	// 既不访问外网也不拖慢测试。
	oldAbout := botAboutURL
	botAboutURL = "http://127.0.0.1:1/"
	t.Cleanup(func() { botAboutURL = oldAbout })

	b, fake := testutil.NewTestBot(t, 1)
	fake.Resp["getChat"] = `{"ok":true,"result":{"type":"channel","title":"Go 技术",` +
		`"description":"分享 Go 文章"}}`

	info := resolveLink(b, "GoTech")
	if info.Kind != "channel" || info.Title != "Go 技术" || info.About != "分享 Go 文章" {
		t.Errorf("频道解析 = %+v", info)
	}
	resolveLink(b, "gotech") // 大小写不同也是同一个
	if n := fake.CountCalls("getChat"); n != 1 {
		t.Errorf("同一用户名查了 %d 次", n)
	}

	fake.Resp["getChat"] = `{"ok":true,"result":{"type":"private","first_name":"客服"}}`
	if k := resolveLink(b, "shop_bot").Kind; k != "bot" {
		t.Errorf("以 bot 结尾的 private 应识别为 bot，得到 %q", k)
	}
	if k := resolveLink(b, "someone").Kind; k != "user" {
		t.Errorf("普通 private 应识别为 user，得到 %q", k)
	}

	fake.Resp["getChat"] = `{"ok":false,"description":"chat not found"}`
	if k := resolveLink(b, "gone_group").Kind; k != "unknown" {
		t.Errorf("查不到应为 unknown，得到 %q", k)
	}
	before := fake.CountCalls("getChat")
	resolveLink(b, "gone_group")
	if fake.CountCalls("getChat") != before {
		t.Error("查不到的结果也应缓存")
	}
}

// TestEnrichSenderFillsBioLinks：送检前把简介里挂的频道查清楚放进画像。
func TestEnrichSenderFillsBioLinks(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	fake.Resp["getChat"] = `{"ok":true,"result":{"type":"channel","title":"T",` +
		`"bio":"看我频道 @TechCh"}}`
	p := senderProfile{UserID: 5}
	enrichSender(b, &p)
	if p.Bio != "看我频道 @TechCh" {
		t.Errorf("bio = %q", p.Bio)
	}
	if len(p.BioLinks) != 1 || p.BioLinks[0].Handle != "TechCh" {
		t.Errorf("bio_links = %+v", p.BioLinks)
	}
}

// TestMentionedBots：正文只有一串 @xxxbot 是召唤访客 bot 代发的典型手法。
func TestMentionedBots(t *testing.T) {
	got := strings.Join(mentionedBots("看这里 @AdsBot @alice @promo_bot"), ",")
	if got != "AdsBot,promo_bot" {
		t.Errorf("mentionedBots = %q", got)
	}
}

// TestAdKindLabel：分类值来自模型输出，面板上直接显示 porn_bait 没人看得懂；
// 表外的值原样显示，不吞信息。
func TestAdKindLabel(t *testing.T) {
	for in, want := range map[string]string{
		"porn_bait": "色情内容", "scam": "诈骗", "": "未分类", "none": "未分类", "weird": "weird"} {
		if got := adKindLabel(in); got != want {
			t.Errorf("adKindLabel(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestPromptsCoverNewSignals：新加的画像字段与口径必须写进两级提示词，
// 光放进 state 模型不会自己建立关联。
func TestPromptsCoverNewSignals(t *testing.T) {
	for name, p := range map[string]string{"so": soInstructions, "llm": llmSystemPrompt} {
		for _, must := range []string{"bio_links", "porn_bait", "mentioned_bots",
			"is_edited", "is_channel", "［图片］", "［访客 bot］"} {
			if !strings.Contains(p, must) {
				t.Errorf("%s 提示词未提到 %s", name, must)
			}
		}
	}
	if !strings.Contains(llmSystemPrompt, `"scope"`) {
		t.Error("复判输出格式里缺 scope")
	}
}

// TestSystemOneReqAsksScope：删一条还是删光由 ad_scope 决定，问题缺了就永远是 message。
func TestSystemOneReqAsksScope(t *testing.T) {
	req := buildSystemOneReq(adState{}, soInstructions)
	qs := req["questions"].(map[string]any)
	if _, ok := qs["ad_scope"]; !ok {
		t.Fatal("systemone 请求缺 ad_scope 问题")
	}
	kinds := qs["ad_kind"].(map[string]any)["criteria"].(map[string]any)
	if _, ok := kinds["porn_bait"]; !ok {
		t.Error("ad_kind 缺 porn_bait")
	}
}

// TestVerdictScopeParsed：两级判定都要把 scope 带出来。
func TestVerdictScopeParsed(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fakeAIWith(t, b, soReply("ad", 0.95, "promo", "account"),
		llmReply(true, 0.9, "promo", "account"))
	snap := b.Cache.Snap()
	so, err := judgeSystemOne(b, snap, adState{}, soInstructions)
	if err != nil || so.Scope != "account" {
		t.Errorf("systemone scope = %q, err = %v", so.Scope, err)
	}
	llm, err := judgeLLM(b, snap, testAdState(), adVerdict{}, llmSystemPrompt)
	if err != nil || llm.Scope != "account" {
		t.Errorf("大模型 scope = %q, err = %v", llm.Scope, err)
	}
}

// TestBuildStateCarriesNewFields：编辑、@bot 进 message，频道身份进 sender。
func TestBuildStateCarriesNewFields(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	m := testutil.GroupMsg(-100, -1001234, 5, "@AdsBot")
	m.EditDate = 1700000100
	st := buildState(b, b.Cache.Snap(), m, buildProfile(b, m, groupMember{}, 1700000000))
	raw, _ := json.Marshal(st)
	for _, want := range []string{`"is_edited":true`, `"mentioned_bots":["AdsBot"]`, `"is_channel":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("state 缺 %s:\n%s", want, raw)
		}
	}
}

// TestMain 把「bot 简介」的抓取地址指到一个关着的端口：单元测试不该访问
// 外网，漏配的测试会立刻失败而不是挂 5 秒；需要真实预览页的测试自己覆盖
// 成 httptest 地址（见 TestResolveLinkBotAbout）。
func TestMain(m *testing.M) {
	old := botAboutURL
	botAboutURL = "http://127.0.0.1:1/"
	code := m.Run()
	botAboutURL = old
	os.Exit(code)
}
