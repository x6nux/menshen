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

// setupPrewarmSweep 用真 Registry 起装：PrewarmSweep 以 sh.Reg 遍历 bot，
// 普通 NewTestBot 没有 Registry，整轮会被 nil 守卫跳过。返回的计数器用来
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

// setSweepChecked 预置上次复查时间与资料指纹。
func setSweepChecked(t *testing.T, b *core.Bot, chat, uid, at int64, hash string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`UPDATE group_members
		SET prewarm_checked_at=?, profile_hash=? WHERE chat_id=? AND user_id=?`,
		at, hash, chat, uid); err != nil {
		t.Fatal(err)
	}
}

// sweepCheckedAt 读某人的上次复查时间与指纹。
func sweepCheckedAt(t *testing.T, b *core.Bot, chat, uid int64) (int64, string) {
	t.Helper()
	var at int64
	var hash string
	if err := b.Store.Read.QueryRow(`SELECT prewarm_checked_at,profile_hash
		FROM group_members WHERE chat_id=? AND user_id=?`, chat, uid).
		Scan(&at, &hash); err != nil {
		t.Fatal(err)
	}
	return at, hash
}

// sweepProfile 是假 getChat 给某个 uid 返回的资料。
type sweepProfile struct {
	first string
	bio   string
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

// 选人、跳过的各条路径与重复 sweep 的幂等：老成员按 24h 轮转、新成员按
// 10min 节流；白名单不选、join_mutes 只标记；首查干净资料零 AI。
func TestPrewarmSweepSelectionAndIdempotency(t *testing.T) {
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	now := time.Now().Unix()
	addSweepMember(t, b, chat, 601, 25*3600, 0) // 老成员首查，可疑简介 → AI 命中禁言
	addSweepMember(t, b, chat, 602, 25*3600, 0) // 老成员 1 小时前查过：24h 内不查
	setSweepChecked(t, b, chat, 602, now-3600, "stale602")
	addSweepMember(t, b, chat, 603, 25*3600, 0) // 老成员 25 小时前查过：到期复查
	setSweepChecked(t, b, chat, 603, now-25*3600,
		profileHash(senderProfile{Bio: "喜欢摄影"})) // 指纹未变 → 零 AI
	addSweepMember(t, b, chat, 604, 25*3600, 1) // 群白名单：不选
	addSweepMember(t, b, chat, 605, 2*3600, 0)  // 新成员 5 分钟前查过：10min 内不查
	setSweepChecked(t, b, chat, 605, now-300, "stale605")
	addSweepMember(t, b, chat, 606, 2*3600, 0) // 新成员首查，干净资料 → 零 AI，只落指纹
	addSweepMember(t, b, chat, 607, 25*3600, 0)
	saveJoinMute(b, chat, 607, kindPrewarm, "已有前置号限制", 0) // 已在禁言：只标记
	addSweepMember(t, b, chat, 608, -1, 0)                // joined_at=0 的存量成员：进老成员轮转

	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		601: {bio: "免押小额洗资：https://t.me/+abcdef"},
		603: {bio: "喜欢摄影"},
		606: {bio: "喜欢摄影"},
		608: {bio: "喜欢摄影"},
	})

	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("应只禁 601 一个人，得到 %d 次", got)
	}
	for _, uid := range []int64{601, 603, 606, 607, 608} {
		if at, _ := sweepCheckedAt(t, b, chat, uid); at == 0 {
			t.Errorf("uid %d 应被标记已查", uid)
		}
	}
	// 首查干净：指纹落库，但一个 AI 都没跑（601 的 AI 由假上游断言）。
	for _, uid := range []int64{601, 606, 608} {
		if _, hash := sweepCheckedAt(t, b, chat, uid); hash == "" {
			t.Errorf("uid %d 首查后应落资料指纹", uid)
		}
	}
	// 602（24h 内）、604（白名单）、605（10min 内）不该被碰。
	if at, hash := sweepCheckedAt(t, b, chat, 602); at != now-3600 || hash != "stale602" {
		t.Errorf("602 24h 内不该复查：at/hash = %d/%q", at, hash)
	}
	if at, hash := sweepCheckedAt(t, b, chat, 604); at != 0 || hash != "" {
		t.Errorf("604 白名单不该被选：at/hash = %d/%q", at, hash)
	}
	if at, hash := sweepCheckedAt(t, b, chat, 605); at != now-300 || hash != "stale605" {
		t.Errorf("605 10min 内不该复查：at/hash = %d/%q", at, hash)
	}
	if _, hash := sweepCheckedAt(t, b, chat, 607); hash != "" {
		t.Errorf("join_mutes 跳过只推时间，不该动指纹，得到 %q", hash)
	}

	// 再跑一轮：全部都在各自档位的间隔内，不产生新动作。
	checks := map[int64][2]any{}
	for _, uid := range []int64{601, 602, 603, 604, 605, 606, 607, 608} {
		at, hash := sweepCheckedAt(t, b, chat, uid)
		checks[uid] = [2]any{at, hash}
	}
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 1 {
		t.Fatalf("重复 sweep 不应再禁，得到 %d 次", got)
	}
	for uid, want := range checks {
		at, hash := sweepCheckedAt(t, b, chat, uid)
		if at != want[0] || hash != want[1] {
			t.Errorf("uid %d 在间隔内被改写了：(%v,%v) -> (%d,%q)",
				uid, want[0], want[1], at, hash)
		}
	}
	if rec, ok := loadJoinMute(b.Store, chat, 607); !ok || rec.Kind != kindPrewarm {
		t.Fatalf("607 的 kind = %q,%v，应保持 prewarm", rec.Kind, ok)
	}
}

// 新成员 10 分钟节流：查过一次后，间隔内再跑 sweep 连资料都不拉；
// 超过 10 分钟且指纹未变时只推时间、不花 AI（§9.2/§9.3）。
func TestPrewarmSweepNewMemberHighFrequency(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	addSweepMember(t, b, chat, 620, 2*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{620: {bio: "喜欢摄影"}})

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	at1, hash1 := sweepCheckedAt(t, b, chat, 620)
	if at1 == 0 || hash1 == "" {
		t.Fatalf("新成员首查应落时间与指纹：at=%d hash=%q", at1, hash1)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("首查干净资料不该花 AI，跑了 %d 次", n)
	}
	getChat1 := fake.CountCalls("getChat")

	// 10 分钟内：不该再被选中。
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if at, _ := sweepCheckedAt(t, b, chat, 620); at != at1 {
		t.Fatalf("10 分钟内不该复查：at %d -> %d", at1, at)
	}
	if got := fake.CountCalls("getChat"); got != getChat1 {
		t.Fatalf("10 分钟内不该拉资料：getChat %d -> %d", getChat1, got)
	}

	// 超过 10 分钟：重新拉资料；指纹未变，只推时间不花 AI。
	stale := time.Now().Unix() - int64(prewarmNewInterval/time.Second) - 60
	setSweepChecked(t, b, chat, 620, stale, hash1)
	fake.Reset()
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	at2, hash2 := sweepCheckedAt(t, b, chat, 620)
	if at2 <= stale {
		t.Fatalf("超过 10 分钟应重新复查：at %d（stale=%d）", at2, stale)
	}
	if hash2 != hash1 {
		t.Fatalf("指纹未变不该改写：%q -> %q", hash1, hash2)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("指纹未变不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("getChat"); got != 1 {
		t.Fatalf("超过 10 分钟应重新拉一次资料，得到 %d 次 getChat", got)
	}
}

// 老成员每日节流（含 joined_at=0 的存量成员）：24h 内不查、超过 24h 再查；
// 指纹未变只推时间不花 AI。
func TestPrewarmSweepOldMemberDaily(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"), "")
	addSweepMember(t, b, chat, 630, 25*3600, 0) // 入群 25h
	addSweepMember(t, b, chat, 631, -1, 0)      // joined_at=0 的存量成员
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		630: {bio: "喜欢摄影"}, 631: {bio: "喜欢摄影"},
	})

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	at630, hash630 := sweepCheckedAt(t, b, chat, 630)
	at631, _ := sweepCheckedAt(t, b, chat, 631)
	if at630 == 0 || at631 == 0 {
		t.Fatalf("两个老成员（含 joined_at=0）都应被首查：630 at=%d，631 at=%d",
			at630, at631)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("首查干净资料不该花 AI，跑了 %d 次", n)
	}

	// 24h 内：不复查。
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if at, _ := sweepCheckedAt(t, b, chat, 630); at != at630 {
		t.Fatalf("24h 内不该复查 630：%d -> %d", at630, at)
	}
	if at, _ := sweepCheckedAt(t, b, chat, 631); at != at631 {
		t.Fatalf("24h 内不该复查 631：%d -> %d", at631, at)
	}

	// 630 的上次复查挪到 25h 前：到期复查；631 仍在 24h 内。
	stale := time.Now().Unix() - int64(prewarmDailyInterval/time.Second) - 3600
	setSweepChecked(t, b, chat, 630, stale, hash630)
	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if at, hash := sweepCheckedAt(t, b, chat, 630); at <= stale || hash != hash630 {
		t.Fatalf("超过 24h 应复查 630 且指纹不变：at=%d hash=%q", at, hash)
	}
	if at, _ := sweepCheckedAt(t, b, chat, 631); at != at631 {
		t.Fatalf("631 还在 24h 内不该复查：%d -> %d", at631, at)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("指纹未变不该花 AI，跑了 %d 次", n)
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
	stale := time.Now().Unix() - 25*3600
	setSweepChecked(t, b, chat, 640, stale, h)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{640: {bio: bio}})

	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("指纹未变不该禁言，得到 %d 次", got)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("指纹未变不该花 AI，跑了 %d 次", n)
	}
	if at, hash := sweepCheckedAt(t, b, chat, 640); at <= stale || hash != h {
		t.Fatalf("只该推时间、不动指纹：at=%d hash=%q", at, hash)
	}
}

// 首查且本地预筛不中：零 AI，只落指纹。
func TestPrewarmSweepFirstSightCleanNoAI(t *testing.T) {
	b, fake, chat, soN, llmN := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 641, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{641: {bio: "喜欢摄影"}})

	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("首查正常资料不该花 AI，跑了 %d 次", n)
	}
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("不该禁言，得到 %d 次", got)
	}
	if at, hash := sweepCheckedAt(t, b, chat, 641); at == 0 || hash == "" {
		t.Fatalf("首查应落时间与指纹：at=%d hash=%q", at, hash)
	}
}

// 首查可疑资料（本地预筛命中）→ 冷判定；命中即禁言并写指纹。
func TestPrewarmSweepFirstSightSuspiciousJudges(t *testing.T) {
	b, fake, chat, soN, _ := setupPrewarmSweep(t,
		soReply("ad", 0.96, "promo", "account"),
		llmReply(true, 0.95, "promo", "account"))
	addSweepMember(t, b, chat, 642, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{642: {bio: "免押小额洗钱"}})

	PrewarmSweep(b.Shared)
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
	if at, hash := sweepCheckedAt(t, b, chat, 642); at == 0 || hash == "" {
		t.Fatalf("应落时间与指纹：at=%d hash=%q", at, hash)
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
	setSweepChecked(t, b, chat, 643, time.Now().Unix()-25*3600,
		profileHash(senderProfile{FirstName: "Robert Williamson"}))
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{
		643: {first: "手机拍违停 一百圆/张"},
	})

	PrewarmSweep(b.Shared)
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
	setSweepChecked(t, b, chat, 608, time.Now().Unix()-25*3600,
		profileHash(senderProfile{Bio: "旧资料"}))
	// 指纹只看用户名/昵称/简介；这里三项与 prewarmRecheck 组装的一致。
	if GrantProfileOK(b, senderProfile{UserID: 608, Bio: bio}, 6, "测试放行") == 0 {
		t.Fatal("测试前置放行失败")
	}

	PrewarmSweep(b.Shared)
	waitIdle(t, b)
	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("资料已放行的不该禁言，得到 %d 次", got)
	}
	if n := soN.Load() + llmN.Load(); n != 0 {
		t.Fatalf("放行资料不该送 AI，跑了 %d 次", n)
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
	b, fake, chat, _, _ := setupPrewarmSweep(t,
		soReply("clean", 0.9, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	addSweepMember(t, b, chat, 610, 25*3600, 0)
	// 预置旧指纹（资料变过）→ 走冷判定；首查干净资料现在是零 AI 路径，
	// 由 TestPrewarmSweepFirstSightCleanNoAI 覆盖。
	setSweepChecked(t, b, chat, 610, time.Now().Unix()-25*3600,
		profileHash(senderProfile{Bio: "旧资料"}))
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{610: {bio: "喜欢摄影"}})

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
	b, fake, chat, _, _ := setupPrewarmSweep(t, soReply("ad", 0.99, "promo", "account"), "")
	addSweepMember(t, b, chat, 601, 25*3600, 0)
	fakeSweepProfiles(t, fake, map[int64]sweepProfile{601: {bio: "洗钱"}})
	if err := b.PutSetting("antiad_enabled", "0"); err != nil {
		t.Fatal(err)
	}
	PrewarmSweep(b.Shared)
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

	PrewarmSweep(b.Shared)
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

	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	if got := fake.CountCalls("restrictChatMember"); got != 0 {
		t.Fatalf("豁免用户不该禁言，得到 %d 次", got)
	}
	if got := fake.CountCalls("getChat"); got != 0 {
		t.Fatalf("豁免用户不该拉资料，得到 %d 次 getChat", got)
	}
	if at, hash := sweepCheckedAt(t, b, chat, 614); at == 0 || hash != "" {
		t.Fatalf("豁免跳过应只推时间、不落指纹：at=%d hash=%q", at, hash)
	}
}

// 每 bot 每轮的复查数有上限：群多时 2 档 × 20 × N 会一次性灌满共享判定
// 队列，饿死实时消息判定。三个群各 25 个老成员候选（3×20 > 60）逼出上限。
func TestPrewarmSweepPerBotCap(t *testing.T) {
	b, fake, _, _, _ := setupPrewarmSweep(t, "", "")
	chats := []int64{-100, -101, -102}
	for _, c := range chats {
		if c != -100 {
			testutil.EnableAntiad(t, b, c)
		}
		for i := 0; i < prewarmSweepBatch+5; i++ {
			addSweepMember(t, b, c, int64(7000+i), 25*3600, 0)
		}
	}
	fakeSweepProfiles(t, fake, nil)

	// 资料为空：首查只落空指纹不送检，60 条上限跑得很快。
	PrewarmSweep(b.Shared)
	waitIdle(t, b)

	got := countRows(t, b, `SELECT COUNT(*) FROM group_members
		WHERE prewarm_checked_at > 0`)
	if got != prewarmSweepPerBot {
		t.Fatalf("每 bot 每轮应只提交 %d 条复查，得到 %d", prewarmSweepPerBot, got)
	}
	for _, c := range chats {
		n := countRows(t, b, `SELECT COUNT(*) FROM group_members
			WHERE chat_id=? AND prewarm_checked_at > 0`, c)
		// 本用例全是老成员：单群单档上限即 prewarmSweepBatch。
		if n > prewarmSweepBatch {
			t.Errorf("群 %d 单档超过每群批量 %d：%d", c, prewarmSweepBatch, n)
		}
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

	PrewarmSweep(b.Shared)
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

	PrewarmSweep(b.Shared)
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
