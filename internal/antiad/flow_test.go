package antiad

import (
	"testing"
	"time"

	"menshen/internal/billing"
	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
	"menshen/internal/upstream"
)

// countRows 数某张表的行数。
func countRows(t *testing.T, b *core.Bot, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := b.Store.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	return n
}

// TestGroupMessageInactiveIsSilent 锁住守门：总开关与群白名单都是纯内存
// 判断，未启用时零 API 调用、零数据库写入。bot 可能还在一堆无关群里，
// 一开开关就对所有群生效是不可接受的。
func TestGroupMessageInactiveIsSilent(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 777)

	// 总开关关着
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 1, "随便说点什么"))
	// 开关开了但该群不在白名单里
	testutil.EnableAntiad(t, b, -999)
	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 2, "还是不该被处理"))

	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages`); n != 0 {
		t.Errorf("未生效时不该留底，实际 %d 条", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_members`); n != 0 {
		t.Errorf("未生效时不该写画像，实际 %d 行", n)
	}
	if fake.CountCalls("getChatMember") != 0 {
		t.Error("未生效时不该发任何 TG 请求")
	}
}

// TestGroupMessageRecordsBeforeJudging 确认留底发生在一切判定分支之前：
// /ad 复查要的恰恰是这些没被判过的消息。
func TestGroupMessageRecordsBeforeJudging(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 777)
	testutil.EnableAntiad(t, b, -100)
	// 让发送者被认成群管理员（豁免路径），判定一定不会跑
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 11, "豁免者的发言"))

	var text string
	err := b.Store.Read.QueryRow(
		`SELECT text FROM group_messages WHERE chat_id=? AND message_id=?`,
		-100, 11).Scan(&text)
	if err != nil {
		t.Fatalf("豁免者的消息也必须留底: %v", err)
	}
	if text != "豁免者的发言" {
		t.Errorf("留底正文 = %q", text)
	}

	// 豁免者的发言仍要进上下文：否则「管理员刚说了别发广告」这种
	// 关键语境会从模型视野里消失。
	if got := b.AdCtx.Recent(-100, 5); len(got) != 1 || got[0].Text != "豁免者的发言" {
		t.Errorf("豁免者的发言应进上下文环，得到 %+v", got)
	}
}

// TestGroupMessageCountsSeniorityForStickers 锁住一处容易写反的顺序：
// 资历累计必须先于「无正文就早退」。不计的话纯贴纸用户 msg_count
// 永不增长，在发言数轴上永远算新人。
func TestGroupMessageCountsSeniorityForStickers(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	testutil.EnableAntiad(t, b, -100)

	sticker := testutil.GroupMsg(-100, 42, 21, "")
	HandleGroupMessage(b, sticker)

	gm, ok := loadMember(b.Store, -100, 42)
	if !ok {
		t.Fatal("纯贴纸也应建立画像行")
	}
	if gm.MsgCount != 1 {
		t.Errorf("msg_count = %d, 期望 1", gm.MsgCount)
	}
	// 但没有正文就不该留底
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages`); n != 0 {
		t.Errorf("无正文不该留底，实际 %d 条", n)
	}
}

// TestAdCommandNotRecorded 确认 /ad 自己不进留底——
// 留了会被送进下一次复查。
func TestAdCommandNotRecorded(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	testutil.EnableAntiad(t, b, -100)

	HandleGroupMessage(b, testutil.GroupMsg(-100, 42, 31, "/ad 12345"))

	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages`); n != 0 {
		t.Errorf("/ad 命令不该留底，实际 %d 条", n)
	}
}

// TestRecordJoinKeepsMsgCount 确认退群重进只更新 joined_at：
// 历史发言量仍是有效画像。
func TestRecordJoinKeepsMsgCount(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)

	touchMember(b, -100, 42, 1000)
	touchMember(b, -100, 42, 1001)
	recordJoin(b, -100, 42, 2000)

	gm, _ := loadMember(b.Store, -100, 42)
	if gm.MsgCount != 2 {
		t.Errorf("重新进群后 msg_count = %d, 期望保留为 2", gm.MsgCount)
	}
	if gm.JoinedAt != 2000 {
		t.Errorf("joined_at = %d, 期望 2000", gm.JoinedAt)
	}
}

// TestTouchMemberKeepsFirstSeen 确认 first_seen 只在插入时写。
// 它是 joined_at 缺失时唯一的年龄下界，被每条消息刷新的话
// 所有人都会永远是「刚出现」。
func TestTouchMemberKeepsFirstSeen(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)

	touchMember(b, -100, 42, 1000)
	touchMember(b, -100, 42, 9999)

	gm, _ := loadMember(b.Store, -100, 42)
	if gm.FirstSeen != 1000 {
		t.Errorf("first_seen = %d, 期望保持 1000", gm.FirstSeen)
	}
	if gm.LastMsgAt != 9999 {
		t.Errorf("last_msg_at = %d, 期望 9999", gm.LastMsgAt)
	}
}

// TestChatMemberUpdateIgnoresRestrictedFlip 锁住关键坑：
// status 在 member 与 restricted 之间来回跳（群管挂临时限制）不是进群，
// 按进群处理会把老成员重新变成「新人」，下一条消息就被按最严档处置。
func TestChatMemberUpdateIgnoresRestrictedFlip(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	testutil.EnableAntiad(t, b, -100)

	// 真进群：left → member
	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: -100, Type: "supergroup"}, Date: 5000,
		OldChatMember: &tg.ChatMemberInfo{Status: "left"},
		NewChatMember: &tg.ChatMemberInfo{Status: "member", User: &tg.TGUser{ID: 42}},
	})
	gm, ok := loadMember(b.Store, -100, 42)
	if !ok || gm.JoinedAt != 5000 {
		t.Fatalf("进群未被记录: ok=%v joined_at=%d", ok, gm.JoinedAt)
	}

	// 权限变动：member → restricted，不是进群
	HandleChatMemberUpdate(b, &tg.ChatMemberUpdated{
		Chat: &tg.Chat{ID: -100, Type: "supergroup"}, Date: 8000,
		OldChatMember: &tg.ChatMemberInfo{Status: "member"},
		NewChatMember: &tg.ChatMemberInfo{Status: "restricted", User: &tg.TGUser{ID: 42}},
	})
	gm, _ = loadMember(b.Store, -100, 42)
	if gm.JoinedAt != 5000 {
		t.Errorf("joined_at 被权限变动覆盖成 %d —— 老成员会被当成新人", gm.JoinedAt)
	}
}

// TestNewChatMembersFallback 确认 service 消息这条兜底路径也记进群：
// chat_member 在 bot 权限变动期间可能漏收。
func TestNewChatMembersFallback(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	testutil.EnableAntiad(t, b, -100)

	m := testutil.GroupMsg(-100, 1, 41, "")
	m.NewChatMembers = []*tg.TGUser{{ID: 88}}
	HandleGroupMessage(b, m)

	if gm, ok := loadMember(b.Store, -100, 88); !ok || gm.JoinedAt == 0 {
		t.Errorf("service 消息未记录进群: ok=%v %+v", ok, gm)
	}
}

// TestCleanupKeepsAdHits 确认有命中史的画像行不被清理：
// ad_hits 是风控证据，清掉等于给惯犯重置档案。
func TestCleanupKeepsAdHits(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	old := time.Now().Unix() - 100*86400

	// 两个都很久没发言，一个有命中史
	b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,1,?,?,5,?,0),(-100,2,?,?,5,?,3)`,
		old, old, old, old, old, old)
	b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,action,reason,created_at)
		VALUES (-100,1,1,'旧广告','ad',0.9,'llm','deleted','',?)`, old)
	b.Store.Write.Exec(`INSERT INTO group_messages
		(chat_id,message_id,user_id,text,at) VALUES (-100,1,1,'旧消息',?)`, old)

	CleanupData(b.Shared)

	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log`); n != 0 {
		t.Errorf("过期流水应被清理，剩 %d 条", n)
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM group_messages`); n != 0 {
		t.Errorf("过期留底应被清理，剩 %d 条", n)
	}
	if n := countRows(t, b,
		`SELECT COUNT(*) FROM group_members WHERE user_id=1`); n != 0 {
		t.Error("无命中史的过期画像应被清理")
	}
	if n := countRows(t, b,
		`SELECT COUNT(*) FROM group_members WHERE user_id=2`); n != 1 {
		t.Error("有命中史的画像必须保留——那是风控证据")
	}
}

// TestCleanupRejectsBadRetention 确认非法保留天数回落到 30 天，
// 而不是把 cutoff 算成「现在」——后者会把全部历史一次性删光。
func TestCleanupRejectsBadRetention(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	if err := b.PutSetting("log_retention_days", "0"); err != nil {
		t.Fatal(err)
	}

	recent := time.Now().Unix() - 86400 // 1 天前
	b.Store.Write.Exec(`INSERT INTO antiad_log
		(chat_id,user_id,message_id,text,verdict,confidence,decider,action,reason,created_at)
		VALUES (-100,1,1,'昨天的','ad',0.9,'llm','deleted','',?)`, recent)

	CleanupData(b.Shared)

	if n := countRows(t, b, `SELECT COUNT(*) FROM antiad_log`); n != 1 {
		t.Error("保留天数非法时应回落到 30 天，昨天的记录不该被删")
	}
}

// TestLogAdRoundTrip 确认流水落库与读回一致，且 error 判定被正确标记。
func TestLogAdRoundTrip(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	m := testutil.GroupMsg(-100, 42, 51, "广告原文")

	id := logAd(b, m, adVerdict{IsAd: true, Confidence: 0.93, Kind: "crypto",
		Reason: "命中", Decider: "llm",
		Usage: billing.Usage{PromptTokens: 100, CompletionTokens: 20}, Cost: 5},
		"deleted", "")
	if id == 0 {
		t.Fatal("落库失败")
	}

	row, ok := LoadAdLog(b.Store, id)
	if !ok {
		t.Fatal("读回失败")
	}
	if row.Verdict != "ad" || row.Kind != "crypto" || row.Action != "deleted" {
		t.Errorf("读回不一致: %+v", row)
	}
	if row.PromptTokens != 100 || row.QuotaCost != 5 {
		t.Errorf("用量/开销未落库: %+v", row)
	}

	// Decider 为空 = 判定链路整个失败，必须记成 error 而不是 clean，
	// 否则「一条都没拦到」会被当成群里很干净。
	id2 := logAd(b, m, adVerdict{Reason: "上游全挂"}, "none", "判定失败")
	row2, _ := LoadAdLog(b.Store, id2)
	if row2.Verdict != "error" {
		t.Errorf("判定失败应记为 error，实际 %q", row2.Verdict)
	}
}

// TestBumpAdHitsFloorsAtZero 确认误判回退不会把命中数压到负数。
func TestBumpAdHitsFloorsAtZero(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 777)
	touchMember(b, -100, 42, 1000)

	BumpAdHits(b, -100, 42, 1)
	BumpAdHits(b, -100, 42, -1)
	BumpAdHits(b, -100, 42, -1) // 多退一次

	gm, _ := loadMember(b.Store, -100, 42)
	if gm.AdHits != 0 {
		t.Errorf("ad_hits = %d, 期望不低于 0", gm.AdHits)
	}
}

// TestExtractUsage 覆盖两种协议的归一化。OpenAI 的 prompt_tokens
// 包含 cached_tokens，不减掉的话四类会重叠、开销虚高。
func TestExtractUsage(t *testing.T) {
	chat := billing.ExtractUsage(upstream.EPChat, []byte(`{"usage":{
		"prompt_tokens":100,"completion_tokens":30,
		"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10}}}`))
	want := billing.Usage{PromptTokens: 60, CompletionTokens: 30,
		CacheReadTokens: 40, CacheWriteTokens: 10}
	if chat != want {
		t.Errorf("chat usage = %+v, 期望 %+v", chat, want)
	}

	so := billing.ExtractUsage(upstream.EPSystemOne, []byte(`{"usage":{
		"input_tokens":50,"output_tokens":5}}`))
	if so != (billing.Usage{PromptTokens: 50, CompletionTokens: 5}) {
		t.Errorf("systemone usage = %+v", so)
	}

	// 解析失败返回零值，不该影响判定结果
	if got := billing.ExtractUsage(upstream.EPChat, []byte("不是 json")); got != (billing.Usage{}) {
		t.Errorf("解析失败应返回零值，得到 %+v", got)
	}
}

// TestComputeCost 确认四类 token 各乘各的单价，且向上取整避免零成本。
func TestComputeCost(t *testing.T) {
	m := &upstream.Model{PromptPrice: 1, CompletionPrice: 2,
		CacheReadPrice: 0.1, CacheWritePrice: 1.25}

	// 1M 输入 = $1 = 500000 quota
	if got := billing.ComputeCost(billing.Usage{PromptTokens: 1_000_000}, m); got != billing.QuotaPerUSD {
		t.Errorf("成本 = %d, 期望 %d", got, billing.QuotaPerUSD)
	}
	// 零用量不该造出成本
	if got := billing.ComputeCost(billing.Usage{}, m); got != 0 {
		t.Errorf("零用量成本应为 0，得到 %d", got)
	}
	// 极小用量向上取整，不出现零成本请求
	if got := billing.ComputeCost(billing.Usage{PromptTokens: 1}, m); got != 1 {
		t.Errorf("极小用量应向上取整为 1，得到 %d", got)
	}
}
