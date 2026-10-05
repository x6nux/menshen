package antiad

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func TestProfileEmpty(t *testing.T) {
	cases := []struct {
		name string
		p    senderProfile
		want bool
	}{
		{"全空", senderProfile{}, true},
		{"只有空白名字", senderProfile{FirstName: "  "}, true},
		{"有简介", senderProfile{Bio: "hello"}, false},
		{"有用户名", senderProfile{Username: "abc"}, false},
		{"有名字", senderProfile{FirstName: "小明"}, false},
	}
	for _, c := range cases {
		if got := c.p.ProfileEmpty(); got != c.want {
			t.Errorf("%s: ProfileEmpty = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestPrewarmStateFields(t *testing.T) {
	zero := 0
	p := senderProfile{Photos: &zero, PhotoKnown: true}
	if !p.PhotoKnown || p.Photos == nil || *p.Photos != 0 {
		t.Fatal("Photos/PhotoKnown 应可表达「查到了，0 张」")
	}
	st := adState{JoinCheck: false, PrewarmCheck: true}
	if !st.PrewarmCheck {
		t.Fatal("PrewarmCheck 应为真")
	}
}

func TestPrewarmCandidate(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	now := time.Now().Unix()
	gm := groupMember{Known: true, MsgCount: 1, JoinedAt: now - 3600}
	msg := func(text string) *tg.Message {
		return &tg.Message{From: &tg.TGUser{ID: 555}, Text: text}
	}
	if !prewarmCandidate(b, snap, gm, msg("哈喽"), false) {
		t.Error("新成员首条短招呼应命中")
	}
	if !prewarmCandidate(b, snap, gm, msg("擦"), false) {
		t.Error("无意义短词也应命中（判定交给 AI）")
	}
	if prewarmCandidate(b, snap, gm, msg("麻烦问下这个怎么配置"), false) {
		t.Error("长消息不该命中")
	}
	if prewarmCandidate(b, snap, gm, msg("哈喽"), true) {
		t.Error("编辑过的消息不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 2, JoinedAt: now - 3600},
		msg("哈喽"), false) {
		t.Error("非首条消息不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 1}, msg("哈喽"), false) {
		t.Error("入群时间未知不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 1,
		JoinedAt: now - int64(prewarmJoinWindow/time.Second) - 60},
		msg("哈喽"), false) {
		t.Error("超出进群窗口不该命中")
	}
	withLink := msg("哈喽")
	withLink.Entities = []tg.MessageEntity{{Type: "url"}}
	if prewarmCandidate(b, snap, gm, withLink, false) {
		t.Error("带链接的消息不该命中")
	}

	// 以下必须交回普通判定（识图/引用口径在那里），不能在候选门里被吞掉。
	captionOnly := msg("")
	captionOnly.Caption = "哈喽"
	if prewarmCandidate(b, snap, gm, captionOnly, false) {
		t.Error("纯配文（无正文）不该命中")
	}
	reply := msg("哈喽")
	reply.ReplyToMessage = &tg.Message{MessageID: 2}
	if prewarmCandidate(b, snap, gm, reply, false) {
		t.Error("回复消息不该命中")
	}
	external := msg("哈喽")
	external.ExternalReply = &tg.ExternalReplyInfo{}
	if prewarmCandidate(b, snap, gm, external, false) {
		t.Error("跨聊天引用消息不该命中")
	}
	quoted := msg("哈喽")
	quoted.Quote = &tg.TextQuote{}
	if prewarmCandidate(b, snap, gm, quoted, false) {
		t.Error("带手动引文的消息不该命中")
	}
	forwarded := msg("哈喽")
	forwarded.ForwardOrigin = []byte(`{"type":"user"}`)
	if prewarmCandidate(b, snap, gm, forwarded, false) {
		t.Error("转发消息不该命中")
	}
	// 载荷藏在按钮/联系人卡片里的短招呼：msgText 会把载荷计入长度，把
	// 消息撑出候选线，交回普通判定。
	withButton := &tg.Message{From: &tg.TGUser{ID: 555}}
	if err := json.Unmarshal([]byte(`{"message_id":10,"text":"哈喽",`+
		`"reply_markup":{"inline_keyboard":[[{"text":"点这里",`+
		`"url":"https://t.me/adchannel"}]]}}`), withButton); err != nil {
		t.Fatal(err)
	}
	if prewarmCandidate(b, snap, gm, withButton, false) {
		t.Error("短招呼带内联按钮载荷不该命中")
	}
	withContact := msg("哈喽")
	withContact.MsgPayload = tg.MsgPayload{Contact: &tg.Contact{
		FirstName: "客服", PhoneNumber: "13800138000"}}
	if prewarmCandidate(b, snap, gm, withContact, false) {
		t.Error("短招呼带联系人卡片载荷不该命中")
	}
	// bot 不走账号级无限期禁言（与 coldJudge 一致）；普通路径按
	// antiad_judge_bots 的配置照常判它。
	fromBot := msg("哈喽")
	fromBot.From.IsBot = true
	if prewarmCandidate(b, snap, gm, fromBot, false) {
		t.Error("bot 消息不该命中")
	}

	// 开关关闭：一律不圈。
	b2, _ := testutil.NewTestBot(t, 2)
	testutil.EnableAntiad(t, b2, -100)
	if prewarmCandidate(b2, b2.Cache.Snap(), gm, msg("哈喽"), false) {
		t.Error("开关关闭时不该命中")
	}
}

// setupPrewarm 建好群、开关与假上游，并造一条「新成员首条招呼」。
func setupPrewarm(t *testing.T, so, llm string) (*core.Bot, *testutil.FakeTG, int64, int64) {
	t.Helper()
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	for k, v := range map[string]string{"antiad_prewarm": "1", "antiad_cold_conf": "85"} {
		if err := b.PutSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getUserProfilePhotos"] = `{"ok":true,"result":{"total_count":0,"photos":[]}}`
	if so != "" || llm != "" {
		fakeAIWith(t, b, so, llm)
	}
	return b, fake, 555, -100
}

func sendPrewarmMessage(t *testing.T, b *core.Bot, uid, chatID int64) {
	t.Helper()
	recordJoin(b, chatID, uid, time.Now().Unix()-3600)
	HandleGroupMessage(b, testutil.GroupMsg(chatID, uid, 1, "哈喽"))
	waitIdle(t, b)
}

// 命中：删招呼 + 无限期禁言 + kind=prewarm + 流水 prewarm_muted。
func TestPrewarmHitMutes(t *testing.T) {
	b, fake, uid, chat := setupPrewarm(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	sendPrewarmMessage(t, b, uid, chat)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("应禁言 1 次，得到 %d", got)
	}
	if got := fake.CountCalls("deleteMessage"); got == 0 {
		t.Fatal("应删除招呼消息")
	}
	rec, ok := loadJoinMute(b.Store, chat, uid)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("join_mutes = (%v,%q)，期望存在且 kind=prewarm", ok, rec.Kind)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != actionPrewarmMuted {
		t.Fatalf("action = %q，期望 %q", action, actionPrewarmMuted)
	}
}

// 未命中：只落 prewarm_checked，不禁言。
func TestPrewarmCleanOnlyLogs(t *testing.T) {
	b, fake, uid, chat := setupPrewarm(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	sendPrewarmMessage(t, b, uid, chat)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("正常用户不该禁言，得到 %d 次", got)
	}
	var verdict, action string
	if err := b.Store.Read.QueryRow(`SELECT verdict,action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&verdict, &action); err != nil {
		t.Fatal(err)
	}
	if verdict != "clean" || action != actionPrewarmChecked {
		t.Fatalf("verdict/action = %q/%q，期望 clean/prewarm_checked", verdict, action)
	}
}

// 低于采信线不处置。
func TestPrewarmBelowLine(t *testing.T) {
	b, fake, uid, chat := setupPrewarm(t,
		soReply("ad", 0.5, "promo", "account"), "")
	sendPrewarmMessage(t, b, uid, chat)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("低于采信线不该禁言，得到 %d 次", got)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != actionPrewarmChecked {
		t.Fatalf("低于采信线也应完成复核并落流水，action = %q", action)
	}
}

// 简介里冒用国家领导人：走与 judgeAndAct 同一条硬规则，直接封禁出群；
// 头像查询与两级 AI 都不该跑（零额外开销）。
func TestPrewarmLeaderBioBansWithoutAI(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	soN, llmN := fakeAIWith(t, b,
		soReply("clean", 0.9, "none", "account"),
		llmReply(false, 0.9, "none", "account"))
	// 昵称与用户名都正常，只有简介命中（简介是异步段 getChat 才拿到的）。
	fake.Resp["getChat"] = `{"ok":true,"result":{"first_name":"张三","bio":"我的偶像邓小平"}}`
	fake.Resp["getUserProfilePhotos"] = `{"ok":true,"result":{"total_count":0}}`

	recordJoin(b, -100, 555, time.Now().Unix()-3600)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "哈喽"))
	waitIdle(t, b)

	if n := fake.CountCalls("banChatMember"); n != 1 {
		t.Fatalf("资料冒用领导人应封禁出群 1 次，得到 %d", n)
	}
	var action, kind string
	if err := b.Store.Read.QueryRow(`SELECT action,ad_kind FROM antiad_log
		WHERE chat_id=-100 AND user_id=555 ORDER BY id DESC LIMIT 1`).
		Scan(&action, &kind); err != nil {
		t.Fatal(err)
	}
	if action != "deleted_banned" || kind != "impersonate" {
		t.Fatalf("action/kind = %q/%q，期望 deleted_banned/impersonate", action, kind)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("硬规则命中不该送 AI，跑了 %d 次", n)
	}
	if n := fake.CountCalls("getUserProfilePhotos"); n != 0 {
		t.Fatalf("硬规则命中不该查头像，得到 %d 次", n)
	}
	if _, ok := loadJoinMute(b.Store, -100, 555); ok {
		t.Fatal("硬规则封禁不该写 join_mutes")
	}
}

// 演练群：只落 dryrun:prewarm_muted，不删消息、不禁言、不写 join_mutes。
func TestPrewarmDryrunOnlyLogs(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getUserProfilePhotos"] = `{"ok":true,"result":{"total_count":0}}`
	fakeAIWith(t, b, soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	recordJoin(b, -100, 555, time.Now().Unix()-3600)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "哈喽"))
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("演练不该禁言，得到 %d 次", got)
	}
	if got := fake.CountCalls("deleteMessage"); got != 0 {
		t.Fatalf("演练不该删消息，得到 %d 次", got)
	}
	if _, ok := loadJoinMute(b.Store, -100, 555); ok {
		t.Fatal("演练不该写 join_mutes")
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=-100 AND user_id=555 ORDER BY id DESC LIMIT 1`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "dryrun:"+actionPrewarmMuted {
		t.Fatalf("action = %q，期望 dryrun:prewarm_muted", action)
	}
}

// 头像查询失败仍送检：photo_known=false 不得当成无头像，正常结论照落。
func TestPrewarmPhotoFailureStillJudges(t *testing.T) {
	b, fake, uid, chat := setupPrewarm(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getUserProfilePhotos" {
			return `{"ok":false,"description":"boom"}`, true
		}
		return "", false
	}
	sendPrewarmMessage(t, b, uid, chat)
	if n := fake.CountCalls("getUserProfilePhotos"); n == 0 {
		t.Fatal("头像查询应被调用")
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != actionPrewarmChecked {
		t.Fatalf("头像失败也应完成复核，action = %q", action)
	}
}

// systemone 失败（返回缺 is_ad 的响应）：落到大模型并仍能定案。
func TestPrewarmSystemoneDownUsesLLM(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getUserProfilePhotos"] = `{"ok":true,"result":{"total_count":0}}`
	var llmN atomic.Int32
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			w.Write([]byte(`{"answers":{}}`)) // 解析失败 → judgeSystemOne 报错
			return
		}
		llmN.Add(1)
		w.Write([]byte(llmReply(true, 0.95, "promo", "account")))
	})
	recordJoin(b, -100, 555, time.Now().Unix()-3600)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "哈喽"))
	waitIdle(t, b)

	if llmN.Load() == 0 {
		t.Fatal("systemone 失败应回落大模型")
	}
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("大模型判 ad 应禁言 1 次，得到 %d", got)
	}
}

// 两级判定都失败：回落普通消息判定（也失败则按既有「判定失败放行」）。
func TestPrewarmAIErrorFallsBackToMessageJudge(t *testing.T) {
	b, _, uid, chat := setupPrewarm(t, "", "") // 未配模型：两条路都会失败
	sendPrewarmMessage(t, b, uid, chat)

	var action, note string
	if err := b.Store.Read.QueryRow(`SELECT action,reason FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&action, &note); err != nil {
		t.Fatal(err)
	}
	// 回落路径必须真的跑过消息判定：它留下 action=none 的失败流水。
	if action != "none" {
		t.Fatalf("回落消息判定应落 action=none，得到 %q", action)
	}
}

// 提示词必须带上共用条款（业务清单/资料链接/资料放行/摘要口径），
// 并说明 prewarm_check 与 photo_known。
func TestPrewarmPromptsShareClauses(t *testing.T) {
	for name, p := range map[string]string{
		"prewarm": prewarmInstructions, "prewarmLLM": prewarmLLMPrompt,
	} {
		for _, want := range []string{"prewarm_check", "photo_known", "业务清单",
			"没有标价也算", "known_ad_patterns", "不是此人的资料"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s 提示词缺少 %q", name, want)
			}
		}
	}
}

// 载荷必须带 prewarm_check=true，否则提示词口径与模型看到的输入对不上。
func TestPrewarmPayloadCarriesFlag(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	fake.Resp["getUserProfilePhotos"] = `{"ok":true,"result":{"total_count":0}}`
	var saw atomic.Bool
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"prewarm_check":true`) {
			saw.Store(true)
		}
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
			return
		}
		w.Write([]byte(llmReply(false, 0.9, "none", "message")))
	})
	recordJoin(b, -100, 555, time.Now().Unix()-3600)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "哈喽"))
	waitIdle(t, b)
	if !saw.Load() {
		t.Fatal("送检载荷里应带 prewarm_check=true")
	}
}

// 端到端：开关开启时，首条招呼走前置号复核而不是普通消息判定。
func TestPrewarmPipelineRoutesToAccountCheck(t *testing.T) {
	b, _, uid, chat := setupPrewarm(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	sendPrewarmMessage(t, b, uid, chat)

	// 普通消息判定的提示词里没有 prewarm_check 这句；命中前置号时
	// 不应该再跑一遍普通消息判定的先行动作（无临时禁言 tempMutes）。
	if _, ok := cachesOf(b.Shared).tempMutes.Get(tempMuteKey(chat, uid)); ok {
		t.Fatal("前置号复核不应触发消息判定的临时禁言")
	}
	rec, ok := loadJoinMute(b.Store, chat, uid)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("应写入 prewarm 限制，得到 (%v,%q)", ok, rec.Kind)
	}
}

// TestJoinMuteKindRoundTrip：kind 要能落库读回，prewarm 与 profile 区分开。
func TestJoinMuteKindRoundTrip(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, kindPrewarm, "前置号", 0)
	rec, ok := loadJoinMute(b.Store, -100, 555)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("kind 读回 = %q,%v，期望 prewarm", rec.Kind, ok)
	}
	// upsert 更新 kind：同一个人再次被资料类命中时以最后一次为准。
	saveJoinMute(b, -100, 555, kindProfile, "资料广告", 0)
	if rec, _ = loadJoinMute(b.Store, -100, 555); rec.Kind != kindProfile {
		t.Fatalf("upsert 后 kind = %q，期望 profile", rec.Kind)
	}
}

// TestUserPhotoCount：查得到要缓存；查失败要 ok=false 且不缓存（下次重试）。
func TestUserPhotoCount(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fake := b.TG.(*testutil.FakeTG)

	// 查得到：用非零张数，确认解析的是 total_count 本身而不是恒零。
	fake.Resp["getUserProfilePhotos"] =
		`{"ok":true,"result":{"total_count":3,"photos":[]}}`
	n, ok := userPhotoCount(b, 555)
	if !ok || n != 3 {
		t.Fatalf("首次查询 = (%d,%v)，期望 (3,true)", n, ok)
	}
	if calls := fake.Calls("getUserProfilePhotos"); len(calls) != 1 ||
		calls[0]["user_id"] != float64(555) || calls[0]["limit"] != float64(1) {
		t.Fatalf("查询载荷 = %#v，期望 user_id=555、limit=1", calls)
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("首次应查 1 次，得到 %d", got)
	}
	if n, ok := userPhotoCount(b, 555); !ok || n != 3 {
		t.Fatalf("第二次缓存读回 = (%d,%v)，期望 (3,true)", n, ok)
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("缓存命中不应再查，得到 %d 次", got)
	}

	// total_count=0 是合法结果（无头像），必须与失败区分开。
	b0, _ := testutil.NewTestBot(t, 1)
	b0.TG.(*testutil.FakeTG).Resp["getUserProfilePhotos"] =
		`{"ok":true,"result":{"total_count":0,"photos":[]}}`
	if n, ok := userPhotoCount(b0, 555); !ok || n != 0 {
		t.Fatalf("零头像 = (%d,%v)，期望 (0,true)", n, ok)
	}

	// 失败：ok=false 且不进缓存。
	b2, _ := testutil.NewTestBot(t, 1)
	fake = b2.TG.(*testutil.FakeTG)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getUserProfilePhotos" {
			return `{"ok":false,"description":"boom"}`, true
		}
		return "", false
	}
	if _, ok := userPhotoCount(b2, 556); ok {
		t.Fatal("失败应返回 ok=false")
	}
	if _, ok := userPhotoCount(b2, 556); ok {
		t.Fatal("失败不应被缓存成成功")
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 2 {
		t.Fatalf("失败不应缓存，应查 2 次，得到 %d", got)
	}
}

// setupPrewarmSweep 用真 Registry 起装：PrewarmSweep 以 sh.Reg 遍历 bot，
// 普通 NewTestBot 没有 Registry，整轮会被 nil 守卫跳过。
func setupPrewarmSweep(t *testing.T, so, llm string) (*core.Bot, *testutil.FakeTG, int64) {
	t.Helper()
	_, b := testutil.NewTestRegistry(t, nil)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm_sweep", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	fakeAIWith(t, b, so, llm)
	return b, fake, -100
}

func addSweepMember(t *testing.T, b *core.Bot, chat, uid, joinedAgo, msgs, whitelisted int64) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits,whitelisted)
		VALUES (?,?,?,?,?,0,0,?)`,
		chat, uid, now-joinedAgo, now-joinedAgo, msgs, whitelisted); err != nil {
		t.Fatal(err)
	}
}

// 窗口/白名单/发言数/已查过/已禁言的筛选，以及重复 sweep 的幂等。
func TestPrewarmSweepSelectionAndIdempotency(t *testing.T) {
	b, fake, chat := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	now := time.Now().Unix()
	addSweepMember(t, b, chat, 601, 25*3600, 0, 0)   // 命中：资料变广告
	addSweepMember(t, b, chat, 602, 2*3600, 0, 0)    // 进群 <24h：不查
	addSweepMember(t, b, chat, 603, 8*24*3600, 0, 0) // 进群 >7d：不查
	addSweepMember(t, b, chat, 604, 25*3600, 0, 1)   // 白名单：不查
	addSweepMember(t, b, chat, 605, 25*3600, 3, 0)   // 发言 3 条：不查
	addSweepMember(t, b, chat, 606, 25*3600, 0, 0)   // 已查过：不查
	old606 := now - 3600
	b.Store.Write.Exec(`UPDATE group_members SET prewarm_checked_at=?
		WHERE chat_id=? AND user_id=606`, old606, chat)
	addSweepMember(t, b, chat, 607, 25*3600, 0, 0) // 已在 prewarm 禁言：标记但不重复禁
	saveJoinMute(b, chat, 607, kindPrewarm, "已有前置号限制", 0)
	addSweepMember(t, b, chat, 609, 25*3600, 0, 0) // 资料为空：跳过不判（空壳归 Layer 1）

	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method == "getChat" {
			uid := int64(p["chat_id"].(float64))
			bio := ""
			if uid == 601 {
				bio = "免押小额洗资：https://t.me/+abcdef"
			}
			return fmt.Sprintf(`{"ok":true,"result":{"id":%d,"bio":%q}}`, uid, bio), true
		}
		return "", false
	}

	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("应只禁 601 一个人，得到 %d 次", got)
	}
	checked := map[int64]int64{}
	rows, err := b.Store.Read.Query(`SELECT user_id,prewarm_checked_at
		FROM group_members WHERE chat_id=?`, chat)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var uid, at int64
		if rows.Scan(&uid, &at) == nil {
			checked[uid] = at
		}
	}
	rows.Close()
	for _, uid := range []int64{602, 603, 604, 605} {
		if checked[uid] != 0 {
			t.Errorf("uid %d 不该被查，checked=%d", uid, checked[uid])
		}
	}
	if checked[606] != old606 {
		t.Errorf("已查过的 606 时间戳被改写：%d -> %d", old606, checked[606])
	}
	if checked[601] == 0 || checked[607] == 0 || checked[609] == 0 {
		t.Errorf("601/607/609 应标记已查，得到 %d/%d/%d",
			checked[601], checked[607], checked[609])
	}

	// 再跑一轮：不产生第二次禁言，kind 不被覆盖成 profile。
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("重复 sweep 不应再禁，得到 %d 次", got)
	}
	if rec, ok := loadJoinMute(b.Store, chat, 607); !ok || rec.Kind != kindPrewarm {
		t.Fatalf("607 的 kind = %q,%v，应保持 prewarm", rec.Kind, ok)
	}
}

// 资料已被复判放行且没改过：跳过，不重复吃同一个结论。
func TestPrewarmSweepSkipsAllowedProfile(t *testing.T) {
	b, fake, chat := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 608, 25*3600, 0, 0)
	bio := "免押小额洗资：https://t.me/+abcdef"
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getChat" {
			return fmt.Sprintf(`{"ok":true,"result":{"id":608,"bio":%q}}`, bio), true
		}
		return "", false
	}
	// 指纹只看用户名/昵称/简介；这里三项与 prewarmRecheck 组装的一致。
	if GrantProfileOK(b, senderProfile{UserID: 608, Bio: bio}, 6, "测试放行") == 0 {
		t.Fatal("测试前置放行失败")
	}

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("资料已放行的不该禁言，得到 %d 次", got)
	}
}

// 演练群：只落 dryrun:join_muted，不动人。
func TestPrewarmSweepDryrunOnlyLogs(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	testutil.EnableAntiadMode(t, b, -100, true)
	if err := b.PutSetting("antiad_prewarm_sweep", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	fakeAIWith(t, b, soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, -100, 601, 25*3600, 0, 0)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":601,"bio":"洗资"}}`, true
		}
		return "", false
	}

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("演练不该禁言，得到 %d 次", got)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=-100 AND user_id=601 ORDER BY id DESC LIMIT 1`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "dryrun:join_muted" {
		t.Fatalf("action = %q，期望 dryrun:join_muted", action)
	}
}

// 复查判为正常：落 join_checked，不禁言。
func TestPrewarmSweepCleanLogsJoinChecked(t *testing.T) {
	b, fake, chat := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	addSweepMember(t, b, chat, 610, 25*3600, 0, 0)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":610,"bio":"喜欢摄影"}}`, true
		}
		return "", false
	}

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("正常资料不该禁言，得到 %d 次", got)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=? AND user_id=610 ORDER BY id DESC LIMIT 1`,
		chat).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "join_checked" {
		t.Fatalf("action = %q，期望 join_checked", action)
	}
}

// 全局急停时不跑。
func TestPrewarmSweepHonorsGlobalStop(t *testing.T) {
	b, fake, chat := setupPrewarmSweep(t, soReply("ad", 0.99, "promo", "account"), "")
	addSweepMember(t, b, chat, 601, 25*3600, 0, 0)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":601,"bio":"洗资"}}`, true
		}
		return "", false
	}
	if err := b.PutSetting("antiad_enabled", "0"); err != nil {
		t.Fatal(err)
	}
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("急停时不该动手，得到 %d 次", got)
	}
}
