package upstream

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// adapterChunk 是适配器输出的 OpenAI 形状块。
type adapterChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		Details          struct {
			Cached     int `json:"cached_tokens"`
			CacheWrite int `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// runStreamAdapter 把一段渠道 SSE 喂给适配器，按 readChatStream 的方式
// 消费输出，汇总内容 / 思考 / 用量 / 错误。
func runStreamAdapter(t *testing.T, kind Kind, sse string) (
	content, reasoning string, usage *adapterChunk, sawDone bool, errs []string) {

	t.Helper()
	u := &Upstream{Kind: kind}
	r := u.StreamAdapter(io.NopCloser(strings.NewReader(sse)))
	defer r.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var c adapterChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("适配器输出了非 JSON: %q", data)
		}
		for _, ch := range c.Choices {
			content += ch.Delta.Content
			reasoning += ch.Delta.ReasoningContent
		}
		if c.Usage != nil {
			cp := c
			usage = &cp
		}
		if len(c.Error) > 0 && string(c.Error) != "null" {
			errs = append(errs, string(c.Error))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读适配器输出失败: %v", err)
	}
	return
}

func sse(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

func TestAnthropicStreamAdapter(t *testing.T) {
	in := sse(
		`event: message_start`+"\n"+
			`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
		`event: content_block_delta`+"\n"+
			`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"th"}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ver"}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"dict"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	)
	content, reasoning, usage, done, errs := runStreamAdapter(t, KindAnthropic, in)
	if content != "verdict" || reasoning != "th" {
		t.Errorf("正文/思考 = %q / %q", content, reasoning)
	}
	if usage == nil || usage.Usage.PromptTokens != 18 || usage.Usage.CompletionTokens != 3 ||
		usage.Usage.Details.Cached != 5 || usage.Usage.Details.CacheWrite != 2 {
		t.Errorf("用量翻译不对: %+v", usage)
	}
	if !done || len(errs) != 0 {
		t.Errorf("收尾不对: done=%v errs=%v", done, errs)
	}
}

func TestOpenAIResponsesStreamAdapter(t *testing.T) {
	in := sse(
		`event: response.output_text.delta`+"\n"+
			`data: {"type":"response.output_text.delta","delta":"hello "}`,
		`event: response.reasoning_summary_text.delta`+"\n"+
			`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`,
		`event: response.output_text.delta`+"\n"+
			`data: {"type":"response.output_text.delta","delta":"world"}`,
		`event: response.completed`+"\n"+
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":30,"output_tokens":7,"input_tokens_details":{"cached_tokens":10}}}}`,
	)
	content, reasoning, usage, done, errs := runStreamAdapter(t, KindOpenAIResp, in)
	if content != "hello world" || reasoning != "think" {
		t.Errorf("正文/思考 = %q / %q", content, reasoning)
	}
	if usage == nil || usage.Usage.PromptTokens != 30 || usage.Usage.CompletionTokens != 7 ||
		usage.Usage.Details.Cached != 10 {
		t.Errorf("用量翻译不对: %+v", usage)
	}
	if !done || len(errs) != 0 {
		t.Errorf("收尾不对: done=%v errs=%v", done, errs)
	}
}

func TestGeminiStreamAdapter(t *testing.T) {
	in := sse(
		`data: {"candidates":[{"content":{"parts":[{"text":"o","thought":true},{"text":"ok"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"!"}]}}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"cachedContentTokenCount":1}}`,
	)
	content, reasoning, usage, done, errs := runStreamAdapter(t, KindGemini, in)
	if content != "ok!" || reasoning != "o" {
		t.Errorf("正文/思考 = %q / %q", content, reasoning)
	}
	if usage == nil || usage.Usage.PromptTokens != 9 || usage.Usage.CompletionTokens != 2 ||
		usage.Usage.Details.Cached != 1 {
		t.Errorf("用量翻译不对: %+v", usage)
	}
	if !done || len(errs) != 0 {
		t.Errorf("收尾不对: done=%v errs=%v", done, errs)
	}
}

func TestCloudflareStreamAdapter(t *testing.T) {
	in := sse(
		`data: {"response":"he"}`,
		`data: {"response":"llo","usage":{"prompt_tokens":4,"completion_tokens":1}}`,
		`data: [DONE]`,
	)
	content, _, usage, done, errs := runStreamAdapter(t, KindCloudflare, in)
	if content != "hello" {
		t.Errorf("正文 = %q", content)
	}
	if usage == nil || usage.Usage.PromptTokens != 4 || usage.Usage.CompletionTokens != 1 {
		t.Errorf("用量翻译不对: %+v", usage)
	}
	if !done || len(errs) != 0 {
		t.Errorf("收尾不对: done=%v errs=%v", done, errs)
	}
}

// TestStreamAdapterErrorEvents：各家的错误事件都要变成 readChatStream
// 认得的 error 块，不能让一条挂掉的上游被当成正常空回复。
func TestStreamAdapterErrorEvents(t *testing.T) {
	cases := []struct {
		kind Kind
		in   string
		want string
	}{
		{KindAnthropic, sse(`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), "Overloaded"},
		{KindOpenAIResp, sse(`data: {"type":"error","message":"boom"}`), "boom"},
		{KindOpenAIResp, sse(`data: {"type":"response.failed","response":{"error":{"message":"failed"}}}`), "failed"},
		{KindGemini, sse(`data: {"error":{"message":"quota"}}`), "quota"},
		{KindGemini, sse(`data: {"promptFeedback":{"blockReason":"SAFETY"}}`), "SAFETY"},
	}
	for _, c := range cases {
		_, _, _, _, errs := runStreamAdapter(t, c.kind, c.in)
		if len(errs) != 1 || !strings.Contains(errs[0], c.want) {
			t.Errorf("%s 的错误事件应带出 %q，得到 %v", c.kind, c.want, errs)
		}
	}
}

// TestOpenAIStreamAdapterPassThrough：OpenAI 渠道不做转换，原样透传。
func TestOpenAIStreamAdapterPassThrough(t *testing.T) {
	in := `data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n"
	u := &Upstream{Kind: KindOpenAI}
	r := u.StreamAdapter(io.NopCloser(strings.NewReader(in)))
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil || string(raw) != in {
		t.Errorf("原样透传失败: %q, %v", raw, err)
	}
}
