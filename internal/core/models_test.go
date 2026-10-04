package core_test

import (
	"math"
	"strings"
	"testing"

	"menshen/internal/core"
	"menshen/internal/testutil"
)

// TestModelOps：新模型的名字校验（上游存在、长度、重复）、价格非负、
// 部分更新与删除。
func TestModelOps(t *testing.T) {
	b, _ := testutil.NewTestBot(t, 1)
	sh := b.Shared
	if err := sh.AddUpstream(core.UpstreamPatch{Name: ptr("up"),
		BaseURL: ptr("https://x")}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ up, id string }{
		{"nope", "m"}, {"up", " "}, {"up", strings.Repeat("x", core.ModelNameMax)},
	} {
		if _, err := sh.AddModel(c.up, c.id, [4]float64{}); !isOpErr(err) {
			t.Fatalf("%s/%s 应被拒，得到 %v", c.up, c.id, err)
		}
	}
	if _, err := sh.AddModel("up", "m", [4]float64{-1}); !isOpErr(err) {
		t.Fatalf("负价应被拒，得到 %v", err)
	}
	if _, err := sh.AddModel("up", "m", [4]float64{math.NaN()}); !isOpErr(err) {
		t.Fatalf("NaN 应被拒，得到 %v", err)
	}

	name, err := sh.AddModel("up", "m", [4]float64{0.1, 0.2, 0.01, 0})
	if err != nil || name != "up/m" {
		t.Fatalf("AddModel = %q, %v", name, err)
	}
	if _, err := sh.AddModel("up", "m", [4]float64{}); !isOpErr(err) {
		t.Fatalf("重复模型应被拒，得到 %v", err)
	}

	if err := sh.UpdateModel(name, core.ModelPatch{
		Prices: [4]*float64{nil, ptr(0.5)}, Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	m := sh.Cache.Snap().Models[name]
	if m.PromptPrice != 0.1 || m.CompletionPrice != 0.5 || m.Enabled {
		t.Fatalf("补丁应只改补全价与启用，得到 %+v", m)
	}
	if err := sh.UpdateModel(name, core.ModelPatch{
		Prices: [4]*float64{ptr(-1.0)}}); !isOpErr(err) {
		t.Fatalf("负价更新应被拒，得到 %v", err)
	}
	if err := sh.UpdateModel("up/none", core.ModelPatch{Enabled: ptr(true)}); !isOpErr(err) {
		t.Fatalf("不存在的模型应被拒，得到 %v", err)
	}

	if err := sh.DeleteModel(name); err != nil {
		t.Fatal(err)
	}
	if sh.Cache.Snap().Models[name] != nil {
		t.Fatal("删除后模型仍在")
	}
}
