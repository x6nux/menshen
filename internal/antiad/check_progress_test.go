package antiad

import (
	"strings"
	"testing"

	"menshen/internal/testutil"
)

// TestCheckShowsModelWithoutUpstreamPrefix：/check 卡片上的模型名要剥掉
// 上游前缀 —— <上游名>/<模型ID> 是内部路由与流水口径（换模型校准阈值时
// 要知道是谁答的），发给群里的卡片只该有模型 ID。上游名保证不含 /
// （ValidUpstreamName 校验），所以剥的总是第一段。
func TestCheckShowsModelWithoutUpstreamPrefix(t *testing.T) {
	b, fake := testutil.NewTestBot(t, 1)
	testutil.EnableAntiad(t, b, -100)
	fakeAI(t, b, nil) // 两级都判广告
	// 覆盖 fakeAI 配的单值键：列表键优先（见 ModelsFor）。
	setGlobal(t, b, "antiad_so_models", `["fake/so-model"]`)
	setGlobal(t, b, "antiad_llm_models", `["fake/llm-model"]`)
	recordMessage(b, -100, 1, 555, "加微信买号 日入5000", 1700000000, "")

	HandleGroupMessage(b, testutil.GroupMsg(-100, 777, 9, "/check 555"))
	waitIdle(t, b)

	edits := checkEdits(fake)
	if len(edits) == 0 {
		t.Fatal("复查应有逐步编辑的卡片")
	}
	final := edits[len(edits)-1]
	if !strings.Contains(final, "（so-model）") || !strings.Contains(final, "（llm-model）") {
		t.Errorf("卡片应显示剥掉上游前缀的模型 ID：\n%s", final)
	}
	if strings.Contains(final, "fake/") {
		t.Errorf("卡片不该出现上游前缀：\n%s", final)
	}
}

// TestModelShort：剥前缀只剥第一段 —— 模型 ID 自己可以带 /
// （如 org/model 的社区命名），上游名不会（校验保证）。
func TestModelShort(t *testing.T) {
	for full, want := range map[string]string{
		"fake/gpt-4o":          "gpt-4o",
		"fake/org/deepseek-v3": "org/deepseek-v3", // ID 自带 / 时只剥上游段
		"bare-model":           "bare-model",      // 没有 / 的名字原样返回
		"":                     "",
	} {
		if got := modelShort(full); got != want {
			t.Errorf("modelShort(%q) = %q，应为 %q", full, got, want)
		}
	}
}
