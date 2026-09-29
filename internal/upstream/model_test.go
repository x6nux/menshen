package upstream

import (
	"fmt"
	"testing"
	"time"
)

// TestSplitModelName 守的是模型名的唯一解析规则：<上游名>/<模型ID>，
// **按第一个 "/" 切** —— 模型 ID 自身可以带 "/"（OpenRouter 形态），
// 切错了就会把 openrouter/openai/gpt-4o 发给错误的上游。
func TestSplitModelName(t *testing.T) {
	cases := []struct{ in, wantUp, wantID string }{
		{"a/b", "a", "b"},
		{"openrouter/openai/gpt-4o", "openrouter", "openai/gpt-4o"},
		{"gpt-4o", "", "gpt-4o"}, // 旧格式：无前缀
		{"/x", "", "/x"},         // 空上游名按无前缀处理
		{"a/", "", "a/"},         // 空模型 ID 同样按无前缀
		{"", "", ""},
	}
	for _, c := range cases {
		up, id := SplitModelName(c.in)
		if up != c.wantUp || id != c.wantID {
			t.Errorf("SplitModelName(%q) = (%q, %q)，期望 (%q, %q)",
				c.in, up, id, c.wantUp, c.wantID)
		}
		m := &Model{Name: c.in}
		if m.UpstreamName() != c.wantUp || m.ModelID() != c.wantID {
			t.Errorf("Model{%q} 的便捷读法不一致", c.in)
		}
	}
}

// TestPickWeightedSticky：同一个 key 稳定选中同一个上游，权重体现在分布上，
// 候选列表始终含全部可用上游且不重复。
func TestPickWeightedSticky(t *testing.T) {
	ups := []*Upstream{
		{ID: 1, Name: "a", Status: 1, Weight: 1, SupportsChat: true},
		{ID: 2, Name: "b", Status: 1, Weight: 9, SupportsChat: true},
	}
	first := Pick(ups, EPChat, "same")[0].ID
	for i := 0; i < 10; i++ {
		if got := Pick(ups, EPChat, "same")[0].ID; got != first {
			t.Fatalf("同一 key 的 sticky 选择必须稳定，得到 %d/%d", got, first)
		}
	}
	counts := map[int64]int{}
	for i := 0; i < 1000; i++ {
		counts[Pick(ups, EPChat, fmt.Sprintf("k%d", i))[0].ID]++
	}
	if counts[2] < 800 || counts[1] < 50 {
		t.Errorf("权重 9:1 的分布不对: %v", counts)
	}
	out := Pick(ups, EPChat, "x")
	if len(out) != 2 || out[0].ID == out[1].ID {
		t.Errorf("候选列表应含全部上游且不重复: %v", out)
	}
}

// TestPickHugeWeightDoesNotBlowUp：权重被写成 1e9 时不得按权重物化切片
// （旧实现会一次性分配 ~8GB，直接 OOM 掉整个多租户进程）。
func TestPickHugeWeightDoesNotBlowUp(t *testing.T) {
	ups := []*Upstream{
		{ID: 1, Status: 1, Weight: 1e9, SupportsChat: true},
		{ID: 2, Status: 1, Weight: 1, SupportsChat: true},
	}
	done := make(chan []*Upstream, 1)
	go func() { done <- Pick(ups, EPChat, "k") }()
	select {
	case out := <-done:
		if len(out) != 2 {
			t.Fatalf("候选列表不对: %v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超大权重把 Pick 卡住了（仍在物化分配）")
	}
}

// TestPickFiltersDisabledAndUnsupported：停用或不支持该端点的上游不进候选。
func TestPickFiltersDisabledAndUnsupported(t *testing.T) {
	ups := []*Upstream{
		{ID: 1, Status: 0, Weight: 1, SupportsChat: true},
		{ID: 2, Status: 1, Weight: 1, SupportsChat: false},
		{ID: 3, Status: 1, Weight: 1, SupportsChat: true},
	}
	out := Pick(ups, EPChat, "k")
	if len(out) != 1 || out[0].ID != 3 {
		t.Fatalf("只应留下启用的、支持该端点的上游: %v", out)
	}
}
