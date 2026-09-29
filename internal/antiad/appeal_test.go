package antiad

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

// appealAI 造一个只回答申诉复判的假上游。
func appealAI(t *testing.T, b *core.Bot, uphold bool, reason string) {
	t.Helper()
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		body := fmt.Sprintf(
			`{"choices":[{"message":{"content":"{\"uphold\":%t,\"confidence\":0.9,\"reason\":\"%s\"}"}}]}`,
			uphold, reason)
		w.Write([]byte(body))
	})
}

func appealDM(uid int64) *tg.Message {
	return &tg.Message{MessageID: 1, From: &tg.TGUser{ID: uid},
		Chat: &tg.Chat{ID: uid, Type: "private"}}
}

func appealGo(uid int64) *tg.CallbackQuery {
	return &tg.CallbackQuery{ID: "cb", Data: "a:ap:go", From: &tg.TGUser{ID: uid},
		Message: &tg.Message{MessageID: 5, Chat: &tg.Chat{ID: uid, Type: "private"}}}
}

func appealStatus(t *testing.T, b *core.Bot, uid int64) string {
	t.Helper()
	var st string
	if err := b.Store.Read.QueryRow(
		`SELECT status FROM appeals WHERE bot_id=? AND user_id=? ORDER BY id DESC LIMIT 1`,
		b.BotID(), uid).Scan(&st); err != nil {
		t.Fatalf("读申诉单失败: %v", err)
	}
	return st
}

// TestAppealEntryAcceptsLogPayload：群内告警的按钮带的是 start=log<记录号>
// —— 管理员点进记录卡片，普通用户进申诉入口。非管理员的这条 payload 必须
// 被申诉入口接管，而不是被当成陌生参数放回去。
func TestAppealEntryAcceptsLogPayload(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)

	if !HandleNonStaffPrivate(b, appealDM(555), "/start log12") {
		t.Fatal("log payload 应被申诉入口接管")
	}
	last := fake.LastCall("sendMessage")
	if last == nil || !strings.Contains(last["text"].(string), "没有被本 bot 限制") {
		t.Errorf("没有限制时应明确告知，得到 %v", last)
	}

	// 未知 payload 不接管：可能是别的处理器或未来格式的入口。
	if HandleNonStaffPrivate(b, appealDM(555), "/start somethingelse") {
		t.Error("未知 payload 不该被接管")
	}
}

// TestAppealAIOverturnsAndLifts：AI 撤销原判时直接解除（含冷判定禁言），
// 给申诉人回执并给归属人推简报卡片；申诉开销记在申诉单上，不污染判定账本。
func TestAppealAIOverturnsAndLifts(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	appealAI(t, b, false, "资料已改正")
	saveJoinMute(b, -100, 555, "简介里有联系方式", 88)

	if !HandleNonStaffPrivate(b, appealDM(555), "/start "+unbanPayload(1)) {
		t.Fatal("申诉入口应被接管")
	}
	HandleAppealCallback(b, appealGo(555))
	waitIdle(t, b)

	if _, still := loadJoinMute(b.Store, -100, 555); still {
		t.Error("AI 撤销后应删掉 join_mutes 记录")
	}
	if n := fake.CountCalls("restrictChatMember"); n == 0 {
		t.Error("AI 撤销后应恢复发言权限")
	}
	if st := appealStatus(t, b, 555); st != "lifted" {
		t.Errorf("申诉单应进入 lifted，得到 %q", st)
	}
	if n := fake.CountCalls("sendMessage"); n < 2 {
		t.Errorf("应给申诉人回执并给归属人推卡片，实际发了 %d 条", n)
	}
	// 开销记在申诉单上，不进 antiad_log（那是判定账本，形态总结从里面取样）。
	var cost int64
	if err := b.Store.Read.QueryRow(
		`SELECT ai_cost FROM appeals WHERE user_id=555`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	var logs int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM antiad_log`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 0 {
		t.Errorf("申诉不该写判定流水，实际 %d 行", logs)
	}
}

// TestAppealUpholdFallsBackToNoWeb：AI 维持原判、网页不可用时进入 noweb，
// 告知申诉人联系群管理员，并给归属人推卡片。
func TestAppealUpholdFallsBackToNoWeb(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	appealAI(t, b, true, "仍是广告")
	saveJoinMute(b, -100, 555, "简介里有联系方式", 88)

	HandleNonStaffPrivate(b, appealDM(555), "/start "+unbanPayload(1))
	HandleAppealCallback(b, appealGo(555))
	waitIdle(t, b)

	if st := appealStatus(t, b, 555); st != "noweb" {
		t.Errorf("没有网页时 AI 维持应进入 noweb，得到 %q", st)
	}
	// 卡片会随后推给归属人，所以按收件人找给申诉人的那条回执。
	found := false
	for _, c := range fake.Calls("sendMessage") {
		if c["chat_id"] != float64(555) {
			continue
		}
		if text, _ := c["text"].(string); strings.Contains(text, "群管理员") {
			found = true
		}
	}
	if !found {
		t.Error("noweb 应告诉申诉人联系群管理员")
	}
}

// TestAppealUpholdGoesToWeb：配好 public_url 与 Turnstile 密钥时，
// AI 维持原判后进入 web 并私聊验证链接。
func TestAppealUpholdGoesToWeb(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	appealAI(t, b, true, "仍是广告")
	saveJoinMute(b, -100, 555, "简介里有联系方式", 88)
	b.Cfg.PublicURL = "https://ad.example.com"
	b.Cfg.TurnstileSiteKey, b.Cfg.TurnstileSecret = "site", "secret"
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}

	HandleNonStaffPrivate(b, appealDM(555), "/start "+unbanPayload(1))
	HandleAppealCallback(b, appealGo(555))
	waitIdle(t, b)

	if st := appealStatus(t, b, 555); st != "web" {
		t.Fatalf("应进入 web，得到 %q", st)
	}
	last := fake.LastCall("sendMessage")
	text, _ := last["text"].(string)
	if !strings.Contains(text, "/_w/ap/") {
		t.Errorf("应私聊验证链接:\n%s", text)
	}
}

// TestAppealGateBlocksRetry：退避期内再发起申诉会被挡下，且不建新单。
func TestAppealGateBlocksRetry(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, "简介里有联系方式", 88)
	unbanGateBump(b.Shared, 555)

	HandleAppealCallback(b, appealGo(555))

	last := fake.LastCall("sendMessage")
	if last == nil {
		t.Fatal("应回一条说明")
	}
	if text, _ := last["text"].(string); !strings.Contains(text, "请求太频繁") {
		t.Errorf("退避期内应告知等待时间:\n%s", text)
	}
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeals`).Scan(&n)
	if n != 0 {
		t.Errorf("被闸门挡下时不该建申诉单，实际 %d 张", n)
	}
}

// TestAppealSingleOpen：同一人同一 bot 只能有一张未结单；再次 /start
// 展示的是进度（web 状态重发链接），不是重新发起。
func TestAppealSingleOpen(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	if err := EnsureWebSecret(b.Shared); err != nil {
		t.Fatal(err)
	}
	b.Cfg.PublicURL = "https://ad.example.com"
	now := int64(1700000000)
	if _, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,web_since,created_at,updated_at)
		VALUES (?,?, 'web', ?, ?, ?)`, b.BotID(), 555, now, now, now); err != nil {
		t.Fatal(err)
	}
	saveJoinMute(b, -100, 555, "简介里有联系方式", 88)

	HandleNonStaffPrivate(b, appealDM(555), "/start "+unbanPayload(1))

	last := fake.LastCall("sendMessage")
	text, _ := last["text"].(string)
	if !strings.Contains(text, "/_w/ap/") {
		t.Errorf("已有未结单时应重发验证链接:\n%s", text)
	}
	if kb := fmt.Sprint(last["reply_markup"]); strings.Contains(kb, "a:ap:st") {
		t.Error("已有未结单时不该再给「发起申诉」按钮")
	}
	var n int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeals WHERE user_id=555`).Scan(&n)
	if n != 1 {
		t.Errorf("不该重复建单，实际 %d 张", n)
	}
}
