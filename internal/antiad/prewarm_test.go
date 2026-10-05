package antiad

import (
	"testing"
	"time"

	"menshen/internal/testutil"
	"menshen/internal/tg"
)

func TestProfileEmpty(t *testing.T) {
	cases := []struct {
		name string
		p    senderProfile
		want bool
	}{
		{"全空", senderProfile{}, true},
		{"只有空白名字", senderProfile{FirstName: "  "}, true},
		{"有简介", senderProfile{Bio: "hello"}, false},
		{"有用户名", senderProfile{Username: "abc"}, false},
		{"有名字", senderProfile{FirstName: "小明"}, false},
	}
	for _, c := range cases {
		if got := c.p.ProfileEmpty(); got != c.want {
			t.Errorf("%s: ProfileEmpty = %v，期望 %v", c.name, got, c.want)
		}
	}
}

func TestPrewarmStateFields(t *testing.T) {
	zero := 0
	p := senderProfile{Photos: &zero, PhotoKnown: true}
	if !p.PhotoKnown || p.Photos == nil || *p.Photos != 0 {
		t.Fatal("Photos/PhotoKnown 应可表达「查到了，0 张」")
	}
	st := adState{JoinCheck: false, PrewarmCheck: true}
	if !st.PrewarmCheck {
		t.Fatal("PrewarmCheck 应为真")
	}
}

func TestPrewarmCandidate(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	if err := b.PutSetting("antiad_prewarm", "1"); err != nil {
		t.Fatal(err)
	}
	snap := b.Cache.Snap()
	now := time.Now().Unix()
	gm := groupMember{Known: true, MsgCount: 1, JoinedAt: now - 3600}
	msg := func(text string) *tg.Message {
		return &tg.Message{From: &tg.TGUser{ID: 555}, Text: text}
	}
	if !prewarmCandidate(b, snap, gm, msg("哈喽"), false) {
		t.Error("新成员首条短招呼应命中")
	}
	if !prewarmCandidate(b, snap, gm, msg("擦"), false) {
		t.Error("无意义短词也应命中（判定交给 AI）")
	}
	if prewarmCandidate(b, snap, gm, msg("麻烦问下这个怎么配置"), false) {
		t.Error("长消息不该命中")
	}
	if prewarmCandidate(b, snap, gm, msg("哈喽"), true) {
		t.Error("编辑过的消息不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 2, JoinedAt: now - 3600},
		msg("哈喽"), false) {
		t.Error("非首条消息不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 1}, msg("哈喽"), false) {
		t.Error("入群时间未知不该命中")
	}
	if prewarmCandidate(b, snap, groupMember{MsgCount: 1,
		JoinedAt: now - int64(prewarmJoinWindow/time.Second) - 60},
		msg("哈喽"), false) {
		t.Error("超出进群窗口不该命中")
	}
	withLink := msg("哈喽")
	withLink.Entities = []tg.MessageEntity{{Type: "url"}}
	if prewarmCandidate(b, snap, gm, withLink, false) {
		t.Error("带链接的消息不该命中")
	}

	// 开关关闭：一律不圈。
	b2, _ := testutil.NewTestBot(t, 2)
	testutil.EnableAntiad(t, b2, -100)
	if prewarmCandidate(b2, b2.Cache.Snap(), gm, msg("哈喽"), false) {
		t.Error("开关关闭时不该命中")
	}
}

// TestJoinMuteKindRoundTrip：kind 要能落库读回，prewarm 与 profile 区分开。
func TestJoinMuteKindRoundTrip(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	saveJoinMute(b, -100, 555, kindPrewarm, "前置号", 0)
	rec, ok := loadJoinMute(b.Store, -100, 555)
	if !ok || rec.Kind != kindPrewarm {
		t.Fatalf("kind 读回 = %q,%v，期望 prewarm", rec.Kind, ok)
	}
	// upsert 更新 kind：同一个人再次被资料类命中时以最后一次为准。
	saveJoinMute(b, -100, 555, kindProfile, "资料广告", 0)
	if rec, _ = loadJoinMute(b.Store, -100, 555); rec.Kind != kindProfile {
		t.Fatalf("upsert 后 kind = %q，期望 profile", rec.Kind)
	}
}

// TestUserPhotoCount：查得到要缓存；查失败要 ok=false 且不缓存（下次重试）。
func TestUserPhotoCount(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	fake := b.TG.(*testutil.FakeTG)

	// 查得到：用非零张数，确认解析的是 total_count 本身而不是恒零。
	fake.Resp["getUserProfilePhotos"] =
		`{"ok":true,"result":{"total_count":3,"photos":[]}}`
	n, ok := userPhotoCount(b, 555)
	if !ok || n != 3 {
		t.Fatalf("首次查询 = (%d,%v)，期望 (3,true)", n, ok)
	}
	if calls := fake.Calls("getUserProfilePhotos"); len(calls) != 1 ||
		calls[0]["user_id"] != float64(555) || calls[0]["limit"] != float64(1) {
		t.Fatalf("查询载荷 = %#v，期望 user_id=555、limit=1", calls)
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("首次应查 1 次，得到 %d", got)
	}
	if n, ok := userPhotoCount(b, 555); !ok || n != 3 {
		t.Fatalf("第二次缓存读回 = (%d,%v)，期望 (3,true)", n, ok)
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("缓存命中不应再查，得到 %d 次", got)
	}

	// total_count=0 是合法结果（无头像），必须与失败区分开。
	b0, _ := testutil.NewTestBot(t, 1)
	b0.TG.(*testutil.FakeTG).Resp["getUserProfilePhotos"] =
		`{"ok":true,"result":{"total_count":0,"photos":[]}}`
	if n, ok := userPhotoCount(b0, 555); !ok || n != 0 {
		t.Fatalf("零头像 = (%d,%v)，期望 (0,true)", n, ok)
	}

	// 失败：ok=false 且不进缓存。
	b2, _ := testutil.NewTestBot(t, 1)
	fake = b2.TG.(*testutil.FakeTG)
	fake.RespFunc = func(method string, _ map[string]any) (string, bool) {
		if method == "getUserProfilePhotos" {
			return `{"ok":false,"description":"boom"}`, true
		}
		return "", false
	}
	if _, ok := userPhotoCount(b2, 556); ok {
		t.Fatal("失败应返回 ok=false")
	}
	if _, ok := userPhotoCount(b2, 556); ok {
		t.Fatal("失败不应被缓存成成功")
	}
	if got := fake.CountCalls("getUserProfilePhotos"); got != 2 {
		t.Fatalf("失败不应缓存，应查 2 次，得到 %d", got)
	}
}
