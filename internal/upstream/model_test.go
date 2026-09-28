package upstream

import "testing"

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
