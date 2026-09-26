package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestMessageAdHashDeletesDirectly：消息本身是广告、判定要删的，记下内容哈希；
// 同样的内容换个人再发，不再送检，直接删；禁言交给复判模型。
func TestMessageAdHashDeletesDirectly(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"),
		llmReply(true, 0.95, "scam", "message"))

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加V"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM ad_hashes`); n != 1 {
		t.Fatalf("消息级广告应记下哈希，实际 %d", n)
	}
	so0, llm0 := soN.Load(), llmN.Load()
	del0, mute0 := fake.CountCalls("deleteMessage"), fake.CountCalls("restrictChatMember")

	// 别人发同样的内容（空白不同也算同一段）
	HandleGroupMessage(b, testutil.GroupMsg(-100, 43, 2, "日入过万  加V"))
	waitIdle(t, b)

	if soN.Load() != so0 {
		t.Error("命中哈希不该再送 systemone")
	}
	if fake.CountCalls("deleteMessage") != del0+1 {
		t.Error("命中哈希应直接删除")
	}
	if llmN.Load() != llm0+1 {
		t.Error("命中哈希后禁言应交给复判模型决定")
	}
	muteCalls := fake.Calls("restrictChatMember")
	if len(muteCalls) != mute0+1 || untilOf(muteCalls[len(muteCalls)-1]) < 23*3600 {
		t.Error("复判确认后应正式禁言（且命中时不先临时禁言）")
	}
	_, action, reason := logRow(t, b)
	if action != "deleted_muted" || !strings.Contains(reason, "内容相同") {
		t.Errorf("action=%q reason=%q", action, reason)
	}
}

// TestHashHitWithoutLLMOnlyDeletes：没配复判模型时，命中哈希只删不禁——
// 禁言只由复判模型决定，不沿用当初那条的禁言。
func TestHashHitWithoutLLMOnlyDeletes(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))
	if err := b.PutSetting("antiad_llm_model", ""); err != nil {
		t.Fatal(err)
	}
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加V"))
	waitIdle(t, b)
	mute0 := fake.CountCalls("restrictChatMember")

	HandleGroupMessage(b, testutil.GroupMsg(-100, 43, 2, "日入过万 加V"))
	waitIdle(t, b)
	if fake.CountCalls("restrictChatMember") != mute0 {
		t.Error("没有复判模型时命中哈希不该禁言")
	}
	if _, action, _ := logRow(t, b); action != "deleted" {
		t.Errorf("action = %q，期望 deleted", action)
	}
}

// TestAccountAdNotHashed：账号级广告的正文可能只是「你好」，记下来会误删所有人的你好。
func TestAccountAdNotHashed(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "promo", "account"), llmReply(true, 0.95, "promo", "account"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "你好"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM ad_hashes`); n != 0 {
		t.Errorf("账号级广告不该记哈希，实际 %d", n)
	}
}

// TestHashForgottenWhenReviewClean：命中哈希后复判判为正常，说明这段内容不是广告，
// 哈希要撤掉，否则会一直误删下去。
func TestHashForgottenWhenReviewClean(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加V"))
	waitIdle(t, b)

	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(false, 0.9, "none", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 43, 2, "日入过万 加V"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM ad_hashes`); n != 0 {
		t.Errorf("复判为正常后哈希应撤掉，实际 %d", n)
	}
}

// TestForgetAdHashByLogText：管理员点「误判」时按流水原文撤掉哈希。
func TestForgetAdHashByLogText(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加V"))
	waitIdle(t, b)

	var text string
	b.Store.Read.QueryRow(`SELECT text FROM antiad_log ORDER BY id DESC LIMIT 1`).Scan(&text)
	ForgetAdHash(b, text)
	if n := countRows(t, b, `SELECT COUNT(*) FROM ad_hashes`); n != 0 {
		t.Errorf("误判后哈希应撤掉，实际 %d", n)
	}
}

// TestAdHashIsPerBot：一个租户的误判不该删到别人的群里。
func TestAdHashIsPerBot(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	rememberAdHash(b, "日入过万", adVerdict{IsAd: true, Confidence: 0.9, Scope: "message"}, 1)
	if _, ok := lookupAdHash(b, "日入过万"); !ok {
		t.Fatal("本 bot 应能查到")
	}
	if _, err := b.Store.Write.Exec(`UPDATE ad_hashes SET bot_id=999`); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupAdHash(b, "日入过万"); ok {
		t.Error("别的 bot 记下的哈希不该对本 bot 生效")
	}
}

// TestDryrunDoesNotHash：演练群的判定没人核对过，不能据此在正式群里直接删。
func TestDryrunDoesNotHash(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiadMode(t, b, -100, true)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"), llmReply(true, 0.95, "scam", "message"))
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "日入过万 加V"))
	waitIdle(t, b)
	if n := countRows(t, b, `SELECT COUNT(*) FROM ad_hashes`); n != 0 {
		t.Errorf("演练群的判定不该记哈希，实际 %d", n)
	}
}
