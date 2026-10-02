package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// TestParseKind：空串与大小写都归一；未知类型必须报错，防止面板存进
// 一个协议层不认识的 kind 后静默失效。
func TestParseKind(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
		bad  bool
	}{
		{"", KindOpenAI, false},
		{"openai", KindOpenAI, false},
		{" OpenAI ", KindOpenAI, false},
		{"openai-responses", KindOpenAIResp, false},
		{"anthropic", KindAnthropic, false},
		{"gemini", KindGemini, false},
		{"cloudflare", KindCloudflare, false},
		{"CLOUDFLARE", KindCloudflare, false},
		{"cohere", "", true},
	}
	for _, c := range cases {
		got, err := ParseKind(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseKind(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseKind(%q) = (%q, %v)，期望 %q", c.in, got, err, c.want)
		}
	}
	// 零值对象按 openai 处理：老数据与手工构造的测试对象不用显式赋值。
	if k := (&Upstream{}).EffectiveKind(); k != KindOpenAI {
		t.Errorf("零值 kind 应归一成 openai，得到 %q", k)
	}
	if k := (&Upstream{Kind: KindCloudflare}).EffectiveKind(); k != KindCloudflare {
		t.Errorf("cloudflare 应原样保留，得到 %q", k)
	}
}

// TestKindPolicy：chat-only 判定、默认能力与能力收敛。
func TestKindPolicy(t *testing.T) {
	for _, k := range []Kind{KindOpenAIResp, KindAnthropic, KindGemini} {
		if !k.ChatOnly() {
			t.Errorf("%s 应是 chat-only", k)
		}
		if _, so := k.DefaultCaps(); so {
			t.Errorf("%s 默认不该开 systemone", k)
		}
		if chat, so := k.ResolveCaps(false, true, true); !chat || so {
			t.Errorf("%s 应强制 chat、拒绝 systemone，得到 %v/%v", k, chat, so)
		}
	}
	if KindOpenAI.ChatOnly() || KindCloudflare.ChatOnly() {
		t.Error("openai 与 cloudflare 不该是 chat-only")
	}
	if chat, so := KindCloudflare.DefaultCaps(); chat || !so {
		t.Errorf("Cloudflare 默认应是主判定，得到 %v/%v", chat, so)
	}
	if chat, so := KindOpenAI.DefaultCaps(); !chat || so {
		t.Errorf("OpenAI 默认应是 chat，得到 %v/%v", chat, so)
	}
	// 没显式传能力时用类型默认值；决策渠道允许自定义组合。
	if chat, so := KindCloudflare.ResolveCaps(true, true, true); !chat || !so {
		t.Errorf("Cloudflare 允许 chat+主判定，得到 %v/%v", chat, so)
	}
}

// TestURLPerKind：各类型的端点路径与不支持的端点。
func TestURLPerKind(t *testing.T) {
	cases := []struct {
		label, name, base, model, want string
		kind                           Kind
		ep                             Endpoint
	}{
		{"openai chat", "o", "https://api.example.com/", "o/gpt", "https://api.example.com/v1/chat/completions", KindOpenAI, EPChat},
		{"openai systemone", "o", "https://api.example.com", "o/jev", "https://api.example.com/v1/systemone", KindOpenAI, EPSystemOne},
		{"responses", "r", "https://api.openai.com", "r/gpt-5", "https://api.openai.com/v1/responses", KindOpenAIResp, EPChat},
		{"anthropic", "a", "https://api.anthropic.com", "a/claude-sonnet-4-5", "https://api.anthropic.com/v1/messages", KindAnthropic, EPChat},
		{"gemini", "g", "https://generativelanguage.googleapis.com/v1beta", "g/gemini-2.5-flash", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", KindGemini, EPChat},
		{"cloudflare", "c", "https://api.cloudflare.com/client/v4/accounts/acc123/", "c/@cf/cloudflare/clef", "https://api.cloudflare.com/client/v4/accounts/acc123/ai/run/@cf/cloudflare/clef", KindCloudflare, EPSystemOne},
	}
	for _, c := range cases {
		u := &Upstream{Name: c.name, Kind: c.kind, BaseURL: c.base}
		got, err := u.URL(c.ep, c.model)
		if err != nil || got != c.want {
			t.Errorf("%s: URL = %q, %v，期望 %q", c.label, got, err, c.want)
		}
	}
	// chat-only 类型不解析 systemone 端点。
	for _, k := range []Kind{KindOpenAIResp, KindAnthropic, KindGemini} {
		u := &Upstream{Name: "x", Kind: k, BaseURL: "https://x"}
		if _, err := u.URL(EPSystemOne, "x/m"); err == nil {
			t.Errorf("%s 不该解析出 systemone 端点", k)
		}
		if u.Supports(EPSystemOne) {
			t.Errorf("%s 的 Supports(systemone) 应为 false", k)
		}
	}
	// OpenAI 渠道路径与请求体模型保持不变。
	o := &Upstream{Name: "o", BaseURL: "https://api.example.com",
		SupportsChat: true, SupportsSystemOne: true}
	if body := o.BuildBody(EPSystemOne, "o/some/model", map[string]any{"state": "x"}); body["model"] != "some/model" {
		t.Errorf("OpenAI 渠道应原样透传模型 ID，得到 %v", body["model"])
	}
}

// TestBuildBodyPerKind：请求体按渠道改写（非 OpenAI 渠道走非流式）。
func TestBuildBodyPerKind(t *testing.T) {
	payload := map[string]any{
		"temperature":    0,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages": []map[string]string{
			{"role": "system", "content": "SYS"},
			{"role": "user", "content": "USER"},
		},
	}

	o := &Upstream{Name: "o"}
	body := o.BuildBody(EPChat, "o/gpt-4o", payload)
	if body["model"] != "gpt-4o" || body["stream"] != true {
		t.Errorf("OpenAI 渠道应原样透传并补 model: %v", body)
	}
	// chat 载荷没写 stream 时统一补 true；systemone 是 TypeSafe 原生
	// 形态，不能带 stream。
	body = o.BuildBody(EPChat, "o/gpt-4o", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "x"}}})
	if body["stream"] != true {
		t.Errorf("chat 载荷缺 stream 时默认流式: %v", body)
	}
	body = o.BuildBody(EPSystemOne, "o/jev", map[string]any{"state": "x"})
	if _, ok := body["stream"]; ok {
		t.Errorf("systemone 不该带 stream: %v", body)
	}

	r := &Upstream{Name: "r", Kind: KindOpenAIResp}
	body = r.BuildBody(EPChat, "r/gpt-5", payload)
	if body["model"] != "gpt-5" || body["stream"] != true {
		t.Errorf("Responses 渠道应带 model 且走流式: %v", body)
	}
	if body["instructions"] != "SYS" {
		t.Errorf("Responses 的 system 提示应走 instructions: %v", body)
	}
	if in, ok := body["input"].([]map[string]string); !ok || len(in) != 1 || in[0]["role"] != "user" {
		t.Errorf("Responses 的 input 组装不对: %v", body["input"])
	}

	a := &Upstream{Name: "a", Kind: KindAnthropic}
	body = a.BuildBody(EPChat, "a/claude", payload)
	if body["system"] != "SYS" || body["max_tokens"] != anthropicDefaultMaxTokens {
		t.Errorf("Anthropic 应把 system 提顶层并带 max_tokens: %v", body)
	}
	if body["stream"] != true {
		t.Errorf("Anthropic 应走流式: %v", body)
	}
	if msgs, ok := body["messages"].([]map[string]string); !ok || len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Errorf("Anthropic 的 messages 应剔掉 system: %v", body["messages"])
	}

	g := &Upstream{Name: "g", Kind: KindGemini}
	body = g.BuildBody(EPChat, "g/gem", payload)
	if _, ok := body["systemInstruction"]; !ok {
		t.Errorf("Gemini 应带 systemInstruction: %v", body)
	}
	if cs, ok := body["contents"].([]map[string]any); !ok || len(cs) != 1 || cs[0]["role"] != "user" {
		t.Errorf("Gemini 的 contents 组装不对: %v", body["contents"])
	}

	c := &Upstream{Name: "c", Kind: KindCloudflare}
	body = c.BuildBody(EPChat, "c/@cf/meta/llama-3.1-8b-instruct", payload)
	if body["stream"] != true {
		t.Errorf("Cloudflare chat 应走流式: %v", body)
	}
	if _, ok := body["model"]; ok {
		t.Error("Cloudflare chat 请求不该带 model")
	}
	body = c.BuildBody(EPSystemOne, "c/@cf/cloudflare/clef-flash",
		map[string]any{"state": "x", "questions": map[string]any{}})
	if body["model"] != "clef-flash" {
		t.Errorf("Cloudflare 主判定的 model 应只保留最后一段，得到 %v", body["model"])
	}
}

// TestNormalizeResponsePerKind：各家响应翻译回统一形状。
func TestNormalizeResponsePerKind(t *testing.T) {
	type chatOut struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			Details          struct {
				Cached     int `json:"cached_tokens"`
				CacheWrite int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	decode := func(raw []byte) chatOut {
		t.Helper()
		var out chatOut
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("翻译结果不是 JSON: %v", err)
		}
		return out
	}

	r := &Upstream{Name: "r", Kind: KindOpenAIResp}
	raw, err := r.NormalizeResponse(EPChat, []byte(`{
		"output":[{"type":"reasoning","summary":[{"text":"think"}]},
		          {"type":"message","content":[{"type":"output_text","text":"hello "},{"type":"output_text","text":"world"}]}],
		"usage":{"input_tokens":30,"output_tokens":7,"input_tokens_details":{"cached_tokens":10}}}`))
	if err != nil {
		t.Fatalf("Responses 翻译失败: %v", err)
	}
	out := decode(raw)
	if out.Choices[0].Message.Content != "hello world" || out.Choices[0].Message.ReasoningContent != "think" {
		t.Errorf("Responses 正文/思考翻译不对: %+v", out.Choices[0].Message)
	}
	if out.Usage.PromptTokens != 30 || out.Usage.CompletionTokens != 7 || out.Usage.Details.Cached != 10 {
		t.Errorf("Responses usage 翻译不对: %+v", out.Usage)
	}

	a := &Upstream{Name: "a", Kind: KindAnthropic}
	raw, err = a.NormalizeResponse(EPChat, []byte(`{"content":[{"type":"text","text":"verdict"}],
		"usage":{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}`))
	if err != nil {
		t.Fatalf("Anthropic 翻译失败: %v", err)
	}
	out = decode(raw)
	if out.Choices[0].Message.Content != "verdict" {
		t.Errorf("Anthropic 正文翻译不对: %+v", out.Choices[0].Message)
	}
	// prompt = 11 + 5 + 2（含缓存总量），cached / cache_write 分列。
	if out.Usage.PromptTokens != 18 || out.Usage.Details.Cached != 5 || out.Usage.Details.CacheWrite != 2 {
		t.Errorf("Anthropic usage 翻译不对: %+v", out.Usage)
	}

	g := &Upstream{Name: "g", Kind: KindGemini}
	raw, err = g.NormalizeResponse(EPChat, []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],
		"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2}}`))
	if err != nil {
		t.Fatalf("Gemini 翻译失败: %v", err)
	}
	out = decode(raw)
	if out.Choices[0].Message.Content != "ok" || out.Usage.PromptTokens != 9 || out.Usage.CompletionTokens != 2 {
		t.Errorf("Gemini 翻译不对: %+v / %+v", out.Choices[0].Message, out.Usage)
	}

	c := &Upstream{Name: "c", Kind: KindCloudflare}
	raw, err = c.NormalizeResponse(EPChat, []byte(`{"result":{"response":"hi","usage":{"prompt_tokens":4,"completion_tokens":1}},"success":true,"errors":[]}`))
	if err != nil {
		t.Fatalf("Cloudflare chat 翻译失败: %v", err)
	}
	if out = decode(raw); out.Choices[0].Message.Content != "hi" {
		t.Errorf("Cloudflare chat 正文翻译不对: %+v", out.Choices[0].Message)
	}
	raw, err = c.NormalizeResponse(EPSystemOne, []byte(`{"result":{"answers":{"a":1}},"success":true}`))
	if err != nil || string(raw) != `{"answers":{"a":1}}` {
		t.Errorf("Cloudflare systemone 应只拆信封，得到 %q, %v", raw, err)
	}
	if _, err := c.NormalizeResponse(EPChat, []byte(`{"success":false,"errors":[{"code":1,"message":"bad"}]}`)); err == nil {
		t.Error("Cloudflare 失败信封应报错")
	}
	if _, err := r.NormalizeResponse(EPChat, []byte(`{"error":{"message":"boom"}}`)); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("上游 error 字段应转成错误，得到 %v", err)
	}
}

// TestSetAuth：鉴权头按渠道类型。
func TestSetAuth(t *testing.T) {
	newReq := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		return req
	}
	a := &Upstream{Kind: KindAnthropic, APIKey: "ak"}
	req := newReq()
	a.SetAuth(req)
	if req.Header.Get("x-api-key") != "ak" || req.Header.Get("anthropic-version") == "" {
		t.Errorf("Anthropic 鉴权头不对: %v", req.Header)
	}
	g := &Upstream{Kind: KindGemini, APIKey: "gk"}
	req = newReq()
	g.SetAuth(req)
	if req.Header.Get("x-goog-api-key") != "gk" || req.Header.Get("Authorization") != "" {
		t.Errorf("Gemini 鉴权头不对: %v", req.Header)
	}
	o := &Upstream{APIKey: "sk"}
	req = newReq()
	o.SetAuth(req)
	if req.Header.Get("Authorization") != "Bearer sk" {
		t.Errorf("默认鉴权头不对: %v", req.Header)
	}
}

// TestUnwrapCF：解 Cloudflare 的 {result,success,errors} 信封。
func TestUnwrapCF(t *testing.T) {
	ok := `{"result":{"model":"clef","answers":{}},"success":true,"errors":[],"messages":[]}`
	got, err := UnwrapCF([]byte(ok))
	if err != nil || string(got) != `{"model":"clef","answers":{}}` {
		t.Fatalf("解包成功信封失败: %q, %v", got, err)
	}

	bad := `{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`
	if _, err := UnwrapCF([]byte(bad)); err == nil ||
		!strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("失败信封应带出可读原因，得到 %v", err)
	}
	if msg := CFErrorMessage([]byte(bad)); msg != "[10000] Authentication error" {
		t.Errorf("CFErrorMessage = %q", msg)
	}

	// 只回 result 的极简网关也认。
	bare := `{"result":{"response":"x"}}`
	if got, err := UnwrapCF([]byte(bare)); err != nil || string(got) != `{"response":"x"}` {
		t.Errorf("无 success 字段时应取 result，得到 %q, %v", got, err)
	}
	if _, err := UnwrapCF([]byte("not json")); err == nil {
		t.Error("非 JSON 应报错")
	}
}
