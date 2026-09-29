package billing

import (
	"testing"

	"menshen/internal/upstream"
)

// TestExtractUsageOpenAI：OpenAI 的 prompt_tokens 包含 cached_tokens，
// 不减掉就会出现「四类之和 > 真实总量」，成本被重复计算。
func TestExtractUsageOpenAINormalizesCached(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,
		"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":5}}}`)
	u := ExtractUsage(upstream.EPChat, body)
	if u.PromptTokens != 20 || u.CompletionTokens != 20 ||
		u.CacheReadTokens != 80 || u.CacheWriteTokens != 5 {
		t.Fatalf("归一化结果不对: %+v", u)
	}
	if got := u.PromptTokens + u.CompletionTokens + u.CacheReadTokens; got != 120 {
		t.Errorf("非缓存部分之和应等于 prompt+completion，得到 %d", got)
	}
}

// TestExtractUsageSystemOne：systemone 是扁平结构，input_tokens 同样包含
// cached_tokens。
func TestExtractUsageSystemOne(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":50,"output_tokens":7,
		"input_tokens_details":{"cached_tokens":30}}}`)
	u := ExtractUsage(upstream.EPSystemOne, body)
	if u.PromptTokens != 20 || u.CompletionTokens != 7 || u.CacheReadTokens != 30 {
		t.Fatalf("systemone 归一化不对: %+v", u)
	}
}

// TestExtractUsageBadBodyIsZero：用量只影响面板上的开销数字，解析失败
// 必须返回零值而不是报错——不能让一次统计失败把判定结果一起丢掉。
func TestExtractUsageBadBodyIsZero(t *testing.T) {
	for _, body := range []string{
		`<html>502 Bad Gateway</html>`,
		`{"usage":null}`,
		`{}`,
		``,
	} {
		if u := ExtractUsage(upstream.EPChat, []byte(body)); u != (Usage{}) {
			t.Errorf("body=%q 应返回零值，得到 %+v", body, u)
		}
	}
	// 上游字段异常（cached 比 prompt 还大）不得出现负用量。
	u := ExtractUsage(upstream.EPChat, []byte(`{"usage":{"prompt_tokens":10,
		"prompt_tokens_details":{"cached_tokens":50}}}`))
	if u.PromptTokens < 0 || u.CacheReadTokens < 0 {
		t.Errorf("负数应被钳到 0: %+v", u)
	}
}

func TestMergeUsage(t *testing.T) {
	a := Usage{PromptTokens: 1, CompletionTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4}
	b := Usage{PromptTokens: 10, CompletionTokens: 20, CacheReadTokens: 30}
	got := MergeUsage(a, b)
	want := Usage{PromptTokens: 11, CompletionTokens: 22, CacheReadTokens: 33, CacheWriteTokens: 4}
	if got != want {
		t.Errorf("MergeUsage = %+v, 期望 %+v", got, want)
	}
}

// TestComputeCost：四类 token 各乘各的单价，向上取整为 quota，
// 零用量与脏数据（负价）都不得产生负数成本。
func TestComputeCost(t *testing.T) {
	m := &upstream.Model{PromptPrice: 1, CompletionPrice: 2,
		CacheReadPrice: 0.1, CacheWritePrice: 3}

	if got := ComputeCost(Usage{PromptTokens: 1_000_000}, m); got != QuotaPerUSD {
		t.Errorf("1M 输入 token（$1）应折 %d quota，得到 %d", QuotaPerUSD, got)
	}
	if got := ComputeCost(Usage{}, m); got != 0 {
		t.Errorf("零用量应零成本，得到 %d", got)
	}
	// 向上取整：极小用量也要有非零成本，否则面板会把真实开销显示成 0。
	if got := ComputeCost(Usage{PromptTokens: 1}, m); got != 1 {
		t.Errorf("极小用量应向上取整为 1 quota，得到 %d", got)
	}
	// 老库里的脏数据（负价）不得算出负数。
	if got := ComputeCost(Usage{PromptTokens: 100},
		&upstream.Model{PromptPrice: -1}); got != 0 {
		t.Errorf("负价应零成本，得到 %d", got)
	}
}

func TestFormatUSDFine(t *testing.T) {
	cases := []struct {
		q    int64
		want string
	}{
		{0, "$0.00000000"},
		{QuotaPerUSD, "$1.00000000"},
		{QuotaPerUSD / 2, "$0.50000000"},
		{-QuotaPerUSD / 2, "-$0.50000000"},
	}
	for _, c := range cases {
		if got := FormatUSDFine(c.q); got != c.want {
			t.Errorf("FormatUSDFine(%d) = %q, 期望 %q", c.q, got, c.want)
		}
	}
}
