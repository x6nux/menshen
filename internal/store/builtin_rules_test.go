package store

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestSeedBuiltinRules 内置规则要补种进 ad_rules、默认启用且不经 AI 直接
// 最高档处置（enforce=0，命中先删后判），并且重复补种不会出现第二条。
func TestSeedBuiltinRules(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	countBuiltin := func() int {
		var n int
		if err := st.Read.QueryRow(
			`SELECT COUNT(*) FROM ad_rules WHERE source='builtin'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countBuiltin() != 0 {
		t.Fatal("Open 不该补种内置规则（那样会让所有单测的 ad_rules 多出一条）")
	}

	if err := SeedBuiltinRules(st); err != nil {
		t.Fatal(err)
	}
	if n := countBuiltin(); n != len(builtinRules) {
		t.Fatalf("应补种 %d 条内置规则，得到 %d 条", len(builtinRules), n)
	}
	var enabled, enforce int
	var pattern string
	if err := st.Read.QueryRow(`SELECT pattern,enabled,enforce FROM ad_rules
		WHERE source='builtin' LIMIT 1`).Scan(&pattern, &enabled, &enforce); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Error("内置规则应默认启用")
	}
	if enforce != 0 {
		t.Error("内置规则不该 enforce：命中先删后判，交复判定案")
	}

	// 幂等：再补一次不该多出规则。
	if err := SeedBuiltinRules(st); err != nil {
		t.Fatal(err)
	}
	if n := countBuiltin(); n != len(builtinRules) {
		t.Fatalf("重复补种不该新增规则，得到 %d 条", n)
	}

	// 规则本体要真的命中返利拉新文案，且不误伤纯讨论邀请码。
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("内置规则正则无法编译: %v", err)
	}
	ad := "快来看看你的个人 AI 智能体 Muse 。在加入后的 48 小时内通过“设置”兑现我的邀请码，" +
		"我们就能分别获得 10 亿个 Muse 词元。\n\n邀请码：M8P5J1\nhttps://muse.ai/join"
	if !re.MatchString(ad) {
		t.Error("内置规则应命中邀请码返利广告")
	}
	for _, clean := range []string{
		"muse各位老板都撸到手了吗？发一下你们的邀请码",
		"有人有L站的邀请码可以邀请我下吗",
		"这个项目的邀请码在 README 里 https://github.com/foo/bar",
	} {
		if re.MatchString(clean) {
			t.Errorf("内置规则不该命中正常讨论：%q", clean)
		}
	}
}
