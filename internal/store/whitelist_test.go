package store

import "testing"

// TestWhitelistedRanges 验证白名单的范围规则：
// bot_id=0 全平台、chat_id=0 该 bot 名下所有群、expires_at=0 永久。
func TestWhitelistedRanges(t *testing.T) {
	s := &Snapshot{Whitelist: []WhiteRec{
		{BotID: 0, ChatID: 0, UserID: 1, ExpiresAt: 0},       // 全平台永久
		{BotID: 7, ChatID: 0, UserID: 2, ExpiresAt: 0},       // bot 7 名下所有群
		{BotID: 7, ChatID: -100, UserID: 3, ExpiresAt: 0},    // bot 7 的 -100 群
		{BotID: 7, ChatID: -100, UserID: 4, ExpiresAt: 2000}, // 已过期
		{BotID: 8, ChatID: -100, UserID: 5, ExpiresAt: 0},    // 别的 bot
	}}

	cases := []struct {
		name                    string
		botID, chatID, uid, now int64
		want                    bool
	}{
		{"全平台任意 bot 任意群", 9, -100, 1, 1000, true},
		{"bot 名下所有群", 7, -999, 2, 1000, true},
		{"别的 bot 不匹配", 8, -999, 2, 1000, false},
		{"指定群命中", 7, -100, 3, 1000, true},
		{"指定群不命中别的群", 7, -200, 3, 1000, false},
		{"已过期", 7, -100, 4, 3000, false},
		{"过期前仍有效", 7, -100, 4, 1000, true},
		{"别的 bot 的群不匹配", 9, -100, 5, 1000, false},
		{"无关的人不匹配", 7, -100, 99, 1000, false},
	}
	for _, c := range cases {
		if got := s.Whitelisted(c.botID, c.chatID, c.uid, c.now); got != c.want {
			t.Errorf("%s: Whitelisted(%d,%d,%d,%d) = %v，期望 %v",
				c.name, c.botID, c.chatID, c.uid, c.now, got, c.want)
		}
	}
}
