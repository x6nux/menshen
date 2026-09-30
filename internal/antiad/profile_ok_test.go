package antiad

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func profileFixture() senderProfile {
	return senderProfile{UserID: 6438368916, Username: "mavita777",
		FirstName: "Mavita", Bio: "有问题请联系我的管家 @Childkiller_bot"}
}

// TestProfileOKGrantLookupAndInvalidation：资料放行按「资料指纹」绑定，
// 改过资料、过期、超过上限都有明确行为。
func TestProfileOKGrantLookupAndInvalidation(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	p := profileFixture()
	now := time.Now().Unix()

	// 没有放行时查不到。
	if until := b.Cache.Snap().ProfileAllowed(b.BotID(), p.UserID, profileHash(p), now); until != 0 {
		t.Fatalf("没放行过不该命中，得到 %d", until)
	}
	// 给了 0 小时 = 不放行。
	if until := GrantProfileOK(b, p, 0, "模型说不用"); until != 0 {
		t.Errorf("0 小时不该落库，得到 %d", until)
	}

	until := GrantProfileOK(b, p, 12, "复判确认只有资料可疑")
	if until <= now {
		t.Fatalf("放行时间应在将来，得到 %d", until)
	}
	got := b.Cache.Snap().ProfileAllowed(b.BotID(), p.UserID, profileHash(p), now)
	if got != until {
		t.Errorf("放行查询应返回到期时间 %d，得到 %d", until, got)
	}
	// 画像上要用它压掉「凭资料判广告」这一路。
	p2 := profileFixture()
	markProfileOK(b, &p2)
	if !p2.ProfileOK || p2.ProfileOKUntil == "" {
		t.Errorf("画像应标上资料已放行，得到 %+v", p2)
	}

	// 改过简介 = 另一份资料，放行不再适用。
	p3 := profileFixture()
	p3.Bio = "日入5000 私聊我"
	if u := b.Cache.Snap().ProfileAllowed(b.BotID(), p3.UserID, profileHash(p3), now); u != 0 {
		t.Errorf("改过资料不该继续放行，得到 %d", u)
	}

	// 过期后失效。
	if _, err := b.Store.Write.Exec(`UPDATE profile_ok SET expires_at=? WHERE bot_id=? AND user_id=?`,
		now-1, b.BotID(), p.UserID); err != nil {
		t.Fatal(err)
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if u := b.Cache.Snap().ProfileAllowed(b.BotID(), p.UserID, profileHash(p), now); u != 0 {
		t.Errorf("过期后不该命中，得到 %d", u)
	}

	// 上限：模型给多大都夹到 72 小时。
	if u := GrantProfileOK(b, p, 500, "手滑给多了"); u > now+72*3600 || u <= now+71*3600 {
		t.Errorf("放行时长应夹到 72 小时，得到 %d", u-now)
	}
}

// TestReviewCleanGrantsProfileOK：复判判为正常、且给了放行时长时落库，
// 并把「资料放行 N 小时」写进流水说明；判成广告时忽略这个字段；
// 判成「账号广告号」时撤销已有的放行。
func TestReviewCleanGrantsProfileOK(t *testing.T) {
	// 复判正常 + 24 小时放行。
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAIWith(t, b, soReply("ad", 0.95, "scam", "message"),
		llmReplyWithOK(false, 0.3, "none", "message", 24))

	// 让画像带上「写着自己 bot 的简介」：这类资料正是会被反复判的那种。
	fake.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":7001,"type":"private","first_name":"Mavita",` +
				`"bio":"有问题请联系我的管家 @Childkiller_bot"}}`, true
		}
		if method == "getChatMember" {
			return `{"ok":true,"result":{"status":"member"}}`, true
		}
		return "", false
	}

	HandleGroupMessage(b, testutil.GroupMsg(-100, 7001, 7, "最近在鼓捣啥项目？"))
	waitIdle(t, b)

	var hours, expires int64
	var phash string
	if err := b.Store.Read.QueryRow(`SELECT hours,expires_at,phash FROM profile_ok
		WHERE bot_id=? AND user_id=?`, b.BotID(), 7001).Scan(&hours, &expires, &phash); err != nil {
		t.Fatalf("复判正常且给了时长时应落库: %v", err)
	}
	if hours != 24 || expires <= time.Now().Unix() {
		t.Errorf("放行应记 24 小时且未过期，得到 hours=%d expires=%d", hours, expires)
	}
	if _, action, reason := logRow(t, b); action != "deleted" ||
		!strings.Contains(reason, "资料放行 24 小时") {
		t.Errorf("流水说明里应写明资料放行，action=%q reason=%q", action, reason)
	}

	// 判成广告时同名字段被忽略（模型偶尔会两个都填）。
	b2, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b2, -100)
	fakeAIWith(t, b2, soReply("ad", 0.95, "scam", "message"),
		llmReplyWithOK(true, 0.9, "scam", "message", 24))
	HandleGroupMessage(b2, testutil.GroupMsg(-100, 7002, 8, "加微信 日入5000"))
	waitIdle(t, b2)
	if n := countRows(t, b2, `SELECT COUNT(*) FROM profile_ok`); n != 0 {
		t.Errorf("判成广告时不该给资料放行，得到 %d 行", n)
	}

	// 判成「账号本身就是广告号」时，已有的放行作废。
	b3, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b3, -100)
	fakeAIWith(t, b3, soReply("ad", 0.95, "scam", "account"),
		llmReplyWithOK(true, 0.9, "scam", "account", 0))
	p := senderProfile{UserID: 7003, FirstName: "广告位"}
	if GrantProfileOK(b3, p, 48, "先前的放行") == 0 {
		t.Fatal("准备放行失败")
	}
	HandleGroupMessage(b3, testutil.GroupMsg(-100, 7003, 9, "看主页 日入5000"))
	waitIdle(t, b3)
	if n := countRows(t, b3, `SELECT COUNT(*) FROM profile_ok`); n != 0 {
		t.Errorf("账号广告号判下来后应撤销资料放行，剩 %d 行", n)
	}
}

// TestColdJudgeSkipsClearedProfile：资料刚被复判放过、又没改过时不再冷判定 ——
// 同一份资料反复判只会把同一个误判重演一遍。
func TestColdJudgeSkipsClearedProfile(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	soN, llmN := fakeAIWith(t, b, soReply("ad", 0.99, "scam", "account"),
		llmReplyWithOK(false, 0.2, "none", "account", 24))
	bio := "有问题请联系我的管家 @Childkiller_bot"
	fakeAI := b.TG.(*testutil.FakeTG)
	fakeAI.RespFunc = func(method string, payload map[string]any) (string, bool) {
		if method == "getChat" {
			return `{"ok":true,"result":{"id":7004,"type":"private",` +
				`"first_name":"Mavita","bio":"` + bio + `"}}`, true
		}
		return "", false
	}
	u := &tg.TGUser{ID: 7004, FirstName: "Mavita", Username: "mavita777"}
	conf := testutil.ChatConfOf(t, b, -100)

	// 预置：这份资料（含简介）刚被复判放行。
	seed := buildProfile(b, &tg.Message{From: u}, groupMember{}, time.Now().Unix())
	seed.Bio = bio
	if GrantProfileOK(b, seed, 24, "预置放行") == 0 {
		t.Fatal("预置放行失败")
	}
	coldJudge(b, conf, u)
	if soN.Load()+llmN.Load() != 0 {
		t.Errorf("资料已放行时不该再冷判定，调了 %d+%d 次", soN.Load(), llmN.Load())
	}
	if n := countRows(t, b, `SELECT COUNT(*) FROM join_mutes`); n != 0 {
		t.Errorf("资料已放行时不该限制进群，得到 %d 条", n)
	}

	// 改了资料（介绍换成引流话术）：放行失效，冷判定照跑。
	bio = "做单日结 5000 私聊我"
	b.BioCache.Delete(u.ID) // 简介有 1 小时缓存，测试里直接清掉
	if n := countRows(t, b, `SELECT COUNT(*) FROM profile_ok`); n != 1 {
		t.Fatalf("放行记录应还在，得到 %d", n)
	}
	coldJudge(b, conf, u)
	if soN.Load()+llmN.Load() == 0 {
		t.Error("资料改过之后应重新冷判定")
	}
}

// llmReplyWithOK 造一条带 profile_ok_hours 的复判响应。
func llmReplyWithOK(isAd bool, conf float64, kind, scope string, okHours int) string {
	inner, _ := json.Marshal(map[string]any{"is_ad": isAd, "confidence": conf,
		"kind": kind, "scope": scope, "profile_ok_hours": okHours, "reason": "测试"})
	out, _ := json.Marshal(map[string]any{"choices": []any{
		map[string]any{"message": map[string]any{"content": string(inner)}}}})
	return string(out)
}

// TestProfileOKStoreLayer：快照层只装未过期的行，查询再按到期时间复核。
func TestProfileOKStoreLayer(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, row := range []struct {
		uid     int64
		expires int64
	}{
		{8001, now + 3600},
		{8002, now - 3600},
	} {
		if _, err := b.Store.Write.Exec(`INSERT INTO profile_ok
			(bot_id,user_id,phash,hours,reason,created_at,expires_at)
			VALUES (?,?,?,?,?,?,?)`, b.BotID(), row.uid, "abc", 1, "", now, row.expires); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	if u := snap.ProfileAllowed(b.BotID(), 8001, "abc", now); u != now+3600 {
		t.Errorf("未过期的行应命中，得到 %d", u)
	}
	if u := snap.ProfileAllowed(b.BotID(), 8002, "abc", now); u != 0 {
		t.Errorf("过期的行不该命中，得到 %d", u)
	}
	// 指纹不同不算命中。
	if u := snap.ProfileAllowed(b.BotID(), 8001, "different", now); u != 0 {
		t.Errorf("指纹不同不该命中，得到 %d", u)
	}
	var n int
	if err := b.Store.Read.QueryRow(`SELECT COUNT(*) FROM profile_ok`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("表里应有两行，得到 %d", n)
	}
}
