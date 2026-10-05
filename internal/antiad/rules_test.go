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
	"menshen/internal/tg"
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

// insertKindLog 与 insertLog 相同，但可指定 ad_kind。
func insertKindLog(t *testing.T, b *core.Bot, verdict, action, kind, text string) {
	t.Helper()
	if _, err := b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,ad_kind,
		 action,reason,created_at,bot_id)
		VALUES (-100,555,0,?,?,0.9,'test',?,?,'',?,?)`,
		text, verdict, kind, action, time.Now().Unix(), b.BotID()); err != nil {
		t.Fatalf("插入流水失败: %v", err)
	}
}

// TestRuleCoverageByKind 锁覆盖率口径：分母是 verdict='ad' 且 action<>'undone'；
// 按 ad_kind 分组；undone/clean 不进分母；空库 coverage=0。
func TestRuleCoverageByKind(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertKindLog(t, b, "ad", "deleted", "scam", "办理贷款加微信")
	insertKindLog(t, b, "ad", "deleted", "scam", "办理贷款加微信")
	insertKindLog(t, b, "ad", "deleted", "scam", "今天天气不错")
	insertKindLog(t, b, "ad", "deleted", "promo", "限时促销")
	insertKindLog(t, b, "ad", "undone", "scam", "办理贷款（撤销）")
	insertKindLog(t, b, "clean", "none", "scam", "办理贷款（正常聊天）")

	res, err := TestRulePattern(b.Shared, "办理贷款")
	if err != nil {
		t.Fatal(err)
	}
	if res.AdsTotal != 4 || res.TP != 2 {
		t.Fatalf("覆盖率分子分母不对：AdsTotal=%d TP=%d", res.AdsTotal, res.TP)
	}
	if got := res.Coverage(); got != 0.5 {
		t.Errorf("总体覆盖率应为 0.5，得到 %v", got)
	}
	want := []RuleKindStat{
		{Kind: "scam", Total: 3, Matched: 2},
		{Kind: "promo", Total: 1, Matched: 0},
	}
	if len(res.Kinds) != len(want) {
		t.Fatalf("类型数应为 %d，得到 %+v", len(want), res.Kinds)
	}
	for i := range want {
		if res.Kinds[i] != want[i] {
			t.Errorf("类型[%d] 应为 %+v，得到 %+v", i, want[i], res.Kinds[i])
		}
	}
	if got := res.Kinds[1].Coverage(); got != 0 {
		t.Errorf("promo 覆盖率应为 0，得到 %v", got)
	}

	// 空库：不除零。
	b2, _ := testutil.NewTestBot(t, 2)
	empty, err := TestRulePattern(b2.Shared, "办理贷款")
	if err != nil {
		t.Fatal(err)
	}
	if empty.AdsTotal != 0 || empty.Coverage() != 0 || len(empty.Kinds) != 0 {
		t.Errorf("空库应 AdsTotal=0 coverage=0 kinds 空，得到 %+v", empty)
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
	// 空文本（纯图/贴纸）也在判定门的输入域里：语料不做 text != '' 过滤。
	insertLog(t, b, "ad", "deleted", "")
	insertLog(t, b, "clean", "none", "")

	res, err := TestRulePattern(b.Shared, "买号加微信")
	if err != nil {
		t.Fatalf("全库测试失败: %v", err)
	}
	if res.Scanned != 84 || res.Matched != 81 {
		t.Errorf("scanned/matched = %d/%d，期望 84/81", res.Scanned, res.Matched)
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

	// 空文本行参与分类与样本：`^$` 只匹配空文本，能把它显式测出来。
	// （test action 走宽松校验，这类草稿允许试跑。）
	empty, err := TestRulePattern(b.Shared, "^$")
	if err != nil {
		t.Fatalf("空文本测试失败: %v", err)
	}
	if empty.Matched != 2 || empty.TP != 1 || empty.FP != 1 {
		t.Errorf("空文本统计 = matched:%d tp:%d fp:%d，期望 2/1/1",
			empty.Matched, empty.TP, empty.FP)
	}
	if len(empty.TPSamples) != 1 || empty.TPSamples[0].Text != "" {
		t.Errorf("空文本应作为样本出现（Text 为空串）：%+v", empty.TPSamples)
	}

	// 宽严两档：TestRulePattern 只要求可编译；写入口的 CompileRulePattern
	// 还要拒绝能匹配空文本的规则（`a*` 编译得过，但命中一切）。
	if _, err := TestRulePattern(b.Shared, "a*"); err != nil {
		t.Errorf("test 试跑应允许匹配空文本的草稿，得到 %v", err)
	}
	if _, err := CompileRulePattern("a*"); err == nil ||
		!strings.Contains(err.Error(), "空文本") {
		t.Errorf("CompileRulePattern(a*) 应拒绝并说明空文本，得到 %v", err)
	}
	if _, err := CompileRulePattern(""); err == nil {
		t.Error("CompileRulePattern 应拒绝空规则")
	}
	if _, err := CompileRulePattern("["); err == nil {
		t.Error("CompileRulePattern 应拒绝无法编译的正则")
	}
	if _, err := CompileRulePattern(strings.Repeat("a", 501)); err == nil {
		t.Error("CompileRulePattern 应拒绝超过 500 字符的规则")
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

// TestRuleDisabledIgnored：禁用规则命中既不处置也不进 prompt ——
// 即便库里 enforce=1（候选态被手改），enabled=0 也必须压住它。
func TestRuleDisabledIgnored(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	insertRule(t, b, "禁用规则名", "禁用命中词", "promo", false, true)

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

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这条包含禁用命中词"))
	waitIdle(t, b)

	for _, method := range []string{"deleteMessage", "restrictChatMember", "banChatMember"} {
		if n := fake.CountCalls(method); n != 0 {
			t.Errorf("禁用规则不该调用 %s，得到 %d 次", method, n)
		}
	}
	var n int
	if err := b.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM antiad_log WHERE decider LIKE 'rule:%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("禁用规则不该产生规则流水，得到 %d 条", n)
	}
	raw, _ := req.Load().(string)
	if raw == "" {
		t.Fatal("没有向 systemone 发请求")
	}
	if strings.Contains(raw, "禁用规则名") {
		t.Errorf("禁用规则不该进 prompt：%s", raw)
	}
}

// TestMatchedRuleInfos：启用且非强制的规则命中会成为证据条目；
// enforce 规则命中即处置、不进证据（与旧 RuleHintText 的排除口径一致）。
func TestMatchedRuleInfos(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRule(t, b, "强制规则名", "强制命中词", "", true, true)
	if hits := MatchRules(b.Cache.Snap(), "强制命中词"); len(hits) != 1 {
		t.Fatalf("MatchRules 应命中 enforce 规则，得到 %d 条", len(hits))
	}
	if got := MatchedRuleInfos(b.Cache.Snap(), "强制命中词"); len(got) != 0 {
		t.Errorf("enforce 规则不该进证据，得到 %+v", got)
	}

	id := insertRule(t, b, "证据规则", "证据命中词", "scam", true, false)
	if _, err := b.Store.Write.Exec(`UPDATE ad_rules SET note='来自历史封禁' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	got := MatchedRuleInfos(b.Cache.Snap(), "这条包含证据命中词")
	if len(got) != 1 || got[0].ID != id || got[0].Name != "证据规则" ||
		got[0].Category != "scam" || got[0].Note != "来自历史封禁" {
		t.Errorf("证据条目字段不对：%+v", got)
	}
	if got := MatchedRuleInfos(b.Cache.Snap(), "没有命中的文本"); len(got) != 0 {
		t.Errorf("未命中不该有证据：%+v", got)
	}
}

// TestLoadAdRulesSkipsBadPattern：绕过写入口直写的坏正则，在 Reload 时
// 被 Warn 跳过；行本身保留，且不影响同表其它规则参与匹配。
func TestLoadAdRulesSkipsBadPattern(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRule(t, b, "好规则", "好规则命中", "", true, false)
	if _, err := b.Store.Write.Exec(`INSERT INTO ad_rules
		(name,pattern,category,note,source,enabled,enforce,created_at,created_by)
		VALUES ('坏规则','[','','','ai',1,1,?,1)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	if err := b.Cache.Reload(); err != nil {
		t.Fatalf("坏规则不该让 Reload 失败: %v", err)
	}
	snap := b.Cache.Snap()
	if len(snap.AdRules) != 1 || snap.AdRules[0].Name != "好规则" {
		t.Fatalf("坏规则应被跳过且不影响好规则，得到 %+v", snap.AdRules)
	}
	if hits := MatchRules(snap, "好规则命中"); len(hits) != 1 {
		t.Errorf("好规则仍应参与匹配，得到 %d 条", len(hits))
	}
	var n int
	if err := b.Store.Read.QueryRow(
		`SELECT COUNT(*) FROM ad_rules WHERE name='坏规则'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("坏规则行不应被删除（面板还要能改它），得到 %d 行", n)
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

// TestRuleHitPreDeleteReview：启用未强制的规则命中时跳过 systemone：
// 立即删除 + 临时禁言，规则作为 prior 直接交大模型复判；复判正常时解除
// 临时禁言，流水来源记 rule:<id>+llm。
func TestRuleHitPreDeleteReview(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	insertRule(t, b, "证据规则", "证据命中词", "scam", true, false)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.99, "scam", "message"),
		llmReply(false, 0.9, "none", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这条包含证据命中词"))
	waitIdle(t, b)

	if soN.Load() != 0 {
		t.Errorf("规则命中应跳过 systemone，实际调用 %d 次", soN.Load())
	}
	if llmN.Load() == 0 {
		t.Fatal("规则命中应过大模型复判")
	}
	if n := fake.CountCalls("deleteMessage"); n == 0 {
		t.Error("规则命中应立即删除")
	}
	// 先临时禁言、复判正常后解除：两次 restrictChatMember，其中一次权限全开。
	if n := fake.CountCalls("restrictChatMember"); n < 2 {
		t.Errorf("应先临时禁言再解除，实际 %d 次", n)
	}
	sawUnmute := false
	for _, p := range fake.Calls("restrictChatMember") {
		if perms, ok := p["permissions"].(map[string]any); ok &&
			perms["can_send_messages"] == true {
			sawUnmute = true
		}
	}
	if !sawUnmute {
		t.Error("复判正常应解除临时禁言")
	}
	var verdict, decider string
	if err := b.Store.Read.QueryRow(
		`SELECT verdict,decider FROM antiad_log ORDER BY id DESC LIMIT 1`).
		Scan(&verdict, &decider); err != nil {
		t.Fatal(err)
	}
	if verdict != "clean" || !strings.HasPrefix(decider, "rule:") ||
		!strings.HasSuffix(decider, "+llm") {
		t.Errorf("流水应记 rule:*+llm 的 clean，得到 verdict=%q decider=%q",
			verdict, decider)
	}

	// 对照：没有规则命中时高置信初判照旧直接采信（不调大模型）。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	soN2, llmN2 := fakeAIWith(t, b2, soReply("clean", 0.99, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 555, 1, "普通消息"))
	waitIdle(t, b2)
	if soN2.Load() == 0 || llmN2.Load() != 0 {
		t.Errorf("无规则命中时高置信初判不该复判，so=%d llm=%d", soN2.Load(), llmN2.Load())
	}
}

// TestRuleHitInjectedIntoPrompt：规则命中时不跑 systemone，规则以
// matched_rules 强证据出现在送给大模型的复判载荷里。
func TestRuleHitInjectedIntoPrompt(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	insertRule(t, b, "提示测试规则", "提示命中词", "", true, false)

	var req atomic.Value
	fakeAI(t, b, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			t.Error("规则命中不该请求 systemone")
			return
		}
		raw, _ := io.ReadAll(r.Body)
		req.Store(string(raw))
		w.Write([]byte(llmReply(false, 0.9, "none", "message")))
	})

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这条包含提示命中词"))
	waitIdle(t, b)

	raw, _ := req.Load().(string)
	if raw == "" {
		t.Fatal("没有向大模型发复判请求")
	}
	if !strings.Contains(raw, "matched_rules") || !strings.Contains(raw, "提示测试规则") {
		t.Errorf("复判载荷里没有规则证据：%s", raw)
	}
}

// TestRuleHitNoLLMFallsBackToMatrix：没有复判模型时，规则命中仍然先删 +
// 临时禁言，并按处置矩阵定案（来源记 rule:<id>）。
func TestRuleHitNoLLMFallsBackToMatrix(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("clean", 0.99, "none", "message"),
		llmReply(false, 0.9, "none", "message"))
	// 清掉复判模型：只剩 systemone（而规则命中不该跑它）。
	if err := b.PutSetting("antiad_llm_model", ""); err != nil {
		t.Fatal(err)
	}
	insertRule(t, b, "证据规则", "证据命中词", "scam", true, false)

	HandleGroupMessage(b, testutil.GroupMsg(-100, 555, 1, "这条包含证据命中词"))
	waitIdle(t, b)

	if soN.Load() != 0 || llmN.Load() != 0 {
		t.Errorf("无复判模型时也不该跑 systemone：so=%d llm=%d", soN.Load(), llmN.Load())
	}
	var action, decider string
	if err := b.Store.Read.QueryRow(
		`SELECT action,decider FROM antiad_log ORDER BY id DESC LIMIT 1`).
		Scan(&action, &decider); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(decider, "rule:") || action != "deleted_muted" {
		t.Errorf("无复判模型时按矩阵定案：action=%q decider=%q", action, decider)
	}
}

// TestMatchedRulesSeeQuoted：规则匹配区分引用来源 —— 外部聊天引用照旧
// 命中（正文为空、载荷全在引用里是典型规避形态，也是 quoted 字段存在的
// 意义）；群内引用是别人的话（引用一条广告提醒管理员），不算引用者发出的
// 载荷，不参与匹配。
func TestMatchedRulesSeeQuoted(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	insertRule(t, b, "引用广告规则", "引用命中词", "scam", true, false)

	// 群内引用：不命中。
	same := testutil.GroupMsg(-100, 42, 1, "看看这个")
	same.ReplyToMessage = testutil.GroupMsg(-100, 43, 2, "引用命中词")
	st := buildState(b, b.Cache.Snap(), same, senderProfile{MsgsInGroup: 5})
	if len(st.MatchedRules) != 0 {
		t.Errorf("群内引用不该触发规则证据：%+v", st.MatchedRules)
	}

	// 外部聊天引用：照旧命中。
	ext := testutil.GroupMsg(-100, 42, 3, "看看这个")
	ext.ExternalReply = &tg.ExternalReplyInfo{Text: "引用命中词",
		Chat: &tg.Chat{ID: -100999, Title: "某频道"}}
	st = buildState(b, b.Cache.Snap(), ext, senderProfile{MsgsInGroup: 5})
	if len(st.MatchedRules) != 1 || st.MatchedRules[0].Name != "引用广告规则" {
		t.Errorf("外部引用里的规则命中应进证据：%+v", st.MatchedRules)
	}
}
