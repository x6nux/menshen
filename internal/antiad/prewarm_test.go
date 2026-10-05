package antiad

import (
	"testing"

	"menshen/internal/testutil"
)

// TestUserPhotoCount：查得到要缓存；查失败要 ok=false 且不缓存（下次重试）。
func TestUserPhotoCount(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)

	// 查得到：total_count=0 是合法结果（无头像），必须与失败区分开。
	b.TG.(*testutil.FakeTG).Resp["getUserProfilePhotos"] =
		`{"ok":true,"result":{"total_count":0,"photos":[]}}`
	n, ok := userPhotoCount(b, 555)
	if !ok || n != 0 {
		t.Fatalf("首次查询 = (%d,%v)，期望 (0,true)", n, ok)
	}
	if got := b.TG.(*testutil.FakeTG).CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("首次应查 1 次，得到 %d", got)
	}
	if _, ok := userPhotoCount(b, 555); !ok {
		t.Fatal("第二次应命中缓存")
	}
	if got := b.TG.(*testutil.FakeTG).CountCalls("getUserProfilePhotos"); got != 1 {
		t.Fatalf("缓存命中不应再查，得到 %d 次", got)
	}

	// 失败：ok=false 且不进缓存。
	b2, _ := testutil.NewTestBot(t, 1)
	fake := b2.TG.(*testutil.FakeTG)
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
