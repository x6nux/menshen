package antiad

import (
	"strings"
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestRestrictionStatusText：/check 的状态块要把限制写明白（类型/原因/群），
// 全局联合封禁给主 bot 链接、专属联合封禁给归属人 bot 的链接。
func TestRestrictionStatusText(t *testing.T) {
	_, b := testutil.NewTestRegistry(t, nil)
	sh := b.Shared
	// 当前 bot 取个用户名，再加一个主 bot（链接要用）。
	if _, err := sh.Store.Write.Exec(`UPDATE bots SET username='workbot' WHERE bot_id=?`,
		b.BotID()); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO bots
		(token,bot_id,username,owner_id,created_at,is_main)
		VALUES ('1:main',9001,'mainbot',777,?,1)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	// 三条限制：全局联封、归属人的专属联封（要圈群才生效）、一条进群限制。
	if err := GbanAdd(sh, 555, "跨群发广告", 0, b.BotID()); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnSetChat(sh, 777, -100, true); err != nil {
		t.Fatal(err)
	}
	if err := GbanOwnAddBan(sh, 777, 555, "简介推广接码服务", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Store.Write.Exec(`INSERT INTO join_mutes
		(chat_id,user_id,bot_id,reason,notice_msg,attempts,created_at)
		VALUES (-100,555,?, '简介写着加微信',0,0,?)`,
		b.BotID(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	text, links := RestrictionStatusText(sh, b.BotID(), 555)
	for _, want := range []string{"全局联合封禁", "专属联合封禁", "进群限制", "跨群发广告"} {
		if !strings.Contains(text, want) {
			t.Errorf("状态块应含 %q：\n%s", want, text)
		}
	}
	var mainLink, ownLink bool
	for _, l := range links {
		if strings.Contains(l[1], "https://t.me/mainbot?start=user555") {
			mainLink = true
		}
		if strings.Contains(l[1], "https://t.me/workbot?start=user555") {
			ownLink = true
		}
	}
	if !mainLink || !ownLink {
		t.Errorf("应分别给出主 bot 与归属人 bot 的解除链接，得到 %v", links)
	}
	// 没有任何限制时不返回状态块。
	if s, rows := RestrictionStatusText(sh, b.BotID(), 556); s != "" || len(rows) != 0 {
		t.Errorf("没有限制不该有状态块：%q %v", s, rows)
	}
	// deep link 往返。
	if uid, ok := ParseUserPayload(UserPayload(123)); !ok || uid != 123 {
		t.Errorf("UserPayload 往返失败：%d %v", uid, ok)
	}
}
