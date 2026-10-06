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
	if prewarmCandidate(b, snap, gm, msg("。。。"), false) {
		t.Error("纯标点消息不该命中")
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
	// 流水要挂被删招呼的消息号，申诉页的留底才能把它标成「被拦」。
	var msgID int64
	if err := b.Store.Read.QueryRow(`SELECT message_id FROM antiad_log
		WHERE chat_id=? AND user_id=? AND action=? ORDER BY id DESC LIMIT 1`,
		chat, uid, actionPrewarmMuted).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	if msgID != 1 {
		t.Fatalf("prewarm_muted 应挂招呼消息号 1，得到 %d", msgID)
	}
}

// 已在进群类禁言中的成员再发首条招呼：不重复禁言、不删招呼、不改 kind
// （与 Layer 2 的判重对称，也堵住「冷判定先禁、招呼后到」的竞态）。
func TestPrewarmJudgeSkipsExistingJoinMute(t *testing.T) {
	b, fake, uid, chat := setupPrewarm(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	saveJoinMute(b, chat, uid, kindProfile, "简介里有联系方式", 0)
	sendPrewarmMessage(t, b, uid, chat)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("已有进群限制不该重复禁言，得到 %d 次", got)
	}
	if got := fake.CountCalls("deleteMessage"); got != 0 {
		t.Errorf("已有进群限制不该删招呼，得到 %d 次", got)
	}
	if rec, ok := loadJoinMute(b.Store, chat, uid); !ok || rec.Kind != kindProfile {
		t.Fatalf("kind = %q,%v，应保持 profile 不被改写", rec.Kind, ok)
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
	var action, note string
	if err := b.Store.Read.QueryRow(`SELECT action,reason FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, uid).Scan(&action, &note); err != nil {
		t.Fatal(err)
	}
	if action != actionPrewarmChecked {
		t.Fatalf("低于采信线也应完成复核并落流水，action = %q", action)
	}
	if !strings.Contains(note, "前置号复核（低于采信线，未处置）") {
		t.Fatalf("低于采信线的结论应注明未处置，得到 %q", note)
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

// TestJoinMuteKindRoundTrip：kind 要能落库读回，且 upsert 不降级 ——
// prewarm 一旦写入，Layer 2 复查随后写的 profile 不能把它覆盖回去，
// 否则申诉出口会被悄悄放宽。反向（profile → prewarm）是升级，允许。
func TestJoinMuteKindRoundTrip(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, kindPrewarm, "前置号", 0)
	rec, ok := loadJoinMute(b.Store, -100, 555)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("kind 读回 = %q,%v，期望 prewarm", rec.Kind, ok)
	}
	// prewarm → profile：不降级（Layer 1 与 Layer 2 的竞态）。
	saveJoinMute(b, -100, 555, kindProfile, "资料广告", 0)
	if rec, _ = loadJoinMute(b.Store, -100, 555); rec.Kind != kindPrewarm {
		t.Fatalf("profile 覆盖后 kind = %q，期望保持 prewarm", rec.Kind)
	}
	// profile → prewarm：升级允许。
	saveJoinMute(b, -100, 556, kindProfile, "资料广告", 0)
	saveJoinMute(b, -100, 556, kindPrewarm, "前置号", 0)
	if rec, _ = loadJoinMute(b.Store, -100, 556); rec.Kind != kindPrewarm {
		t.Fatalf("prewarm 覆盖后 kind = %q，期望 prewarm", rec.Kind)
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

// setupPrewarmSweep 用真 Registry 起装：探测以 sh.Reg 遍历 bot，普通
// NewTestBot 没有 Registry，整轮会被 nil 守卫跳过。返回的计数器用来
// 断言「哪些路径一个 AI 都没花」。
func setupPrewarmSweep(t *testing.T, so, llm string) (*core.Bot, *testutil.FakeTG, int64,
	*atomic.Int32, *atomic.Int32) {
	t.Helper()
	_, b := testutil.NewTestRegistry(t, nil)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm_sweep", "1"); err != nil {
		t.Fatal(err)
	}
	fake := b.TG.(*testutil.FakeTG)
	soN, llmN := fakeAIWith(t, b, so, llm)
	return b, fake, -100, soN, llmN
}

// runPrewarmProbes 反复跑低优先级探测直到没有到期候选（每次最多处理
// 一个），等价于旧测试里的「跑一轮 sweep + 等判定跑空」。
func runPrewarmProbes(t *testing.T, sh *core.Shared) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		if prewarmProbeOnce(sh) == 0 {
			return
		}
	}
	t.Fatal("探测没有收敛")
}

// addSweepMember 造一个成员画像。joinedAgo < 0 表示入群时间未知
// （joined_at=0 的存量成员，进入老成员轮转）。
func addSweepMember(t *testing.T, b *core.Bot, chat, uid, joinedAgo, whitelisted int64) {
	t.Helper()
	now := time.Now().Unix()
	joined := now - joinedAgo
	if joinedAgo < 0 {
		joined = 0
	}
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits,whitelisted)
		VALUES (?,?,?,?,0,0,0,?)`,
		chat, uid, joined, joined, whitelisted); err != nil {
		t.Fatal(err)
	}
}

// setSweepSchedule 预置上次复查时间、下次到期时间与资料指纹。
func setSweepSchedule(t *testing.T, b *core.Bot, chat, uid, checkedAt, nextAt int64, hash string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET prewarm_checked_at=?, prewarm_next_at=?, profile_hash=?
		WHERE chat_id=? AND user_id=?`,
		checkedAt, nextAt, hash, chat, uid); err != nil {
		t.Fatal(err)
	}
}

// sweepSchedule 读某人的复查时间、下次到期时间与指纹。
func sweepSchedule(t *testing.T, b *core.Bot, chat, uid int64) (int64, int64, string) {
	t.Helper()
	var checked, next int64
	var hash string
	if err := b.Store.Read.QueryRow(`SELECT prewarm_checked_at,prewarm_next_at,profile_hash
		FROM group_members WHERE chat_id=? AND user_id=?`, chat, uid).
		Scan(&checked, &next, &hash); err != nil {
		t.Fatal(err)
	}
	return checked, next, hash
}

// 阶梯边界逐一断言：新人（≤72h，与首条招呼的进群窗口一致）随时间
// 拉长但封顶 5min，过了新人窗口放宽到老成员节奏。joined_at=0 走最老一档。
func TestPrewarmSweepInterval(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want time.Duration
	}{
		{"0", 0, time.Minute},
		{"59m", 59 * time.Minute, time.Minute},
		{"1h", time.Hour, time.Minute}, // ≤1h 仍是 1min
		{"1h01", time.Hour + time.Minute, 5 * time.Minute},
		{"12h", 12 * time.Hour, 5 * time.Minute},
		{"24h", 24 * time.Hour, 5 * time.Minute},
		{"71h59", 71*time.Hour + 59*time.Minute, 5 * time.Minute},
		{"72h", 72 * time.Hour, 5 * time.Minute}, // 新人窗口封顶 5min
		{"72h01", 72*time.Hour + time.Minute, 30 * time.Minute},
		{"6d23h", 6*24*time.Hour + 23*time.Hour, 30 * time.Minute},
		{"7d", 7 * 24 * time.Hour, 30 * time.Minute},
		{"7d01", 7*24*time.Hour + time.Minute, time.Hour},
		{"30d", 30 * 24 * time.Hour, time.Hour},
	}
	for _, c := range cases {
		if got := prewarmSweepInterval(c.age); got != c.want {
			t.Errorf("%s: prewarmSweepInterval = %v，期望 %v", c.name, got, c.want)
		}
	}
	// 重点关注成员（空壳特征部分命中）无论多老都压回 5min。
	if got := prewarmSweepIntervalFor(30*24*time.Hour, true); got != 5*time.Minute {
		t.Errorf("重点关注的老成员应封顶 5min，得到 %v", got)
	}
	if got := prewarmSweepIntervalFor(30*time.Minute, true); got != time.Minute {
		t.Errorf("重点关注的新成员仍是 1min 档，得到 %v", got)
	}
	if got := prewarmSweepIntervalFor(30*24*time.Hour, false); got != time.Hour {
		t.Errorf("非重点关注的老成员应走 1h 档，得到 %v", got)
	}
	// joined_at=0（进群时间未知）按最老一档处理，不当成刚进门的号重扫。
	const now = 1_700_000_000
	if got := prewarmAge(now, 0); got <= 7*24*time.Hour {
		t.Errorf("joined_at=0 的年龄应超过 7d，得到 %v", got)
	}
	if got := prewarmSweepInterval(prewarmAge(now, 0)); got != time.Hour {
		t.Errorf("joined_at=0 应取 1h 档，得到 %v", got)
	}
	if got := prewarmAge(now, now-30*60); got != 30*time.Minute {
		t.Errorf("年龄换算 = %v，期望 30m", got)
	}
	// 时钟回拨不应给出负年龄。
	if got := prewarmAge(now, now+3600); got != 0 {
		t.Errorf("未来 joined_at 的年龄应为 0，得到 %v", got)
	}
}

// sweepProfile 是假 getChat 给某个 uid 返回的资料。
type sweepProfile struct {
	first    string
	username string
	bio      string
}

// fakeSweepProfiles 让 getChat 按 uid 返回给定资料（未列出的 uid 返回空资料）。
func fakeSweepProfiles(t *testing.T, fake *testutil.FakeTG, profs map[int64]sweepProfile) {
	t.Helper()
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method != "getChat" {
			return "", false
		}
		uid := int64(p["chat_id"].(float64))
		pr := profs[uid]
		return fmt.Sprintf(`{"ok":true,"result":{"id":%d,"first_name":%q,"bio":%q}}`,
			uid, pr.first, pr.bio), true
	}
}

// 选人、跳过的各条路径与重复 sweep 的幂等：只有 prewarm_next_at 到点的人
// 进候选；未到期不动、白名单不选、join_mutes 只标记；首查干净资料零 AI。
func TestPrewarmSweepSelectionAndIdempotency(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	now := time.Now().Unix()
	addSweepMember(t, b, chat, 601, 25*3600, 0) // 到期，可疑简介 → AI 命中禁言
	setSweepSchedule(t, b, chat, 601, now-1800, now-1, "")
	addSweepMember(t, b, chat, 602, 25*3600, 0) // 未到期：完全不碰
	setSweepSchedule(t, b, chat, 602, now-3600, now+1800, "stale602")
	addSweepMember(t, b, chat, 604, 25*3600, 1) // 群白名单：不选
	addSweepMember(t, b, chat, 607, 25*3600, 0) // 已在 prewarm 禁言：只标记 + 推下一次
	setSweepSchedule(t, b, chat, 607, now-600, now-1, "")
	saveJoinMute(b, chat, 607, kindPrewarm, "已有前置号限制", 0)
	addSweepMember(t, b, chat, 608, 2*3600, 0) // 首查干净 → 零 AI，只落指纹
	setSweepSchedule(t, b, chat, 608, now-600, now-1, "")

	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		601: {bio: "免押小额洗资：https://t.me/+abcdef"},
		608: {bio: "喜欢摄影"},
	})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("应只禁 601 一个人，得到 %d 次", got)
	}
	for _, uid := range []int64{601, 607, 608} {
		checked, next, _ := sweepSchedule(t, b, chat, uid)
		if checked < now || next <= now {
			t.Errorf("uid %d 应记录复查时间并预排下一次：checked=%d next=%d",
				uid, checked, next)
		}
	}
	// 首查干净：指纹落库，但一个 AI 都没跑（601 的 AI 由假上游断言）。
	for _, uid := range []int64{601, 608} {
		if _, _, hash := sweepSchedule(t, b, chat, uid); hash == "" {
			t.Errorf("uid %d 首查后应落资料指纹", uid)
		}
	}
	// 未到期（602）与群白名单（604）不该被碰。
	if checked, next, hash := sweepSchedule(t, b, chat, 602); checked != now-3600 ||
		next != now+1800 || hash != "stale602" {
		t.Errorf("602 未到期不该复查：%d/%d/%q", checked, next, hash)
	}
	if checked, next, hash := sweepSchedule(t, b, chat, 604); checked != 0 ||
		next != 0 || hash != "" {
		t.Errorf("604 白名单不该被选：%d/%d/%q", checked, next, hash)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 607); hash != "" {
		t.Errorf("join_mutes 跳过只推时间，不该动指纹，得到 %q", hash)
	}

	// 再跑一轮：全部 next_at 都在未来（白名单不在候选里），不产生新动作。
	type sched struct {
		checked, next int64
		hash          string
	}
	checks := map[int64]sched{}
	for _, uid := range []int64{601, 602, 604, 607, 608} {
		checked, next, hash := sweepSchedule(t, b, chat, uid)
		checks[uid] = sched{checked, next, hash}
	}
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("重复 sweep 不应再禁，得到 %d 次", got)
	}
	for uid, want := range checks {
		checked, next, hash := sweepSchedule(t, b, chat, uid)
		if checked != want.checked || next != want.next || hash != want.hash {
			t.Errorf("uid %d 在未到期时被改写：(%d,%d,%q) -> (%d,%d,%q)",
				uid, want.checked, want.next, want.hash, checked, next, hash)
		}
	}
	if rec, ok := loadJoinMute(b.Store, chat, 607); !ok || rec.Kind != kindPrewarm {
		t.Fatalf("607 的 kind = %q,%v，应保持 prewarm", rec.Kind, ok)
	}
}

// next_at 预排调度：未到不查、到点再查；跳过路径也会推下一次；joined_at=0
// 走最老一档（1h），进群 2h 的走 5min 档。
func TestPrewarmSweepNextAtScheduling(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	now := time.Now().Unix()
	// 未到点：300 秒后才是候选，本轮连资料都不该拉。
	addSweepMember(t, b, chat, 620, 2*3600, 0)
	setSweepSchedule(t, b, chat, 620, now-7200, now+300, "futurehash")
	// 到点且进群 2h（5min 档）：首查干净 → 零 AI，落指纹并预排。
	addSweepMember(t, b, chat, 621, 2*3600, 0)
	setSweepSchedule(t, b, chat, 621, now-600, now-1, "")
	// 到点但已在进群类禁言：只推时间与下一次。
	addSweepMember(t, b, chat, 622, 2*3600, 0)
	setSweepSchedule(t, b, chat, 622, now-600, now-1, "")
	saveJoinMute(b, chat, 622, kindProfile, "已有进群限制", 0)
	// 到点的存量成员（joined_at=0）：按最老一档推 1h。
	addSweepMember(t, b, chat, 623, -1, 0)
	setSweepSchedule(t, b, chat, 623, now-600, now-1, "")
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		621: {bio: "喜欢摄影"}, 623: {bio: "喜欢摄影"},
	})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("无资料变化不该花 AI，跑了 %d 次", n)
	}
	// 620 未到点，原地不动。
	if checked, next, hash := sweepSchedule(t, b, chat, 620); checked != now-7200 ||
		next != now+300 || hash != "futurehash" {
		t.Errorf("620 未到点不该被碰：%d/%d/%q", checked, next, hash)
	}
	// 621：5min 档，指纹落库。
	checked, next, hash := sweepSchedule(t, b, chat, 621)
	if checked < now || hash == "" {
		t.Errorf("621 应复查并落指纹：checked=%d hash=%q", checked, hash)
	}
	if next < now+int64((5*time.Minute-time.Minute)/time.Second) ||
		next > now+int64((5*time.Minute+time.Minute)/time.Second) {
		t.Errorf("621 的 next_at 应落在 5min 档附近，得到 %d（now=%d）", next, now)
	}
	// 622：join_mutes 跳过，只推时间，指纹保持空。
	checked, next, hash = sweepSchedule(t, b, chat, 622)
	if checked < now || hash != "" {
		t.Errorf("622 join_mutes 跳过应只推时间：checked=%d hash=%q", checked, hash)
	}
	if next < now+int64((5*time.Minute-time.Minute)/time.Second) ||
		next > now+int64((5*time.Minute+time.Minute)/time.Second) {
		t.Errorf("622 的 next_at 应落在 5min 档附近，得到 %d", next)
	}
	// 623：joined_at=0 按 1h 档。
	checked, next, _ = sweepSchedule(t, b, chat, 623)
	if checked < now {
		t.Errorf("623 应复查：checked=%d", checked)
	}
	hour := int64(prewarmSweepInterval(prewarmAge(now, 0)) / time.Second)
	if next < now+hour-60 || next > now+hour+60 {
		t.Errorf("623 的 next_at 应落在 1h 档附近，得到 %d（now=%d）", next, now)
	}

	// 立刻再跑一轮：全部 next_at 都在未来，一个都不该被选中。
	before := map[int64]int64{}
	for _, uid := range []int64{620, 621, 622, 623} {
		_, nx, _ := sweepSchedule(t, b, chat, uid)
		before[uid] = nx
	}
	fake.Reset()
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("getChat"); got != 0 {
		t.Fatalf("未到点不该拉资料，得到 %d 次 getChat", got)
	}
	for uid, want := range before {
		if _, nx, _ := sweepSchedule(t, b, chat, uid); nx != want {
			t.Errorf("uid %d 未到点被改写：next %d -> %d", uid, want, nx)
		}
	}
}

// 入队前原子抢占（端到端）：worker 被占住、任务还没跑时，连续跑探测
// 只应复查一次，不重复入队同一人。
func TestPrewarmSweepClaimEnqueuesOnce(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	addSweepMember(t, b, chat, 660, 2*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{660: {bio: "喜欢摄影"}})

	// 占住全部 worker 与部分队列：复查任务排在后面，两轮探测之间没有
	// worker 会写回 next_at，专测抢占本身（这正是积压重跑/多 bot 的场景）。
	release := make(chan struct{}, 64)
	for i := 0; i < 64; i++ {
		if !b.AdSubmit(func() { <-release }) {
			t.Fatal("测试预置队列不应失败")
		}
	}

	runPrewarmProbes(t, b.Shared)
	runPrewarmProbes(t, b.Shared)

	// 抢占已把 next_at 推到 now+1min：第二轮 select 不该再看到它。
	if _, next, _ := sweepSchedule(t, b, chat, 660); next <= time.Now().Unix() {
		t.Fatalf("入队后应被抢占推后 next_at，得到 %d", next)
	}
	for i := 0; i < 64; i++ {
		release <- struct{}{}
	}
	waitIdle(t, b)

	if got := fake.CountCalls("getChat"); got != 1 {
		t.Fatalf("连续两轮探测只应复查 1 次，得到 %d 次 getChat", got)
	}
	// worker 完成后覆盖成真实档位（进群 2h → 5min），而不是 1min 抢占值。
	checked, next, _ := sweepSchedule(t, b, chat, 660)
	if next <= checked {
		t.Fatalf("复查后应按阶梯预排：checked=%d next=%d", checked, next)
	}
	if next > time.Now().Unix()+int64(5*time.Minute/time.Second) {
		t.Fatalf("2h 成员的下一档应是 5min，得到 %d", next)
	}
}

// 原子抢占：同一 uid 的第二次条件 UPDATE 必须 RowsAffected=0——并发
// 探测/积压重跑时只有一个能抢到。
func TestPrewarmClaimAtomic(t *testing.T) {
	b, _, chat, _, _ := setupPrewarmSweep(t, "", "")
	addSweepMember(t, b, chat, 661, 2*3600, 0)
	now := time.Now().Unix()
	if !claimPrewarmCandidate(b, chat, 661, now) {
		t.Fatal("第一次抢占应成功")
	}
	if claimPrewarmCandidate(b, chat, 661, now) {
		t.Fatal("抢占后的第二次条件更新应失败（RowsAffected=0）")
	}
}

// 到点后重新可选；msg_count>0 不再被过滤，仍是候选。
func TestPrewarmSweepSelectableAfterDue(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	addSweepMember(t, b, chat, 665, 2*3600, 0)
	if _, err := b.Store.Write.Exec(`UPDATE group_members SET msg_count=7
		WHERE chat_id=? AND user_id=?`, chat, 665); err != nil {
		t.Fatal(err)
	}
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{665: {bio: "喜欢摄影"}})

	// 首查（发言过的成员也该被选中）。
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	checked, next, hash := sweepSchedule(t, b, chat, 665)
	if checked == 0 || hash == "" {
		t.Fatalf("发言过的成员也该被首查：checked=%d hash=%q", checked, hash)
	}
	if next <= checked {
		t.Fatalf("应预排下一次：checked=%d next=%d", checked, next)
	}

	// 手动把下一次推到过去：到点后重新进候选；指纹未变 → 零 AI。
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 665, checked, now-1, hash)
	fake.Reset()
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	checked2, next2, hash2 := sweepSchedule(t, b, chat, 665)
	if checked2 < now || next2 <= now {
		t.Fatalf("到点应重新复查并预排：checked=%d next=%d", checked2, next2)
	}
	if hash2 != hash {
		t.Fatalf("指纹未变不该改写：%q -> %q", hash, hash2)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("指纹未变不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("getChat"); got != 1 {
		t.Fatalf("到点应重新拉一次资料，得到 %d 次 getChat", got)
	}
}

// 判定失败保留旧指纹：一次上游抖动不该把「化妆」当成已消费，下一档
// 到期还会重试；失败时冷却记录已清掉，重试不会被 10 分钟冷却挡住。
func TestPrewarmSweepJudgeErrorKeepsHashAndRetries(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t, "", "") // 未配可用的 AI
	addSweepMember(t, b, chat, 661, 25*3600, 0)
	oldH := profileHash(senderProfile{Bio: "旧资料"})
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 661, now-1800, now-1, oldH)
	bio := "免押小额洗资：https://t.me/+abcdef"
	newH := profileHash(senderProfile{Bio: bio})
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{661: {bio: bio}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	checked, next, hash := sweepSchedule(t, b, chat, 661)
	if hash != oldH {
		t.Fatalf("判定失败应保留旧指纹 %q，得到 %q", oldH, hash)
	}
	if checked < now || next <= now {
		t.Fatalf("判定失败应推时间和下一次：checked=%d next=%d", checked, next)
	}

	// 换成能判的假上游：同一份新资料应直接重判——不需要手动清冷却，
	// 失败分支自己已清（否则这里会只推 next_at、不送检）。
	fakeAIWith(t, b, soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	setSweepSchedule(t, b, chat, 661, checked, now-1, oldH)
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("下一档重试应能判并禁言 1 次，得到 %d", got)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 661); hash != newH {
		t.Fatalf("判定成功后应落新指纹 %q，得到 %q", newH, hash)
	}
}

// 禁言失败不是定案：restrictChatMember 抖动时保留旧指纹、下一档重试；
// 恢复后能正常禁言并落新指纹。
func TestPrewarmSweepMuteFailureKeepsHashAndRetries(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 668, 25*3600, 0)
	oldH := profileHash(senderProfile{Bio: "旧资料"})
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 668, now-1800, now-1, oldH)
	bio := "免押小额洗资：https://t.me/+abcdef"
	newH := profileHash(senderProfile{Bio: bio})

	// restrictChatMember 先失败一次，其余照常；getChat 仍按 uid 返回资料。
	var failMute atomic.Bool
	failMute.Store(true)
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		switch method {
		case "restrictChatMember":
			if failMute.Load() {
				return `{"ok":false,"description":"boom"}`, true
			}
			return `{"ok":true,"result":true}`, true
		case "getChat":
			uid := int64(p["chat_id"].(float64))
			return fmt.Sprintf(`{"ok":true,"result":{"id":%d,"bio":%q}}`, uid, bio), true
		}
		return "", false
	}

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if _, ok := loadJoinMute(b.Store, chat, 668); ok {
		t.Fatal("restrictChatMember 失败时不该写 join_mutes")
	}
	checked, next, hash := sweepSchedule(t, b, chat, 668)
	if hash != oldH {
		t.Fatalf("禁言失败应保留旧指纹 %q，得到 %q", oldH, hash)
	}
	if checked < now || next <= now {
		t.Fatalf("禁言失败应推时间和下一次：checked=%d next=%d", checked, next)
	}

	// 恢复 + 模拟冷却已过（AI 已成功跑过一次，冷却按真实时间留痕）：
	// 下一次到点应能禁言成功并落新指纹。
	failMute.Store(false)
	key := fmt.Sprintf("%d:%d", chat, 668)
	cachesOf(b.Shared).prewarmAI.Set(key,
		time.Now().Add(-prewarmAIInterval-time.Minute), prewarmAIInterval)
	setSweepSchedule(t, b, chat, 668, checked, now-1, oldH)
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if rec, ok := loadJoinMute(b.Store, chat, 668); !ok || rec.Kind != kindProfile {
		t.Fatalf("恢复后应禁言并写 join_mutes：ok=%v rec=%+v", ok, rec)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 668); hash != newH {
		t.Fatalf("禁言成功后应落新指纹 %q，得到 %q", newH, hash)
	}
}

// AI 冷却：10 分钟内资料再变也不判、不覆盖指纹，只把 next_at 推到冷却
// 结束；冷却到点后仍会判，不吞掉未判的化妆。
func TestPrewarmSweepAICooldown(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.95, "none", "message"), "")
	addSweepMember(t, b, chat, 662, 25*3600, 0)
	oldH := profileHash(senderProfile{Bio: "旧资料"})
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 662, now-600, now-1, oldH)
	bio1 := "喜欢摄影"
	h1 := profileHash(senderProfile{Bio: bio1})
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{662: {bio: bio1}})

	// 第一次资料变化：跑 AI、落新指纹。
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	ai1 := soN.Load() + llmN.Load()
	if ai1 == 0 {
		t.Fatal("首次变化应送 AI")
	}
	if _, _, hash := sweepSchedule(t, b, chat, 662); hash != h1 {
		t.Fatalf("首次变化应落新指纹 %q，得到 %q", h1, hash)
	}

	// 冷却期内再变一次：不花 AI、不覆盖指纹，next_at 推到冷却结束。
	bio2 := "免押小额洗资：https://t.me/+abcdef"
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{662: {bio: bio2}})
	setSweepSchedule(t, b, chat, 662, now, now-1, h1)
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if n := soN.Load() + llmN.Load(); n != ai1 {
		t.Fatalf("冷却期内不该花 AI，调用从 %d 涨到 %d", ai1, n)
	}
	checked, next, hash2 := sweepSchedule(t, b, chat, 662)
	if hash2 != h1 {
		t.Fatalf("冷却期内不该覆盖指纹：%q -> %q", h1, hash2)
	}
	if checked == 0 || next <= time.Now().Unix() {
		t.Fatalf("冷却期内应把 next_at 推到未来：checked=%d next=%d", checked, next)
	}
	if next > time.Now().Unix()+int64(prewarmAIInterval/time.Second)+5 {
		t.Fatalf("next_at 不该超过冷却结束太多，得到 %d", next)
	}

	// 冷却到点：显式把上次尝试时间拨到 11 分钟前，资料变化会被重新判。
	key := fmt.Sprintf("%d:%d", chat, 662)
	cachesOf(b.Shared).prewarmAI.Set(key,
		time.Now().Add(-prewarmAIInterval-time.Minute), prewarmAIInterval)
	fakeAIWith(t, b, soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	setSweepSchedule(t, b, chat, 662, checked, now-1, h1)
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if n := soN.Load() + llmN.Load(); n == 0 {
		t.Fatal("冷却结束后应重新判")
	}
	h2 := profileHash(senderProfile{Bio: bio2})
	if _, _, hash := sweepSchedule(t, b, chat, 662); hash != h2 {
		t.Fatalf("冷却结束后应落新指纹 %q，得到 %q", h2, hash)
	}
	if _, ok := loadJoinMute(b.Store, chat, 662); !ok {
		t.Fatal("冷却结束后的广告判定应禁言")
	}
}

// chatActive 关闭（群被停用）时直接跳过，但仍推时间与下一次，不拉资料。
func TestPrewarmSweepChatInactiveSkips(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"), "")
	addSweepMember(t, b, chat, 666, 2*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{666: {bio: "免押小额洗钱"}})
	if _, err := b.Store.Write.Exec(`UPDATE bot_chats SET enabled=0
		WHERE bot_id=? AND chat_id=?`, b.BotID(), chat); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	// PrewarmSweep 的群列表只含 enabled 群，这里直接调 worker 验分支。
	probePrewarmCandidate(b, chat, 666, time.Now().Unix())

	if got := fake.CountCalls("getChat"); got != 0 {
		t.Fatalf("停用群不该拉资料，得到 %d 次 getChat", got)
	}
	checked, next, hash := sweepSchedule(t, b, chat, 666)
	if checked == 0 || next <= checked {
		t.Fatalf("跳过也应推时间和下一次：checked=%d next=%d", checked, next)
	}
	if hash != "" {
		t.Fatalf("跳过不该落指纹，得到 %q", hash)
	}
}

// 退群重进：recordJoin 把 prewarm_next_at 清零，按新成员节奏复查，
// 但不动已有指纹与复查时间。
func TestRecordJoinResetsPrewarmNextAt(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	const chat, uid = int64(-100), int64(667)
	addSweepMember(t, b, chat, uid, 25*3600, 0)
	setSweepSchedule(t, b, chat, uid, 12345, 99999, "oldhash")

	recordJoin(b, chat, uid, time.Now().Unix())

	checked, next, hash := sweepSchedule(t, b, chat, uid)
	if next != 0 {
		t.Fatalf("重进应清零 prewarm_next_at，得到 %d", next)
	}
	if checked != 12345 || hash != "oldhash" {
		t.Fatalf("只该清零 next_at：checked=%d hash=%q", checked, hash)
	}
}

// 指纹未变 → 零 AI：哪怕资料里就有可疑词，也以上次看过的指纹为准。
func TestPrewarmSweepFingerprintSkipsAI(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 640, 25*3600, 0)
	bio := "免押小额洗资：https://t.me/+abcdef"
	h := profileHash(senderProfile{Bio: bio})
	now := time.Now().Unix()
	stale := now - 25*3600
	setSweepSchedule(t, b, chat, 640, stale, now-1, h)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{640: {bio: bio}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("指纹未变不该禁言，得到 %d 次", got)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("指纹未变不该花 AI，跑了 %d 次", n)
	}
	checked, next, hash := sweepSchedule(t, b, chat, 640)
	if checked <= stale || next <= now || hash != h {
		t.Fatalf("只该推时间与下一次、不动指纹：checked=%d next=%d hash=%q",
			checked, next, hash)
	}
}

// 首查且本地预筛不中：零 AI，只落指纹。
func TestPrewarmSweepFirstSightCleanNoAI(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 641, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{641: {bio: "喜欢摄影"}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("首查正常资料不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("不该禁言，得到 %d 次", got)
	}
	if at, next, hash := sweepSchedule(t, b, chat, 641); at == 0 || next <= at || hash == "" {
		t.Fatalf("首查应落时间、指纹并预排下一次：at=%d next=%d hash=%q", at, next, hash)
	}
}

// 首查可疑资料（本地预筛命中）→ 冷判定；命中即禁言并写指纹。
func TestPrewarmSweepFirstSightSuspiciousJudges(t *testing.T) {
	b, fake, chat, soN, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 642, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{642: {bio: "免押小额洗钱"}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("可疑资料命中应禁言 1 次，得到 %d", got)
	}
	if soN.Load() == 0 {
		t.Fatal("预筛命中应送 AI")
	}
	if _, ok := loadJoinMute(b.Store, chat, 642); !ok {
		t.Fatal("应写 join_mutes")
	}
	if at, next, hash := sweepSchedule(t, b, chat, 642); at == 0 || next <= at || hash == "" {
		t.Fatalf("应落时间、指纹并预排下一次：at=%d next=%d hash=%q", at, next, hash)
	}
}

// 回归：昵称-only 的化妆（bio 仍为空、指纹已变）也要判。旧实现在
// bio=="" 时直接跳过，这类「进门后改名挂广告」的号会永远漏掉。
func TestPrewarmSweepNicknameOnlyChangeJudges(t *testing.T) {
	b, fake, chat, soN, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 643, 25*3600, 0)
	// 上次查过的是干净资料；现在昵称变成广告，简介仍为空。
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 643, now-25*3600, now-1,
		profileHash(senderProfile{FirstName: "Robert Williamson"}))
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		643: {first: "手机拍违停 一百圆/张"},
	})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("昵称-only 变化应判并禁言 1 次，得到 %d", got)
	}
	if soN.Load() == 0 {
		t.Fatal("昵称变化应送 AI（bio 为空不是跳过理由）")
	}
	if _, ok := loadJoinMute(b.Store, chat, 643); !ok {
		t.Fatal("应写 join_mutes")
	}
	if _, next, _ := sweepSchedule(t, b, chat, 643); next <= now {
		t.Fatalf("判定后应预排下一次，next=%d", next)
	}
}

// 资料已被复判放行且没改过：跳过，不重复吃同一个结论。
func TestPrewarmSweepSkipsAllowedProfile(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 608, 25*3600, 0)
	bio := "免押小额洗资：https://t.me/+abcdef"
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{608: {bio: bio}})
	// 预置一份旧指纹：本次资料算「变过」，直接进判定流程，专测
	// ProfileAllowed 这道门（首查路径另由 TestPrewarmSweepFirstSight* 覆盖）。
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 608, now-25*3600, now-1,
		profileHash(senderProfile{Bio: "旧资料"}))
	// 指纹只看用户名/昵称/简介；这里三项与探测段组装的一致。
	if GrantProfileOK(b, senderProfile{UserID: 608, Bio: bio}, 6, "测试放行") == 0 {
		t.Fatal("测试前置放行失败")
	}

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("资料已放行的不该禁言，得到 %d 次", got)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("放行资料不该送 AI，跑了 %d 次", n)
	}
	// 跳过也要预排下一次，否则下一分钟又会被选中。
	if _, next, _ := sweepSchedule(t, b, chat, 608); next <= now {
		t.Fatalf("ProfileAllowed 跳过应预排下一次，next=%d", next)
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
	addSweepMember(t, b, -100, 601, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{601: {bio: "洗钱"}})

	runPrewarmProbes(t, b.Shared)
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
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	addSweepMember(t, b, chat, 610, 25*3600, 0)
	// 预置旧指纹（资料变过）→ 走冷判定；首查干净资料现在是零 AI 路径，
	// 由 TestPrewarmSweepFirstSightCleanNoAI 覆盖。
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 610, now-25*3600, now-1,
		profileHash(senderProfile{Bio: "旧资料"}))
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{610: {bio: "喜欢摄影"}})

	runPrewarmProbes(t, b.Shared)
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
	b, fake, chat, _, _ := setupPrewarmSweep(t, soReply("ad", 0.99, "promo", "account"), "")
	addSweepMember(t, b, chat, 601, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{601: {bio: "洗钱"}})
	if err := b.PutSetting("antiad_enabled", "0"); err != nil {
		t.Fatal(err)
	}
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("急停时不该动手，得到 %d 次", got)
	}
}

// ad_whitelist（面板/申诉加的永久白名单）也要挡住延迟复查：候选查询只
// 过滤了群画像里的按群白名单（/white），其余豁免由 adExempt 兜住。
func TestPrewarmSweepHonorsAdWhitelist(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 611, 25*3600, 0) // 白名单：不查
	addSweepMember(t, b, chat, 612, 25*3600, 0) // 对照：照常禁言
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		611: {bio: "免押小额洗资：https://t.me/+abcdef"},
		612: {bio: "免押小额洗资：https://t.me/+abcdef"},
	})
	if err := AddWhitelist(b.Shared, b.BotID(), chat, 611, 0, "appeal", 1); err != nil {
		t.Fatal(err)
	}

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("白名单成员不该禁言，只应禁对照的 612，得到 %d 次", got)
	}
	if _, ok := loadJoinMute(b.Store, chat, 611); ok {
		t.Error("白名单成员不该写 join_mutes")
	}
	if _, ok := loadJoinMute(b.Store, chat, 612); !ok {
		t.Error("对照成员应被禁言并写 join_mutes")
	}
}

// antiad_exempt_users（全局豁免名单）也要挡住复查：跳过时只推时间、不落指纹。
func TestPrewarmSweepHonorsExemptUsers(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 614, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		614: {bio: "免押小额洗资：https://t.me/+abcdef"},
	})
	if err := b.PutBotSetting(b.BotID(), "antiad_exempt_users", "[614]"); err != nil {
		t.Fatal(err)
	}

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("豁免用户不该禁言，得到 %d 次", got)
	}
	if got := fake.CountCalls("getChat"); got != 0 {
		t.Fatalf("豁免用户不该拉资料，得到 %d 次 getChat", got)
	}
	if checked, next, hash := sweepSchedule(t, b, chat, 614); checked == 0 ||
		next <= checked || hash != "" {
		t.Fatalf("豁免跳过应只推时间与下一次、不落指纹：checked=%d next=%d hash=%q",
			checked, next, hash)
	}
}

// 探测节奏：每次 prewarmProbeOnce 最多处理一个候选（全局 ~5 次/秒），
// 并按群轮转，不会一次灌满判定池。
func TestPrewarmProbeOnePerCall(t *testing.T) {
	b, fake, _, _, _ := setupPrewarmSweep(t, "", "")
	chats := []int64{-100, -101, -102}
	for _, c := range chats {
		if c != -100 {
			testutil.EnableAntiad(t, b, c)
		}
		for i := 0; i < 2; i++ {
			addSweepMember(t, b, c, int64(7000+i), 25*3600, 0)
		}
	}
	fakeSweepProfiles(t, fake, nil)

	// 第一次探测只能处理一个候选。
	if n := prewarmProbeOnce(b.Shared); n != 1 {
		t.Fatalf("一轮探测应只处理 1 个候选，得到 %d", n)
	}
	waitIdle(t, b)
	if got := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE prewarm_checked_at > 0`); got != 1 {
		t.Fatalf("一轮探测后应只有 1 个已查标记，得到 %d", got)
	}

	// 继续轮转直到全部到点候选被处理完；每个群都该轮到。
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE prewarm_checked_at > 0`); got != 6 {
		t.Fatalf("轮转应把 6 个候选全部处理，得到 %d", got)
	}
	for _, c := range chats {
		n := countRows(t, b, `SELECT COUNT(*) FROM group_members
			WHERE chat_id=? AND prewarm_checked_at > 0`, c)
		if n != 2 {
			t.Errorf("群 %d 应处理 2 个候选（轮转覆盖），得到 %d", c, n)
		}
	}
}

// 探测协程节奏：注入很短的间隔后，到点候选会被自动处理（不需要手动
// 调 prewarmProbeOnce）。停止通道只给测试用。
func TestPrewarmProbeLoopPaces(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	addSweepMember(t, b, chat, 701, 2*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{701: {bio: "喜欢摄影"}})

	old := prewarmProbeInterval
	prewarmProbeInterval = 5 * time.Millisecond
	defer func() { prewarmProbeInterval = old }()
	PrewarmSweep(b.Shared)
	defer close(cachesOf(b.Shared).prewarmProbeStop)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if checked, _, _ := sweepSchedule(t, b, chat, 701); checked > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("探测协程 3 秒内没有处理到点候选")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 判定队列积压到高水位时，延迟复查不再提交，给实时消息判定让路。
func TestPrewarmSweepQueueHighWater(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t, "", "")
	addSweepMember(t, b, chat, 601, 25*3600, 0)

	// 占住判定 worker 与队列：这些任务一直阻塞到 release 关闭，AdBusy
	// 稳定停在高水位上（submit 计数含排队中与执行中，见 core.Bot.AdBusy）。
	release := make(chan struct{})
	defer close(release)
	for i := 0; i < prewarmQueueHighWater; i++ {
		if !b.AdSubmit(func() { <-release }) {
			t.Fatal("测试预置队列不应失败")
		}
	}
	// 到这里 AdBusy == prewarmQueueHighWater：任务全卡在 <-release 上，
	// 只有 defer 的 close 才让计数回落。

	runPrewarmProbes(t, b.Shared)
	// 直接盯计数：若在满水位上还提交了复查，AdBusy 会变成 257 —— 所有
	// worker 都卡在 <-release 上，没有任务能结束把计数降回去。
	if got := b.AdBusy(); got != prewarmQueueHighWater {
		t.Fatalf("队列积压时不该再提交复查：AdBusy = %d，期望 %d",
			got, prewarmQueueHighWater)
	}
	if got := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE prewarm_checked_at > 0`); got != 0 {
		t.Fatalf("队列积压时不该提交复查，得到 %d 条已查标记", got)
	}
	if got := fake.CountCalls("getChat"); got != 0 {
		t.Errorf("队列积压时不该有人被复查，得到 %d 次 getChat", got)
	}
}

// 低于采信线的 ad 结论要注明未处置，措辞与冷判定一致。
func TestPrewarmSweepBelowLineNote(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t, soReply("ad", 0.5, "promo", "account"), "")
	addSweepMember(t, b, chat, 613, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{613: {bio: "洗钱"}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("低于采信线不该禁言，得到 %d 次", got)
	}
	var note string
	if err := b.Store.Read.QueryRow(`SELECT reason FROM antiad_log
		WHERE chat_id=? AND user_id=613 ORDER BY id DESC LIMIT 1`,
		chat).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "延迟复查（低于采信线，未处置）") {
		t.Fatalf("低于采信线的结论应注明未处置，得到 %q", note)
	}
}

// 资料拉取全空不算首查完成：不落指纹、不花 AI，只推 next_at；恢复后
// 同一份广告资料会被判并禁言（防线上 429 风暴把广告永久吞掉）。
func TestPrewarmSweepEmptyFetchRetriesWithoutHash(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 670, 25*3600, 0)
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 670, now-1800, now-1, "")
	// getChat 全空（首次拉取被限流/失败）：名字、用户名、简介都没有。
	fakeSweepProfiles(t, fake, nil)

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	checked, next, hash := sweepSchedule(t, b, chat, 670)
	if hash != "" {
		t.Fatalf("空资料不能算首查完成落指纹，得到 %q", hash)
	}
	if checked < now || next <= now {
		t.Fatalf("空拉取应推时间和下一次：checked=%d next=%d", checked, next)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("空拉取不该花 AI，跑了 %d 次", n)
	}

	// 恢复：拉取返回真实广告资料；下一档（互斥 TTL 已过）应判并禁言。
	bio := "免押小额洗资：https://t.me/+abcdef"
	newH := profileHash(senderProfile{Bio: bio})
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{670: {bio: bio}})
	setSweepSchedule(t, b, chat, 670, checked, now-1, "")
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("恢复后应判广告并禁言 1 次，得到 %d", got)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 670); hash != newH {
		t.Fatalf("判定成功后应落新指纹 %q，得到 %q", newH, hash)
	}
}

// 人已离群、TG 直接答 400 member not found（bot 看不见离群动作时
// group_members 里仍留着此人）：按离群退避 7 天，而不是当成「拉取失败，
// 不确定」在复查阶梯里无限空转——线上每档刷「查询群管理员失败/查询账号
// 资料失败/资料拉取全空」三连警告，就是这条 400 没被认出来。
func TestPrewarmSweepGoneMemberNotFoundBacksOff(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 672, 25*3600, 0)
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 672, now-1800, now-1, "")
	fake.Err["getChat"] = testutil.TGNotFound("getChat",
		"Bad Request: chat not found")
	fake.Err["getChatMember"] = testutil.TGNotFound("getChatMember",
		"Bad Request: member not found")

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	checked, next, hash := sweepSchedule(t, b, chat, 672)
	if hash != "" {
		t.Fatalf("离群退避不该落指纹，得到 %q", hash)
	}
	if checked < now || next < now+int64(prewarmGoneBackoff/time.Second)-60 {
		t.Fatalf("离群成员应退避 7 天：checked=%d next=%d", checked, next)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("离群不该花 AI，跑了 %d 次", n)
	}
}

// 互斥：同一 uid 的一个复查还在处理中时，第二个投递立即返回，不再做
// 任何 TG 工作（队列积压导致抢占 hold 过期时的兜底）。
func TestPrewarmSweepInflightSkipsDuplicate(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 671, 25*3600, 0)
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 671, now-1800, now-1, "")

	started := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		if method == "getChat" {
			started <- struct{}{}
			<-release
			return `{"ok":true,"result":{"id":671,"first_name":"某广告",` +
				`"bio":"免押小额洗资：https://t.me/+abcdef"}}`, true
		}
		return "", false
	}

	first := make(chan struct{})
	go func() { probePrewarmCandidate(b, chat, 671, time.Now().Unix()); close(first) }()
	<-started

	// 第二个同 uid 的投递必须立即返回（互斥命中），不进任何 TG 调用。
	second := make(chan struct{})
	go func() { probePrewarmCandidate(b, chat, 671, time.Now().Unix()); close(second) }()
	immediate := false
	select {
	case <-second:
		immediate = true
	case <-time.After(2 * time.Second):
	}
	release <- struct{}{}
	release <- struct{}{} // 互斥缺失时第二个也卡在 getChat，这里放它出来
	<-first
	<-second
	if !immediate {
		t.Fatal("同一 uid 的第二个复查没有立即返回（互斥缺失）")
	}
	if got := fake.CountCalls("getChat"); got != 1 {
		t.Fatalf("同一 uid 只应有 1 次资料拉取，得到 %d 次", got)
	}
}

// 资料 enforce 规则命中：零 AI 直接禁言，流水带 rule:<id> 且计数 +1。
func TestPrewarmSweepProfileEnforceRuleZeroAI(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	ruleID := insertRule(t, b, "资料广告_测试", `免押小额洗资`, "promo", true, true)
	addSweepMember(t, b, chat, 680, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{680: {bio: "免押小额洗资 私聊"}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("enforce 命中不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("enforce 命中应禁言 1 次，得到 %d", got)
	}
	rec, ok := loadJoinMute(b.Store, chat, 680)
	if !ok || rec.Kind != kindProfile {
		t.Fatalf("应写 kind=profile 的进群限制：ok=%v rec=%+v", ok, rec)
	}
	var decider, action string
	if err := b.Store.Read.QueryRow(`SELECT decider,action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`, chat, 680).
		Scan(&decider, &action); err != nil {
		t.Fatal(err)
	}
	if decider != fmt.Sprintf("rule:%d", ruleID) || action != actionJoinMuted {
		t.Fatalf("decider/action = %q/%q，期望 rule:%d/join_muted",
			decider, action, ruleID)
	}
	if hits := countRows(t, b, `SELECT hits FROM ad_rules WHERE id=?`, ruleID); hits != 1 {
		t.Fatalf("规则命中计数应为 1，得到 %d", hits)
	}
}

// 非 enforce 资料规则命中：作为 matched_rules 强证据随载荷送 AI。
func TestPrewarmSweepProfileRuleEvidenceInPayload(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t, "", "")
	ruleID := insertRule(t, b, "资料广告_证据", `免押小额洗资`, "promo", true, false)
	addSweepMember(t, b, chat, 682, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{682: {bio: "免押小额洗资 私聊"}})

	var sawEvidence atomic.Bool
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body),
			fmt.Sprintf(`"matched_rules":[{"id":%d`, ruleID)) {
			sawEvidence.Store(true)
		}
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
			return
		}
		w.Write([]byte(llmReply(false, 0.9, "none", "message")))
	})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if !sawEvidence.Load() {
		t.Fatal("非 enforce 规则命中应作为 matched_rules 证据进 AI 载荷")
	}
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("非 enforce 命中应交 AI 定案，不该直接禁言，得到 %d 次", got)
	}
}

// 未变的命中资料重扫：非 enforce 规则命中只在真正送 AI 判定时计一次。
// 旧实现把规则门放在指纹门之前且对所有命中计数，这份资料没变、也不该
// 再判，却每上一档就给 ad_rules 记一次，计数被无限吹大。
func TestPrewarmSweepRuleHitsNotInflatedOnRescan(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	ruleID := insertRule(t, b, "资料广告_重扫", `免押小额洗资`, "promo", true, false)
	addSweepMember(t, b, chat, 683, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{683: {bio: "免押小额洗资 私聊"}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	ai1 := soN.Load() + llmN.Load()
	if ai1 == 0 {
		t.Fatal("首次判定的资料应走 AI")
	}
	if hits := countRows(t, b, `SELECT hits FROM ad_rules WHERE id=?`, ruleID); hits != 1 {
		t.Fatalf("首次判定应计一次规则命中，得到 %d", hits)
	}

	// 资料没变，把它推回到期：重扫应命中指纹门直接推时间，不判也不计数。
	_, _, h := sweepSchedule(t, b, chat, 683)
	now := time.Now().Unix()
	setSweepSchedule(t, b, chat, 683, now-1800, now-1, h)
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if hits := countRows(t, b, `SELECT hits FROM ad_rules WHERE id=?`, ruleID); hits != 1 {
		t.Fatalf("未变的命中资料重扫不该再计规则命中，得到 %d", hits)
	}
	if ai2 := soN.Load() + llmN.Load(); ai2 != ai1 {
		t.Fatalf("未变的命中资料重扫不该再花 AI：%d → %d", ai1, ai2)
	}
}

// 形状复用：一号 AI 判广告后学习形状，二号同模板换链接零 AI 直接禁言，
// 流水 decider=phash，命中计数递增。
func TestPrewarmSweepProfileShapeReuse(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "scam", "account"),
		llmReply(true, 0.95, "scam", "account"))
	bio1 := "免押小额洗资 https://t.me/+aaa"
	addSweepMember(t, b, chat, 690, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{690: {bio: bio1}})

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("一号应被 AI 判禁言 1 次，得到 %d", got)
	}
	ai1 := soN.Load() + llmN.Load()
	if ai1 == 0 {
		t.Fatal("一号应花 AI（首个模板）")
	}
	shape := profileShape(senderProfile{Bio: bio1})
	rec, ok := lookupProfileShape(b.Store, shape)
	if !ok || rec.Hits != 1 {
		t.Fatalf("一号禁言后应学习形状且 hits=1：ok=%v rec=%+v", ok, rec)
	}
	// 形状记的是广告内容类别（scam），不是限制类型 profile/prewarm。
	if rec.Kind != "scam" {
		t.Fatalf("形状应记广告类别 scam，得到 %q", rec.Kind)
	}

	// 二号：同模板换个邀请链接 → 形状命中，零 AI。
	addSweepMember(t, b, chat, 691, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		691: {bio: "免押小额洗资 https://t.me/+bbb"}})
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != ai1 {
		t.Fatalf("形状命中不该再花 AI：AI 调用从 %d 涨到 %d", ai1, n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 2 {
		t.Fatalf("二号应被禁言（共 2 次），得到 %d", got)
	}
	rec, ok = lookupProfileShape(b.Store, shape)
	if !ok || rec.Hits != 2 {
		t.Fatalf("形状命中计数应为 2：ok=%v rec=%+v", ok, rec)
	}
	var decider, adKind string
	if err := b.Store.Read.QueryRow(`SELECT decider,ad_kind FROM antiad_log
		WHERE chat_id=? AND user_id=691 ORDER BY id DESC LIMIT 1`, chat).
		Scan(&decider, &adKind); err != nil {
		t.Fatal(err)
	}
	if decider != "phash" {
		t.Fatalf("形状命中流水 decider = %q，期望 phash", decider)
	}
	// phash 流水必须显示内容类别，不能是限制类型 profile。
	if adKind != "scam" {
		t.Fatalf("phash 流水 ad_kind = %q，期望 scam（不是限制类型）", adKind)
	}
}

// 已离群（left/kicked）成员：拉资料为空时退避 7 天，不再按阶梯空转，
// 也不再重复拉取。
func TestPrewarmSweepGoneBackoff(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t, "", "")
	addSweepMember(t, b, chat, 702, 2*3600, 0)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		switch method {
		case "getChat":
			return `{"ok":true,"result":{}}`, true
		case "getChatMember":
			return `{"ok":true,"result":{"status":"kicked"}}`, true
		}
		return "", false
	}

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	checked, next, hash := sweepSchedule(t, b, chat, 702)
	if checked == 0 {
		t.Fatal("离群成员也应记录复查时间")
	}
	backoff := int64(prewarmGoneBackoff / time.Second)
	if next < checked+backoff-60 || next > checked+backoff+60 {
		t.Fatalf("离群退避应约 7 天：checked=%d next=%d", checked, next)
	}
	if hash != "" {
		t.Fatalf("离群退避不该落指纹，得到 %q", hash)
	}
	// 退避期内不再拉资料。
	fake.Reset()
	runPrewarmProbes(t, b.Shared)
	if got := fake.CountCalls("getChat"); got != 0 {
		t.Fatalf("退避期内不该重复拉资料，得到 %d 次 getChat", got)
	}
}

// ---- 空壳特征组合（零 AI 判定 / 重点关注） ----

// sendPrewarmMessageAs 以指定资料的用户发首条招呼。
func sendPrewarmMessageAs(t *testing.T, b *core.Bot, chatID int64,
	u *tg.TGUser, text string) {
	t.Helper()
	recordJoin(b, chatID, u.ID, time.Now().Unix()-3600)
	HandleGroupMessage(b, &tg.Message{
		MessageID: 1, Date: 1700000000, Text: text,
		From: u, Chat: &tg.Chat{ID: chatID, Type: "supergroup", Title: "测试群"},
	})
	waitIdle(t, b)
}

// watchFlag 读成员的关注标记。
func watchFlag(t *testing.T, b *core.Bot, chat, uid int64) int {
	t.Helper()
	var w int
	if err := b.Store.Read.QueryRow(`SELECT prewarm_watch FROM group_members
		WHERE chat_id=? AND user_id=?`, chat, uid).Scan(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

// 空壳四条件全齐（无头像+场景昵称+随机用户名+无简介）：零 AI 直接禁言，
// 招呼删除，流水 prewarm_muted 且 decider=prewarm_shell。
func TestPrewarmShellMutesWithoutAI(t *testing.T) {
	b, fake, _, chat := setupPrewarm(t, "", "")
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	sendPrewarmMessageAs(t, b, chat,
		&tg.TGUser{ID: 771, Username: "tpiw33abik", FirstName: "在线服务"}, "哈喽")

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("特征齐备不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("应禁言 1 次，得到 %d 次", got)
	}
	if got := fake.CountCalls("deleteMessage"); got == 0 {
		t.Fatal("应删除招呼消息")
	}
	rec, ok := loadJoinMute(b.Store, chat, 771)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("join_mutes = (%v,%q)，期望存在且 kind=prewarm", ok, rec.Kind)
	}
	var decider, action, reason string
	if err := b.Store.Read.QueryRow(`SELECT decider,action,text FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, 771).Scan(&decider, &action, &reason); err != nil {
		t.Fatal(err)
	}
	if decider != deciderPrewarmShell || action != actionPrewarmMuted {
		t.Fatalf("decider/action = %q/%q，期望 %q/%q",
			decider, action, deciderPrewarmShell, actionPrewarmMuted)
	}
	if !strings.Contains(reason, "随机") || !strings.Contains(reason, "无头像") {
		t.Errorf("reason 应列出证据，得到 %q", reason)
	}
}

// 只命中一部分（随机用户名+无简介，昵称正常）：不禁言，标记重点关注，
// AI 照常复核。
func TestPrewarmShellPartialWatches(t *testing.T) {
	b, fake, _, chat := setupPrewarm(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	sendPrewarmMessageAs(t, b, chat,
		&tg.TGUser{ID: 772, Username: "tpiw33abik", FirstName: "小明"}, "哈喽")

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("特征不全不该禁言，得到 %d 次", got)
	}
	if got := watchFlag(t, b, chat, 772); got != 1 {
		t.Fatalf("部分命中应标记重点关注，prewarm_watch = %d", got)
	}
	var action string
	if err := b.Store.Read.QueryRow(`SELECT action FROM antiad_log
		WHERE chat_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		chat, 772).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != actionPrewarmChecked {
		t.Fatalf("action = %q，期望 AI 照常复核落 %q", action, actionPrewarmChecked)
	}
}

// 有正常个人简介的用户：即使用户名随机也绝不进组合（不误伤）。
func TestPrewarmBioProtects(t *testing.T) {
	b, fake, _, chat := setupPrewarm(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	fake.Resp["getChat"] = `{"ok":true,"result":{"id":773,
		"first_name":"小明","bio":"热爱生活，喜欢旅行"}}`
	sendPrewarmMessageAs(t, b, chat,
		&tg.TGUser{ID: 773, Username: "tpiw33abik", FirstName: "小明"}, "哈喽")

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("有正常简介不该禁言，得到 %d 次", got)
	}
	if got := watchFlag(t, b, chat, 773); got != 0 {
		t.Fatalf("有正常简介不该重点关注，prewarm_watch = %d", got)
	}
}

// fakeSweepChatProfiles 让 getChat 按 uid 返回含用户名的资料、
// getUserProfilePhotos 按 uid 返回头像数（未列出的 uid 都是空/0 张）。
func fakeSweepChatProfiles(t *testing.T, fake *testutil.FakeTG,
	profs map[int64]sweepProfile, photos map[int64]int) {
	t.Helper()
	fake.RespFunc = func(method string, p map[string]any) (string, bool) {
		switch method {
		case "getChat":
			uid := int64(p["chat_id"].(float64))
			pr := profs[uid]
			return fmt.Sprintf(
				`{"ok":true,"result":{"id":%d,"first_name":%q,"username":%q,"bio":%q}}`,
				uid, pr.first, pr.username, pr.bio), true
		case "getUserProfilePhotos":
			uid := int64(p["user_id"].(float64))
			return fmt.Sprintf(
				`{"ok":true,"result":{"total_count":%d,"photos":[]}}`, photos[uid]), true
		}
		return "", false
	}
}

// 探测路径的空壳组合：全齐零 AI 处置；部分命中标记关注并压到 5min 档；
// 资料补齐后关注自动解除。
func TestPrewarmSweepShell(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	now := time.Now().Unix()
	// 681：四条件齐备 → 零 AI 禁言。
	addSweepMember(t, b, chat, 681, 2*3600, 0)
	setSweepSchedule(t, b, chat, 681, now-600, now-1, "")
	// 682：随机用户名+无简介，但有头像 → 重点关注。
	addSweepMember(t, b, chat, 682, 2*3600, 0)
	setSweepSchedule(t, b, chat, 682, now-600, now-1, "")
	profs := map[int64]sweepProfile{
		681: {first: "在线服务", username: "tpiw33abik"},
		682: {first: "小明", username: "tpiw33abik"},
	}
	fakeSweepChatProfiles(t, fake, profs,
		map[int64]int{681: 0, 682: 2}) // 681 无头像，682 有头像

	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("探测路径零 AI，跑了 %d 次", n)
	}
	if rec, ok := loadJoinMute(b.Store, chat, 681); !ok || rec.Kind != kindPrewarm {
		t.Fatalf("681 应被零 AI 处置：(%v,%q)", ok, rec.Kind)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 681); hash == "" {
		t.Error("681 处置后应落指纹")
	}
	if got := watchFlag(t, b, chat, 681); got != 0 {
		t.Errorf("681 已处置不该标记关注，prewarm_watch = %d", got)
	}
	if got := watchFlag(t, b, chat, 682); got != 1 {
		t.Fatalf("682 部分命中应标记重点关注，prewarm_watch = %d", got)
	}
	// 681/682 都按 5min 档预排（2h 年龄在关注档内）。
	for _, uid := range []int64{681, 682} {
		_, next, _ := sweepSchedule(t, b, chat, uid)
		if next < now+int64(4*time.Minute/time.Second) ||
			next > now+int64(6*time.Minute/time.Second) {
			t.Errorf("%d 的 next_at 应落在 5min 档附近，得到 %d", uid, next)
		}
	}

	// 682 补齐资料（写了正常简介）：关注解除，走普通首查（预筛干净 →
	// 零 AI 落指纹）。
	setSweepSchedule(t, b, chat, 682, now-600, now-1, "")
	profs[682] = sweepProfile{first: "小明", username: "tpiw33abik", bio: "喜欢摄影"}
	runPrewarmProbes(t, b.Shared)
	waitIdle(t, b)
	if got := watchFlag(t, b, chat, 682); got != 0 {
		t.Fatalf("资料补齐后应解除关注，prewarm_watch = %d", got)
	}
	if _, _, hash := sweepSchedule(t, b, chat, 682); hash == "" {
		t.Error("682 解除关注后应按普通首查落指纹")
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Errorf("682 的资料始终干净，不该花 AI，跑了 %d 次", n)
	}
}
