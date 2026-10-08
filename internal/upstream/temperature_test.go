package upstream

// 「模型不接受 temperature」这条路的两端：请求体去掉该参数（BuildBody），
// 以及上游错误的识别（TemperatureRejected）。

import (
	"testing"
)

// TestBuildBodyDropsRejectedTemperature：被上游拒绝过 temperature 的模型，
// 各渠道的请求体都不再带该参数；没被拒过的模型原样保留。
func TestBuildBodyDropsRejectedTemperature(t *testing.T) {
	const rejected = "no-temp-buildbody"
	MarkTemperatureUnsupported(rejected)

	payload := map[string]any{
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": "x"}},
	}
	ups := map[string]*Upstream{
		"openai":     {Name: "o"},
		"responses":  {Name: "r", Kind: KindOpenAIResp},
		"anthropic":  {Name: "a", Kind: KindAnthropic},
		"gemini":     {Name: "g", Kind: KindGemini},
		"cloudflare": {Name: "c", Kind: KindCloudflare},
	}
	for name, u := range ups {
		body := u.BuildBody(EPChat, "o/"+rejected, payload)
		if _, ok := body["temperature"]; ok {
			t.Errorf("%s：被拒 temperature 的模型不该带该参数: %v", name, body)
		}
		if gen, ok := body["generationConfig"].(map[string]any); ok {
			if _, has := gen["temperature"]; has {
				t.Errorf("%s：generationConfig 里不该有 temperature: %v", name, gen)
			}
		}
		// 没被拒过的模型照旧带上（判定要可复现，temperature=0）。
		body = u.BuildBody(EPChat, "o/other-model", payload)
		switch name {
		case "gemini":
			gen, _ := body["generationConfig"].(map[string]any)
			if gen == nil || gen["temperature"] != 0 {
				t.Errorf("gemini：未登记的模型应保留 temperature: %v", body)
			}
		default:
			if body["temperature"] != 0 {
				t.Errorf("%s：未登记的模型应保留 temperature: %v", name, body)
			}
		}
	}
	// 载荷是调用方共用的那份，不能被就地改掉（并发多路与重试都会复用它）。
	if _, ok := payload["temperature"]; !ok {
		t.Error("BuildBody 不该改动调用方的载荷")
	}
}

// TestTemperatureRejected：4xx 且消息提到 temperature 才算，5xx 与无关
// 消息都不算。
func TestTemperatureRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		want   bool
	}{
		{"Claude 系文案", 400, "`temperature` is deprecated for this model", true},
		{"推理模型文案", 400, "Unsupported value: 'temperature' does not support 0", true},
		{"字段不认识", 422, "unknown field temperature", true},
		{"上游故障", 503, "temperature backend overloaded", false},
		{"与参数无关", 400, "invalid api key", false},
		{"没有正文", 400, "", false},
	}
	for _, c := range cases {
		if got := TemperatureRejected(c.status, c.msg); got != c.want {
			t.Errorf("%s：want %v got %v", c.name, c.want, got)
		}
	}
}
