package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
)

// TestProfileShape 守归一化表：链接/数字/@/零宽都折叠成同一形状；
// 太短或只剩占位符时返回空串（不参与复用，避免扩大误伤）。
func TestProfileShape(t *testing.T) {
	cases := []struct {
		name, bio, want string
	}{
		{"链接占位", "免押小额洗资：https://t.me/+abcdef", "免押小额洗资[链接]"},
		{"不同邀请链接同形状", "免押小额洗资 t.me/joinchat/xyz", "免押小额洗资[链接]"},
		{"数字占位", "日入5000 联系微信abc", "日入[数]联系微信abc"},
		{"@号占位", "业务联系 @lilai888", "业务联系[号]"},
		{"零宽与标点", "免\u200b押，小额！！洗资。", "免押小额洗资"},
		{"大小写与空白", "  ABC   Def  ", "abc def"},
		{"只有数字", "12345 678", ""},
		{"太短", "洗资", ""},
		{"只有占位符", "https://t.me/+abc", ""},
		{"空资料", "", ""},
	}
	for _, c := range cases {
		if got := profileShape(senderProfile{Bio: c.bio}); got != c.want {
			t.Errorf("%s: profileShape(%q) = %q，期望 %q",
				c.name, c.bio, got, c.want)
		}
	}
	// bio 为空时退回昵称：同模板批量号的昵称也成套。
	if got := profileShape(senderProfile{FirstName: "免押小额洗资"}); got != "免押小额洗资" {
		t.Errorf("bio 为空应退回昵称，得到 %q", got)
	}
}

// 进群限制移除时按 join_mutes.shape 反查删除形状：限制被推翻后模板
// 不该继续零 AI 复用。
func TestDropJoinMuteRemovesProfileShape(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	shape := profileShape(senderProfile{Bio: "免押小额洗资 https://t.me/+aaa"})
	learnProfileShape(b.Store, shape, "promo", "样本", time.Now().Unix())
	if _, ok := lookupProfileShape(b.Store, shape); !ok {
		t.Fatal("测试前置：形状应已学习")
	}
	saveJoinMute(b, -100, 692, kindProfile, "资料广告", 0)
	if _, err := b.Store.Write.Exec(`UPDATE join_mutes SET shape=?
		WHERE chat_id=-100 AND user_id=692`, shape); err != nil {
		t.Fatal(err)
	}

	dropJoinMute(b, -100, 692)

	if _, ok := loadJoinMute(b.Store, -100, 692); ok {
		t.Fatal("限制记录应被删除")
	}
	if _, ok := lookupProfileShape(b.Store, shape); ok {
		t.Fatal("限制解除应连带删除资料形状")
	}
}

// 清理：30 天未命中的形状删除，近期形状保留。
func TestCleanupDataPrunesProfileShapes(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	now := time.Now().Unix()
	learnProfileShape(b.Store, "老形状abc", "promo", "样本", now-40*86400)
	learnProfileShape(b.Store, "新形状abc", "promo", "样本", now)

	CleanupData(b.Shared)

	if _, ok := lookupProfileShape(b.Store, "老形状abc"); ok {
		t.Error("30 天未命中的形状应被清理")
	}
	if _, ok := lookupProfileShape(b.Store, "新形状abc"); !ok {
		t.Error("近期形状不该被清理")
	}
}
