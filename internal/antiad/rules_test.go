package antiad

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// insertRule 往库里插一条必封规则并重建快照，返回规则 id。
func insertRule(t *testing.T, b *core.Bot, name, pattern, category string,
	enabled, enforce bool) int64 {
	t.Helper()
	en, enf := int64(0), int64(0)
	if enabled {
		en = 1
	}
	if enforce {
		enf = 1
	}
	res, err := b.Store.Write.Exec(`INSERT INTO ad_rules
		(name,pattern,category,note,source,enabled,enforce,created_at,created_by)
		VALUES (?,?,?,'','ai',?,?,?,1)`,
		name, pattern, category, en, enf, time.Now().Unix())
	if err != nil {
		t.Fatalf("插入必封规则失败: %v", err)
	}
	id, _ := res.LastInsertId()
	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return id
}

// insertLog 往 antiad_log 插一行测试流水。
func insertLog(t *testing.T, b *core.Bot, verdict, action, text string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,0,?,?,0.9,'test','promo',?,'',?,?)`,
		text, verdict, action, time.Now().Unix(), b.BotID()); err != nil {
		t.Fatalf("插入流水失败: %v", err)
	}
}

// TestRuleTestPatternClassification 锁住全库测试的分类口径与样本上限：
// ad 计 TP；clean/none 计 FP；action=undone 计 Undone 且同时计 FP
// （它就是被撤销的误判）；skipped/error 计 Neutral；样本按上限截取，
// 正文截到 200 字符。
func TestRuleTestPatternClassification(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	longText := "买号加微信" + strings.Repeat("广", 300)

	// 35 条已确认广告：TP 计数不封顶，样本只留 30 条。
	for i := 0; i < 35; i++ {
		insertLog(t, b, "ad", "deleted", longText)
	}
	// 31 条判过正常的：FP。
	for i := 0; i < 31; i++ {
		insertLog(t, b, "clean", "none", "买号加微信只是普通聊天")
	}
	// 12 条被撤销的处罚：Undone，同时计入 FP。
	for i := 0; i < 12; i++ {
		insertLog(t, b, "ad", "undone", "买号加微信（误封已撤销）")
	}
	// 未判定：Neutral，不计 FP。
	insertLog(t, b, "skipped", "none", "买号加微信但被护栏拦下")
	insertLog(t, b, "skipped", "none", "买号加微信但队列已满")
	insertLog(t, b, "error", "none", "买号加微信但判定失败")
	// 不命中。
	insertLog(t, b, "clean", "none", "今天天气不错")

	res, err := TestRulePattern(b.Shared, "买号加微信")
	if err != nil {
		t.Fatalf("全库测试失败: %v", err)
	}
	if res.Scanned != 82 || res.Matched != 81 {
		t.Errorf("scanned/matched = %d/%d，期望 82/81", res.Scanned, res.Matched)
	}
	if res.TP != 35 {
		t.Errorf("tp = %d，期望 35", res.TP)
	}
	if res.FP != 43 {
		t.Errorf("fp = %d，期望 43（31 clean + 12 undone）", res.FP)
	}
	if res.Undone != 12 {
		t.Errorf("undone = %d，期望 12", res.Undone)
	}
	if res.Neutral != 3 {
		t.Errorf("neutral = %d，期望 3", res.Neutral)
	}
	if len(res.TPSamples) != 30 || len(res.FPSamples) != 30 ||
		len(res.UndoneSamples) != 10 {
		t.Errorf("样本上限不对：tp=%d fp=%d undone=%d，期望 30/30/10",
			len(res.TPSamples), len(res.FPSamples), len(res.UndoneSamples))
	}
	for _, s := range res.TPSamples {
		if got := len([]rune(s.Text)); got != 200 {
			t.Errorf("样本正文应截到 200 字符，得到 %d", got)
		}
	}

	// 校验：空串、编译失败、超长都要拒绝。
	if _, err := TestRulePattern(b.Shared, ""); err == nil {
		t.Error("空规则应被拒绝")
	}
	if _, err := TestRulePattern(b.Shared, "["); err == nil {
		t.Error("无法编译的正则应被拒绝")
	}
	if _, err := TestRulePattern(b.Shared, strings.Repeat("a", 501)); err == nil {
		t.Error("超过 500 字符的规则应被拒绝")
	}
}

// TestRuleEnforceBansWithoutAI：enforce 规则命中即按最高档处置，
// 不产生任何 AI 调用，流水 decider 记 rule:<id>，命中计数 +1。
func TestRuleEnforceBansWithoutAI(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	id := insertRule(t, b, "必封测试规则", "必封测试", "promo", true, true)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.93, "scam", "message"),
		llmReply(true, 0.9, "scam", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这里有必封测试内容"))
	waitIdle(t, b)

	if soN.Load() != 0 || llmN.Load() != 0 {
		t.Fatalf("规则门不该产生 AI 调用，得到 so=%d llm=%d",
			soN.Load(), llmN.Load())
	}
	if n := fake.CountCalls("deleteMessage"); n != 1 {
		t.Errorf("应删除 1 条消息，得到 %d", n)
	}
	if n := fake.CountCalls("restrictChatMember"); n != 1 {
		t.Errorf("应禁言 1 次，得到 %d", n)
	}
	var action, decider, kind, reason string
	if err := b.Store.Read.QueryRow(`SELECT action,decider,ad_kind,reason
		FROM antiad_log WHERE chat_id=-100 AND user_id=555
		ORDER BY id DESC LIMIT 1`).Scan(&action, &decider, &kind, &reason); err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if action != "deleted_muted" || decider != "rule:"+itoa(id) || kind != "promo" {
		t.Errorf("流水字段不对：action=%q decider=%q kind=%q",
			action, decider, kind)
	}
	if !strings.Contains(reason, "命中必封规则《必封测试规则》") {
		t.Errorf("流水理由缺少规则名：%q", reason)
	}
	var hits, lastMatched int64
	if err := b.Store.Read.QueryRow(
		`SELECT hits,last_matched FROM ad_rules WHERE id=?`, id).
		Scan(&hits, &lastMatched); err != nil {
		t.Fatalf("读规则计数失败: %v", err)
	}
	if hits != 1 || lastMatched == 0 {
		t.Errorf("命中计数 = %d / last_matched=%d，期望 1 / >0", hits, lastMatched)
	}
}

// TestRuleDryrunOnlyLogs：演练群命中 enforce 规则只落流水，不动群里的人；
// action 名带 dryrun: 前缀（与其余处置同一口径）。
func TestRuleDryrunOnlyLogs(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	id := insertRule(t, b, "演练规则", "演练命中", "", true, true)

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "演练命中内容"))
	waitIdle(t, b)

	for _, method := range []string{"deleteMessage", "restrictChatMember", "banChatMember"} {
		if n := fake.CountCalls(method); n != 0 {
			t.Errorf("演练模式不该调用 %s，得到 %d 次", method, n)
		}
	}
	var action, decider string
	if err := b.Store.Read.QueryRow(`SELECT action,decider FROM antiad_log
		WHERE chat_id=-100 ORDER BY id DESC LIMIT 1`).
		Scan(&action, &decider); err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if action != "dryrun:deleted_muted" {
		t.Errorf("演练 action = %q，期望 dryrun:deleted_muted", action)
	}
	if decider != "rule:"+itoa(id) {
		t.Errorf("decider = %q，期望 rule:%d", decider, id)
	}
}

// TestRuleWhitelistExempt：白名单里的人与管理员即使命中 enforce 规则也不处置——
// 规则门排在 adExempt 之后。
func TestRuleWhitelistExempt(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	insertRule(t, b, "豁免规则", "豁免命中", "", true, true)
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_whitelist
		(bot_id,chat_id,user_id,expires_at,source,by_uid,created_at)
		VALUES (?,?,?,0,'adw',1,?)`,
		b.BotID(), int64(-100), int64(555), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}

	// 白名单成员。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "豁免命中内容"))
	// 主管理员（uid=1）。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 1, 2, "豁免命中内容"))
	waitIdle(t, b)

	for _, method := range []string{"deleteMessage", "restrictChatMember", "banChatMember"} {
		if n := fake.CountCalls(method); n != 0 {
			t.Errorf("豁免者不该被 %s，得到 %d 次", method, n)
		}
	}
	var n int
	if err := b.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM antiad_log WHERE decider LIKE 'rule:%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("豁免者不该产生规则流水，得到 %d 条", n)
	}
}

// TestRuleHintInjectedIntoPrompt：非 enforce 规则命中时不处置，但规则名
// 要出现在送给 systemone 的 prompt 里（known_ad_patterns）。
func TestRuleHintInjectedIntoPrompt(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	insertRule(t, b, "提示测试规则", "提示命中词", "", true, false)

	var req atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			raw, _ := io.ReadAll(r.Body)
			req.Store(string(raw))
			w.Write([]byte(soReply("clean", 0.9, "none", "message")))
			return
		}
		w.Write([]byte(llmReply(false, 0.9, "none", "message")))
	})

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这条包含提示命中词"))
	waitIdle(t, b)

	raw, _ := req.Load().(string)
	if raw == "" {
		t.Fatal("没有向 systemone 发请求")
	}
	if !strings.Contains(raw, "命中必封规则") || !strings.Contains(raw, "提示测试规则") {
		t.Errorf("prompt 里没有注入规则提示：%s", raw)
	}
}
