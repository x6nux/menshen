package core_test

import (
	"errors"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
	"menshen/internal/upstream"
)

func findUp(t *testing.T, sh *core.Shared, name string) *upstream.Upstream {
	t.Helper()
	for _, u := range sh.Cache.Snap().Upstreams {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("上游 %s 不存在", name)
	return nil
}

func isOpErr(err error) bool {
	var op *core.OpError
	return errors.As(err, &op)
}

// TestUpstreamOps：校验、默认值、按类型收敛能力、名下有模型不许删。
func TestUpstreamOps(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	sh := b.Shared

	for _, bad := range []core.UpstreamPatch{
		{Name: ptr("a/b"), BaseURL: ptr("https://x")},
		{Name: ptr("a"), BaseURL: ptr("ftp://x")},
		{Name: ptr("a"), BaseURL: ptr("https://x"), Weight: ptr(int64(0))},
	} {
		if err := sh.AddUpstream(bad); !isOpErr(err) {
			t.Fatalf("%+v 应被拒，得到 %v", bad, err)
		}
	}

	// 尾部斜杠去掉；没传权重/启用/类型/能力时用默认值。
	if err := sh.AddUpstream(core.UpstreamPatch{Name: ptr("a"),
		BaseURL: ptr("https://x/"), APIKey: ptr("k")}); err != nil {
		t.Fatal(err)
	}
	u := findUp(t, sh, "a")
	if u.BaseURL != "https://x" || u.Weight != 1 || u.Status != 1 ||
		u.EffectiveKind() != upstream.KindOpenAI || !u.SupportsChat || u.SupportsSystemOne {
		t.Fatalf("默认值不对：%+v", u)
	}
	if err := sh.AddUpstream(core.UpstreamPatch{Name: ptr("a"),
		BaseURL: ptr("https://y")}); !isOpErr(err) {
		t.Fatalf("同名上游应被拒，得到 %v", err)
	}

	// 换类型重置能力；chat-only 类型无论传什么都只开 chat。
	if err := sh.UpdateUpstream(u.ID, core.UpstreamPatch{
		Kind: ptr(upstream.KindCloudflare)}); err != nil {
		t.Fatal(err)
	}
	if u = findUp(t, sh, "a"); u.SupportsChat || !u.SupportsSystemOne {
		t.Fatalf("换成 Cloudflare 应重置为默认能力，得到 %+v", u)
	}
	if err := sh.UpdateUpstream(u.ID, core.UpstreamPatch{Kind: ptr(upstream.KindAnthropic),
		Chat: ptr(false), SystemOne: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if u = findUp(t, sh, "a"); !u.SupportsChat || u.SupportsSystemOne {
		t.Fatalf("chat-only 类型应强制只开 chat，得到 %+v", u)
	}
	if err := sh.UpdateUpstream(u.ID, core.UpstreamPatch{Weight: ptr(int64(0))}); !isOpErr(err) {
		t.Fatalf("权重 0 应被拒，得到 %v", err)
	}
	if err := sh.UpdateUpstream(u.ID, core.UpstreamPatch{
		BaseURL: ptr("https://z/"), Enabled: ptr(false), Weight: ptr(int64(3))}); err != nil {
		t.Fatal(err)
	}
	if u = findUp(t, sh, "a"); u.BaseURL != "https://z" || u.Status != 0 || u.Weight != 3 {
		t.Fatalf("更新未生效：%+v", u)
	}

	if _, err := sh.Store.Write.Exec(`INSERT INTO models (name,prompt_price,completion_price) VALUES ('a/m',0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := sh.DeleteUpstream(u.ID); !isOpErr(err) {
		t.Fatalf("名下有模型时应拒绝删除，得到 %v", err)
	}
	if _, err := sh.Store.Write.Exec(`DELETE FROM models`); err != nil {
		t.Fatal(err)
	}
	if err := sh.Cache.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := sh.DeleteUpstream(u.ID); err != nil {
		t.Fatal(err)
	}
	if len(sh.Cache.Snap().Upstreams) != 0 {
		t.Fatal("删除后上游仍在")
	}
}
