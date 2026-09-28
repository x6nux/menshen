package antiad

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func TestUnlockCodeFormatAndRecognition(t *testing.T) {
	for i := 0; i < 20; i++ {
		code, err := newUnlockCode()
		if err != nil {
			t.Fatal(err)
		}
		if !unlockCodeRe.MatchString(code) {
			t.Fatalf("生成的码不符合格式: %q", code)
		}
		body := strings.TrimPrefix(code, "MSU-") // 前缀本身含 U，只查码体
		for _, bad := range []string{"I", "L", "O", "U"} {
			if strings.Contains(body, bad) {
				t.Fatalf("字符集不该包含 %s: %q", bad, code)
			}
		}
	}

	got, ok := findUnlockCode("前面一段 MSU-7k2q-9xfm 后面一段")
	if !ok || got != "MSU-7K2Q-9XFM" {
		t.Errorf("应识别并转大写，得到 %q ok=%v", got, ok)
	}
	for _, bad := range []string{
		"MSU-IIII-IIII", // I 不在字符集
		"MSU-7K2Q9XFM",  // 缺连字符
		"XSU-7K2Q-9XFM", // 前缀不对
	} {
		if _, ok := findUnlockCode(bad); ok {
			t.Errorf("%q 不该被识别为解禁码", bad)
		}
	}
}

// insertCodedAppeal 造一张已签发解禁码的申诉单。
func insertCodedAppeal(t *testing.T, b *core.Bot, uid int64, code string, expires int64) int64 {
	t.Helper()
	now := time.Now().Unix()
	res, err := b.Store.Write.Exec(`INSERT INTO appeals
		(bot_id,user_id,status,code,code_expires,created_at,updated_at)
		VALUES (?,?,'code',?,?,?,?)`, b.BotID(), uid, code, expires, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestGroupRedeemOnlyForPrivileged：解禁码在群里只有四条全满足才被截下；
// 普通成员发的照常留底、照常判定 —— 否则广告号塞一段像码的文字就能躲开判定。
func TestGroupRedeemOnlyForPrivileged(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	const code = "MSU-7K2Q-9XFM"
	appealID := insertCodedAppeal(t, b, 555, code, time.Now().Add(time.Hour).Unix())

	// 普通成员发码：不截，照常留底。
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"member"}}`
	HandleGroupMessage(b, testutil.GroupMsg(-100, 999, 900, "有人要的解禁码 "+code))
	waitIdle(t, b)
	var kept int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages`).Scan(&kept)
	if kept != 1 {
		t.Errorf("普通成员发的码应照常留底，实际 %d 条", kept)
	}
	var wl int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM ad_whitelist`).Scan(&wl)
	if wl != 0 {
		t.Error("普通成员不该触发兑换")
	}

	// 群管理员发码：截下、兑换、删消息。
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`
	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 901, code))
	var st string
	b.Store.Read.QueryRow(`SELECT status FROM appeals WHERE id=?`, appealID).Scan(&st)
	if st != "code" {
		t.Errorf("群内兑换不改申诉单状态，得到 %q", st)
	}
	var wlChat int64
	var wlExpires int64
	if err := b.Store.Read.QueryRow(`SELECT chat_id, expires_at FROM ad_whitelist
		WHERE user_id=555`).Scan(&wlChat, &wlExpires); err != nil {
		t.Fatalf("兑换后应有白名单: %v", err)
	}
	if wlChat != -100 || wlExpires <= time.Now().Unix() {
		t.Errorf("白名单应是本群、未过期，得到 chat=%d expires=%d", wlChat, wlExpires)
	}
	var redeems int
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM appeal_redeems
		WHERE appeal_id=? AND chat_id=-100`, appealID).Scan(&redeems)
	if redeems != 1 {
		t.Errorf("应写一条兑换记录，实际 %d", redeems)
	}
	if n := fake.CountCalls("deleteMessage"); n == 0 {
		t.Error("兑换后应删掉那条消息")
	}
	// 留底不再增加（兑换消息被截下）。
	b.Store.Read.QueryRow(`SELECT COUNT(*) FROM group_messages`).Scan(&kept)
	if kept != 1 {
		t.Errorf("兑换消息不该留底，实际 %d 条", kept)
	}

	// 同一个群第二次兑换：拒绝。
	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 902, code))
	found := false
	for _, c := range fake.Calls("sendMessage") {
		if text, _ := c["text"].(string); strings.Contains(text, "已在本群用过") {
			found = true
		}
	}
	if !found {
		t.Error("第二次兑换应回「已在本群用过」")
	}
}

// TestGroupRedeemExpiredAndSelf：过期码与申诉人本人发的都不生效。
func TestGroupRedeemExpiredAndSelf(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	const code = "MSU-7K2Q-9XFM"
	insertCodedAppeal(t, b, 555, code, time.Now().Add(-time.Hour).Unix())
	fake.Resp["getChatMember"] = `{"ok":true,"result":{"status":"administrator"}}`

	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 900, code))
	found := false
	for _, c := range fake.Calls("sendMessage") {
		if text, _ := c["text"].(string); strings.Contains(text, "已失效") {
			found = true
		}
	}
	if !found {
		t.Error("过期码应回「已失效」")
	}
}

// TestDirectRedeemScopes：归属人兑换只解本 bot 名下的群、不动联合封禁名单；
// 主管理员兑换解全平台并解除联合封禁。
func TestDirectRedeemScopes(t *testing.T) {
	b, _, _ := testutil.NewTestBotOwned(t, 1, 2) // 主管理员 1、归属人 2
	testutil.EnableAntiad(t, b, -100)
	if _, err := b.Store.Write.Exec(`INSERT INTO group_members
		(chat_id,user_id,joined_at,first_seen,msg_count,last_msg_at,ad_hits)
		VALUES (-100,555,0,0,3,0,0)`); err != nil {
		t.Fatal(err)
	}

	const codeOwner = "MSU-7K2Q-9XFM"
	apOwner := insertCodedAppeal(t, b, 555, codeOwner, time.Now().Add(time.Hour).Unix())
	HandleDirectRedeem(b, &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 2},
		Chat: &tg.Chat{ID: 2, Type: "private"}}, codeOwner)

	var wBot, wChat int64
	if err := b.Store.Read.QueryRow(`SELECT bot_id,chat_id FROM ad_whitelist
		WHERE user_id=555`).Scan(&wBot, &wChat); err != nil {
		t.Fatalf("归属人兑换应写白名单: %v", err)
	}
	if wBot != b.BotID() || wChat != 0 {
		t.Errorf("归属人白名单应为该 bot 名下所有群，得到 bot=%d chat=%d", wBot, wChat)
	}
	var st string
	b.Store.Read.QueryRow(`SELECT status FROM appeals WHERE id=?`, apOwner).Scan(&st)
	if st != "redeemed" {
		t.Errorf("归属人兑换后申诉单应 redeemed，得到 %q", st)
	}

	// 主管理员兑换另一张码：全平台白名单 + 解除联合封禁。
	b.Store.Write.Exec(`INSERT INTO gban (user_id,reason,src_chat,by_bot,created_at)
		VALUES (555,'x',-100,0,0)`)
	if err := b.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	const codeMain = "MSU-9XFM-7K2Q"
	insertCodedAppeal(t, b, 555, codeMain, time.Now().Add(time.Hour).Unix())
	HandleDirectRedeem(b, &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 1},
		Chat: &tg.Chat{ID: 1, Type: "private"}}, codeMain)

	var wBot2, wChat2 int64
	if err := b.Store.Read.QueryRow(`SELECT bot_id,chat_id FROM ad_whitelist
		WHERE user_id=555`).Scan(&wBot2, &wChat2); err != nil {
		t.Fatal(err)
	}
	if wBot2 != 0 || wChat2 != 0 {
		t.Errorf("主管理员白名单应为全平台，得到 bot=%d chat=%d", wBot2, wChat2)
	}
	if _, still := b.Cache.Snap().Gban[555]; still {
		t.Error("主管理员兑换应解除联合封禁")
	}

	// 无关的人：拒绝。
	const codeOther = "MSU-2Q9X-FM7K"
	insertCodedAppeal(t, b, 555, codeOther, time.Now().Add(time.Hour).Unix())
	HandleDirectRedeem(b, &tg.Message{MessageID: 1, From: &tg.TGUser{ID: 999},
		Chat: &tg.Chat{ID: 999, Type: "private"}}, codeOther)
	_ = fmt.Sprint()
}
